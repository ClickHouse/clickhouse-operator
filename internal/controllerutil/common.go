package controllerutil

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"golang.org/x/sync/semaphore"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
// (copied from Kubernetes, with changes).
func DeepHashObject(objectToWrite any) (string, error) {
	//nolint:gosec // Used just for hashing an object, don't care about security
	hasher := md5.New()

	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	if _, err := printer.Fprintf(hasher, "%#v", objectToWrite); err != nil {
		return "", fmt.Errorf("print object for hashing: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)[0:]), nil
}

// DeepHashResource writes specified resource's labels, annotations and spec fields to hash.
func DeepHashResource(obj client.Object, specFields []string) (string, error) {
	//nolint:gosec // Used just for hashing an object, don't care about security
	hasher := md5.New()

	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}

	if _, err := printer.Fprintf(hasher, "%#v", obj.GetLabels()); err != nil {
		return "", fmt.Errorf("print labels for hashing: %w", err)
	}

	if _, err := printer.Fprintf(hasher, "%#v", obj.GetAnnotations()); err != nil {
		return "", fmt.Errorf("print annotations for hashing: %w", err)
	}

	for _, field := range specFields {
		spec := reflect.ValueOf(obj).Elem().FieldByName(field)
		if !spec.IsValid() {
			return "", fmt.Errorf("invalid spec field %s", field)
		}

		if _, err := printer.Fprintf(hasher, "%#v", spec.Interface()); err != nil {
			return "", fmt.Errorf("print spec for hashing: %w", err)
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)[0:]), nil
}

// MergeMaps merges multiple maps into a single map. If the same key exists in multiple maps,
// the value from the last map will be used.
func MergeMaps[Value any](mapsToMerge ...map[string]Value) map[string]Value {
	result := map[string]Value{}
	for _, m := range mapsToMerge {
		if m != nil {
			maps.Copy(result, m)
		}
	}

	return result
}

// GetFunctionName returns the name of the function passed as an argument.
func GetFunctionName(temp any) string {
	strs := strings.Split(runtime.FuncForPC(reflect.ValueOf(temp).Pointer()).Name(), ".")
	return strings.TrimSuffix(strs[len(strs)-1], "-fm")
}

// ApplyDefault recursively applies default values from the 'defaults' struct to the zero-values 'source' struct fields.
func ApplyDefault[T any](source *T, defaults T) error {
	sourceValue := reflect.ValueOf(source).Elem()
	defaultValue := reflect.ValueOf(defaults)
	return applyDefaultRecursive(sourceValue, defaultValue)
}

func applyDefaultRecursive(sourceValue reflect.Value, defaults reflect.Value) error {
	if sourceValue.Kind() == reflect.Struct {
		for i := range sourceValue.NumField() {
			if !sourceValue.Field(i).CanSet() {
				continue
			}

			if err := applyDefaultRecursive(sourceValue.Field(i), defaults.Field(i)); err != nil {
				return fmt.Errorf("apply default value for field %s: %w", sourceValue.Type().Field(i).Name, err)
			}
		}

		return nil
	}

	if sourceValue.Kind() == reflect.Map {
		if sourceValue.IsNil() {
			sourceValue.Set(defaults)
			return nil
		}

		for _, key := range defaults.MapKeys() {
			if sourceValue.MapIndex(key).Kind() == reflect.Invalid {
				sourceValue.SetMapIndex(key, defaults.MapIndex(key))
			}
		}

		return nil
	}

	if sourceValue.Kind() == reflect.Pointer {
		if !sourceValue.IsNil() && !defaults.IsNil() && sourceValue.Elem().Kind() != reflect.Bool {
			return applyDefaultRecursive(sourceValue.Elem(), defaults.Elem())
		}
	}

	if sourceValue.IsZero() && !defaults.IsZero() {
		sourceValue.Set(defaults)
	}

	return nil
}

// UpdateResult merges two ctrl.Result objects, choosing the smallest (soonest) non-zero RequeueAfter duration.
func UpdateResult(result *ctrl.Result, update *ctrl.Result) {
	if update.IsZero() || update.RequeueAfter == 0 {
		return
	}

	if result.IsZero() {
		result.RequeueAfter = update.RequeueAfter
		return
	}

	if update.RequeueAfter < result.RequeueAfter {
		result.RequeueAfter = update.RequeueAfter
	}
}

const (
	alpha    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	numeric  = "0123456789"
	special  = "!@#%^-_+="
	alphabet = alpha + numeric + special
	length   = 32
)

// GenerateRandomBytes generates n cryptographically random bytes.
func GenerateRandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("read random source: %v", err))
	}

	return b
}

// GeneratePassword generates a random password of fixed length using a predefined alphabet.
func GeneratePassword() string {
	password := make([]byte, length)
	if _, err := rand.Read(password); err != nil {
		// This should never happen
		// Method returns error for interface compatibility, implementation panics in case of error
		panic(fmt.Sprintf("read random source: %v", err))
	}

	for i, b := range password {
		password[i] = alphabet[b%byte(len(alphabet))]
	}

	return string(password)
}

