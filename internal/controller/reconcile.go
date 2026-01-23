package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
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
		StageHasDiff:          "HasDiff",
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

type Controller interface {
	GetClient() client.Client
	GetScheme() *k8sruntime.Scheme
	GetRecorder() record.EventRecorder
}

func reconcileResource(
	ctx context.Context,
	log util.Logger,
	controller Controller,
	owner client.Object,
	resource client.Object,
	specFields []string,
) (bool, error) {
	cli := controller.GetClient()
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	log = log.With(kind, resource.GetName())

	if err := ctrl.SetControllerReference(owner, resource, controller.GetScheme()); err != nil {
		return false, err
	}

	if len(specFields) == 0 {
		return false, fmt.Errorf("%s specFields is empty", kind)
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
		return true, Create(ctx, controller, owner, resource)
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
			panic(fmt.Sprintf("invalid data field  %s", fieldName))
		}

		field.Set(reflect.ValueOf(resource).Elem().FieldByName(fieldName))
	}

	return true, Update(ctx, controller, owner, foundResource)
}

func ReconcileService(
	ctx context.Context,
	log util.Logger,
	controller Controller,
	owner client.Object,
	service *corev1.Service,
) (bool, error) {
	return reconcileResource(ctx, log, controller, owner, service, []string{"Spec"})
}

func ReconcilePodDisruptionBudget(
	ctx context.Context,
	log util.Logger,
	controller Controller,
	owner client.Object,
	pdb *policyv1.PodDisruptionBudget,
) (bool, error) {
	return reconcileResource(ctx, log, controller, owner, pdb, []string{"Spec"})
}

func ReconcileConfigMap(
	ctx context.Context,
	log util.Logger,
	controller Controller,
	owner client.Object,
	configMap *corev1.ConfigMap,
) (bool, error) {
	return reconcileResource(ctx, log, controller, owner, configMap, []string{"Data", "BinaryData"})
}

func Create(ctx context.Context, controller Controller, owner client.Object, resource client.Object) error {
	recorder := controller.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := controller.GetClient().Create(ctx, resource); err != nil {
		if util.ShouldEmitEvent(err) {
			recorder.Eventf(owner, corev1.EventTypeWarning, v1.EventReasonFailedCreate,
				"Create %s %s failed: %s", kind, resource.GetName(), err.Error())
		}
		return fmt.Errorf("create %s:%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

func Update(ctx context.Context, controller Controller, owner client.Object, resource client.Object) error {
	recorder := controller.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := controller.GetClient().Update(ctx, resource); err != nil {
		if util.ShouldEmitEvent(err) {
			recorder.Eventf(owner, corev1.EventTypeWarning, v1.EventReasonFailedUpdate,
				"Update %s %s failed: %s", kind, resource.GetName(), err.Error())
		}
		return fmt.Errorf("update %s:%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

func Delete(ctx context.Context, controller Controller, owner client.Object, resource client.Object) error {
	recorder := controller.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := controller.GetClient().Delete(ctx, resource); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}

		if util.ShouldEmitEvent(err) {
			recorder.Eventf(owner, corev1.EventTypeWarning, v1.EventReasonFailedDelete,
				"Delete %s %s failed: %s", kind, resource.GetName(), err.Error())
		}
		return fmt.Errorf("delete %s:%s: %w", kind, resource.GetName(), err)
	}

	return nil
}
