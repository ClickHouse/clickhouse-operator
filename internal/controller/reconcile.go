package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/clickhouse-operator/internal/util"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ReplicaUpdateStage int

const (
	StageUpToDate ReplicaUpdateStage = iota
	StageHasDiff
	StageNotReadyUpToDate
	StageUpdating
	StageError
	StageNotExists
)

var (
	mapStatusText = map[ReplicaUpdateStage]string{
		StageUpToDate:         "UpToDate",
		StageHasDiff:          "StatefulSetDiff",
		StageNotReadyUpToDate: "NotReadyUpToDate",
		StageUpdating:         "Updating",
		StageError:            "Error",
		StageNotExists:        "NotExists",
	}
)

func (s ReplicaUpdateStage) String() string {
	return mapStatusText[s]
}

var podErrorStatuses = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff"}

func CheckPodError(ctx context.Context, log util.Logger, client client.Client, sts *appsv1.StatefulSet) (bool, error) {
	var pod corev1.Pod
	podName := fmt.Sprintf("%s-0", sts.Name)

	if err := client.Get(ctx, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      podName,
	}, &pod); err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get clickhouse pod %q: %w", podName, err)
		}

		log.Info("pod is not exists", "pod", podName, "stateful_set", sts.Name)
		return false, nil
	}

	isError := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && slices.Contains(podErrorStatuses, status.State.Waiting.Reason) {
			log.Info("pod in error state", "pod", podName, "reason", status.State.Waiting.Reason)
			isError = true
			break
		}
	}

	return isError, nil
}

func diffFilter(specFields []string) cmp.Option {
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

func ReconcileResource(ctx context.Context, log util.Logger, cli client.Client, scheme *k8sruntime.Scheme, controller metav1.Object, resource client.Object, specFields ...string) (bool, error) {
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	log = log.With(kind, resource.GetName())

	if err := ctrl.SetControllerReference(controller, resource, scheme); err != nil {
		return false, err
	}

	if len(specFields) == 0 {
		specFields = []string{"Spec"}
	}

	resourceHash, err := util.DeepHashResource(resource, specFields)
	if err != nil {
		return false, fmt.Errorf("deep hash %s:%s: %w", kind, resource.GetName(), err)
	}
	util.AddHashWithKeyToAnnotations(resource, util.AnnotationSpecHash, resourceHash)

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

	if util.GetSpecHashFromObject(foundResource) == resourceHash {
		log.Debug("resource is up to date")
		return false, nil
	}

	log.Debug(fmt.Sprintf("resource changed, diff: %s", cmp.Diff(foundResource, resource, diffFilter(specFields))))

	foundResource.SetAnnotations(resource.GetAnnotations())
	foundResource.SetLabels(resource.GetLabels())
	for _, fieldName := range specFields {
		field := reflect.ValueOf(foundResource).Elem().FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			// nit: can't we just return an error here?
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
