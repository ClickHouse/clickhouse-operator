package util

import (
	"cmp"
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/davecgh/go-spew/spew"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
// (copied from Kubernetes, with changes).
func DeepHashObject(objectToWrite interface{}) (string, error) {
	//nolint:gosec // Used just for hashing an object, don't care about security
	hasher := md5.New()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	if _, err := printer.Fprintf(hasher, "%#v", objectToWrite); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)[0:]), nil
}

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
		return "", err
	}

	if _, err := printer.Fprintf(hasher, "%#v", obj.GetAnnotations()); err != nil {
		return "", err
	}

	for _, field := range specFields {
		spec := reflect.ValueOf(obj).Elem().FieldByName(field)
		if !spec.IsValid() {
			return "", fmt.Errorf("invalid spec field %s", field)
		}

		if _, err := printer.Fprintf(hasher, "%#v", spec.Interface()); err != nil {
			return "", err
		}

	}

	return hex.EncodeToString(hasher.Sum(nil)[0:]), nil
}

func MergeMaps[Value any](mapsToMerge ...map[string]Value) map[string]Value {
	result := map[string]Value{}
	for _, m := range mapsToMerge {
		maps.Copy(result, m)
	}

	return result
}

func GetFunctionName(temp interface{}) string {
	strs := strings.Split(runtime.FuncForPC(reflect.ValueOf(temp).Pointer()).Name(), ".")
	return strings.TrimSuffix(strs[len(strs)-1], "-fm")
}

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

	if sourceValue.Kind() == reflect.Ptr {
		if !sourceValue.IsNil() && !defaults.IsNil() {
			return applyDefaultRecursive(sourceValue.Elem(), defaults.Elem())
		}
	}

	if sourceValue.IsZero() && !defaults.IsZero() {
		sourceValue.Set(defaults)
	}

	return nil
}

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

func Sha256Hash(password []byte) string {
	sum := sha256.Sum256(password)
	return hex.EncodeToString(sum[:])
}

type executionResultWithID[Id comparable, Result any] struct {
	id     Id
	result Result
	err    error
}

type executionResult[Id comparable, Result any] struct {
	Result Result
	Err    error
}

func ExecuteParallel[Item any, Id comparable, Tasks ~[]Item, Result any](
	tasks Tasks,
	f func(Item) (Id, Result, error),
) map[Id]executionResult[Id, Result] {
	if len(tasks) == 0 {
		return nil
	}

	wg := sync.WaitGroup{}
	var results = make(chan executionResultWithID[Id, Result], len(tasks))

	for _, task := range tasks {
		wg.Add(1)
		go func(task Item) {
			defer wg.Done()
			id, res, err := f(task)
			results <- executionResultWithID[Id, Result]{
				id:     id,
				result: res,
				err:    err,
			}
		}(task)
	}

	wg.Wait()
	close(results)

	resultMap := make(map[Id]executionResult[Id, Result], len(tasks))
	for res := range results {
		resultMap[res.id] = executionResult[Id, Result]{
			Result: res.result,
			Err:    res.err,
		}
	}

	return resultMap
}

func PathToName(path string) string {
	path = strings.Trim(path, "/")
	path = strings.ReplaceAll(path, "/", "-")
	path = strings.ReplaceAll(path, ".", "-")
	return path
}

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
