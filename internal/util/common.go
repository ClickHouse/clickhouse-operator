package util

import (
	"context"
	"crypto/md5" //nolint:gosec
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/google/go-cmp/cmp"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
			panic(fmt.Sprintf("invalid spec field %s", field))
		}

		if _, err := printer.Fprintf(hasher, "%#v", spec.Interface()); err != nil {
			return "", err
		}

	}

	return hex.EncodeToString(hasher.Sum(nil)[0:]), nil
}

func MergeMaps(mapsToMerge ...map[string]string) map[string]string {
	result := map[string]string{}
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
	if update.IsZero() {
		return
	}

	if result.IsZero() {
		result.Requeue = true
		result.RequeueAfter = update.RequeueAfter
		return
	}

	result.Requeue = true
	if update.RequeueAfter < result.RequeueAfter {
		result.RequeueAfter = update.RequeueAfter
	}
}

func DiffFilter(specFields []string) cmp.Option {
	return cmp.FilterPath(func(path cmp.Path) bool {
		inMeta := false
		for _, s := range path {
			if f, ok := s.(cmp.StructField); ok {
				switch {
				case inMeta:
					return !slices.Contains([]string{"Labels", "Annotations"}, f.Name())
				case f.Name() == "ObjectMeta":
					inMeta = true
				default:
					return !slices.Contains(specFields, f.Name())
				}
			}
		}

		return false
	}, cmp.Ignore())
}

func ReconcileResource(ctx context.Context, log Logger, cli client.Client, scheme *k8sruntime.Scheme, controller metav1.Object, resource client.Object, specFields ...string) (bool, error) {
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	log = log.With(kind, resource.GetName())

	if err := ctrl.SetControllerReference(controller, resource, scheme); err != nil {
		return false, err
	}

	if len(specFields) == 0 {
		specFields = []string{"Spec"}
	}

	resourceHash, err := DeepHashResource(resource, specFields)
	if err != nil {
		return false, fmt.Errorf("deep hash %s:%s: %w", kind, resource.GetName(), err)
	}
	AddHashWithKeyToAnnotations(resource, AnnotationSpecHash, resourceHash)

	foundResource := resource.DeepCopyObject().(client.Object)
	err = cli.Get(ctx, types.NamespacedName{
		Namespace: resource.GetNamespace(),
		Name:      resource.GetName(),
	}, foundResource)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get %s:%s: %w", kind, resource.GetName(), err)
		}

		log.Info("resource not found, creating")
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return cli.Create(ctx, resource)
		}); err != nil {
			return false, fmt.Errorf("create %s:%s: %w", kind, resource.GetName(), err)
		}
		return true, nil
	}

	if GetSpecHashFromObject(foundResource) == resourceHash {
		log.Debug("resource is up to date")
		return false, nil
	}

	log.Debug(fmt.Sprintf("resource changed, diff: %s", cmp.Diff(foundResource, resource, DiffFilter(specFields))))

	foundResource.SetAnnotations(resource.GetAnnotations())
	foundResource.SetLabels(resource.GetLabels())
	for _, fieldName := range specFields {
		field := reflect.ValueOf(foundResource).Elem().FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			panic(fmt.Sprintf("invalid data field  %s", fieldName))
		}

		field.Set(reflect.ValueOf(resource).Elem().FieldByName(fieldName))
	}

	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return cli.Update(ctx, foundResource)
	}); err != nil {
		return false, fmt.Errorf("update %s:%s: %w", kind, resource.GetName(), err)
	}

	return true, nil
}