// Sha256Hash returns the SHA-256 hash of the given password as a hexadecimal string.
func Sha256Hash(password []byte) string {
	sum := sha256.Sum256(password)
	return hex.EncodeToString(sum[:])
}

type executionResultWithID[Id comparable, Result any] struct {
	id     Id
	result Result
	err    error
}

// ExecutionResult holds the result of task execution along with any error encountered.
type ExecutionResult[Result any] struct {
	Result Result
	Err    error
}

// DefaultParallelism is the default number of tasks running at once.
const DefaultParallelism = 8

var errNestedSemaphore = errors.New("nested ExecuteParallel call with the semaphore held by the calling task")

type parallelOptions struct {
	semaphore   *semaphore.Weighted
	taskTimeout time.Duration
}

type heldSemaphoreKey struct {
	semaphore *semaphore.Weighted
}

// ParallelOption configures ExecuteParallel.
type ParallelOption func(*parallelOptions)

// WithParallelism limits the running tasks to n; calls reusing the option share the limit, a nested call sharing it fails all its tasks.
func WithParallelism(n int) ParallelOption {
	sem := semaphore.NewWeighted(int64(n))

	return func(o *parallelOptions) {
		o.semaphore = sem
	}
}

// WithTaskTimeout bounds the context of each task, counting from the task start.
func WithTaskTimeout(timeout time.Duration) ParallelOption {
	return func(o *parallelOptions) {
		o.taskTimeout = timeout
	}
}

// ExecuteParallel runs 'f' for each id in 'ids' under the semaphore limit; tasks not started before ctx is done fail with its cause.
func ExecuteParallel[Id comparable, Result any](
	ctx context.Context,
	ids []Id,
	f func(context.Context, Id) (Result, error),
	opts ...ParallelOption,
) map[Id]ExecutionResult[Result] {
	if len(ids) == 0 {
		return nil
	}

	var options parallelOptions
	for _, opt := range opts {
		opt(&options)
	}

	if options.semaphore == nil {
		options.semaphore = semaphore.NewWeighted(DefaultParallelism)
	}

	held := heldSemaphoreKey{semaphore: options.semaphore}
	if ctx.Value(held) != nil {
		resultMap := make(map[Id]ExecutionResult[Result], len(ids))
		for _, id := range ids {
			resultMap[id] = ExecutionResult[Result]{Err: errNestedSemaphore}
		}

		return resultMap
	}

	taskCtx := context.WithValue(ctx, held, true)

	wg := sync.WaitGroup{}

	var results = make(chan executionResultWithID[Id, Result], len(ids))

	for _, id := range ids {
		if err := options.semaphore.Acquire(ctx, 1); err != nil {
			results <- executionResultWithID[Id, Result]{id: id, err: context.Cause(ctx)}
			continue
		}

		wg.Go(func() {
			defer options.semaphore.Release(1)

			results <- executeTask(taskCtx, id, f, options.taskTimeout)
		})
	}

	wg.Wait()
	close(results)

	resultMap := make(map[Id]ExecutionResult[Result], len(ids))
	for res := range results {
		resultMap[res.id] = ExecutionResult[Result]{
			Result: res.result,
			Err:    res.err,
		}
	}

	return resultMap
}

func executeTask[Id comparable, Result any](
	ctx context.Context,
	id Id,
	f func(context.Context, Id) (Result, error),
	timeout time.Duration,
) executionResultWithID[Id, Result] {
	if err := context.Cause(ctx); err != nil {
		return executionResultWithID[Id, Result]{id: id, err: err}
	}

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	res, err := f(ctx, id)

	return executionResultWithID[Id, Result]{
		id:     id,
		result: res,
		err:    err,
	}
}

// PathToName converts a filesystem-like path to a name by replacing '/' and '.' with '-'.
func PathToName(path string) string {
	path = strings.Trim(path, "/")
	path = strings.ReplaceAll(path, "/", "-")
	path = strings.ReplaceAll(path, ".", "-")
	return path
}

// SortKey sorts a slice of any type T based on a key function that extracts an ordered value V from T.
func SortKey[T any, V cmp.Ordered](slice []T, key func(T) V) {
	slices.SortFunc(slice, func(a, b T) int {
		return cmp.Compare(key(a), key(b))
	})
}

// ShouldEmitEvent returns whether an error should trigger an event emission.
func ShouldEmitEvent(err error) bool {
	if err == nil {
		return false
	}

	var statusErr *k8serrors.StatusError

	ok := errors.As(err, &statusErr)
	if !ok {
		return false
	}

	if k8serrors.IsForbidden(err) ||
		k8serrors.IsUnauthorized(err) ||
		k8serrors.IsInvalid(err) ||
		k8serrors.IsBadRequest(err) ||
		k8serrors.IsRequestEntityTooLargeError(err) {
		return true
	}

	return false
}

var versionRegex = regexp.MustCompile(`\d+(\.\d+){2,3}`)

// ParseVersion extracts a version string from the given raw input.
func ParseVersion(raw string) (string, error) {
	version := versionRegex.FindAllString(raw, -1)
	if len(version) != 1 {
		return "", fmt.Errorf("failed to parse version: %q", raw)
	}

	return version[0], nil
}
