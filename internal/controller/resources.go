package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	gcmp "github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// ReplicaUpdateStage represents the stage of updating a ClickHouse replica. Used in reconciliation process.
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

// CheckPodError checks if the pod of the given StatefulSet have permanent errors preventing it from starting.
func CheckPodError(ctx context.Context, log util.Logger, client client.Client, sts *appsv1.StatefulSet) (bool, error) {
	var pod corev1.Pod

	podName := sts.Name + "-0"

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

func diffFilter(specFields []string) gcmp.Option {
	return gcmp.FilterPath(func(path gcmp.Path) bool {
		inMeta := false
		for _, s := range path {
			if f, ok := s.(gcmp.StructField); ok {
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
	}, gcmp.Ignore())
}

func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) reconcileResource(
	ctx context.Context,
	log util.Logger,
	resource client.Object,
	specFields []string,
) (bool, error) {
	cli := r.GetClient()
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	log = log.With(kind, resource.GetName())

	if err := ctrlruntime.SetControllerReference(r.Cluster, resource, r.GetScheme()); err != nil {
		return false, fmt.Errorf("set %s/%s Ctrl reference: %w", kind, resource.GetName(), err)
	}

	if len(specFields) == 0 {
		return false, fmt.Errorf("%s specFields is empty", kind)
	}

	resourceHash, err := util.DeepHashResource(resource, specFields)
	if err != nil {
		return false, fmt.Errorf("deep hash %s/%s: %w", kind, resource.GetName(), err)
	}

	util.AddHashWithKeyToAnnotations(resource, util.AnnotationSpecHash, resourceHash)

	foundResource := resource.DeepCopyObject().(client.Object) //nolint:forcetypeassert // safe cast

	err = cli.Get(ctx, types.NamespacedName{
		Namespace: resource.GetNamespace(),
		Name:      resource.GetName(),
	}, foundResource)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get %s/%s: %w", kind, resource.GetName(), err)
		}

		log.Info("resource not found, creating")

		return true, r.Create(ctx, resource)
	}

	if util.GetSpecHashFromObject(foundResource) == resourceHash {
		log.Debug("resource is up to date")
		return false, nil
	}

	log.Debug("resource changed, diff: " + gcmp.Diff(foundResource, resource, diffFilter(specFields)))

	foundResource.SetAnnotations(resource.GetAnnotations())
	foundResource.SetLabels(resource.GetLabels())

	for _, fieldName := range specFields {
		field := reflect.ValueOf(foundResource).Elem().FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			panic("invalid data field  " + fieldName)
		}

		field.Set(reflect.ValueOf(resource).Elem().FieldByName(fieldName))
	}

	return true, r.Update(ctx, foundResource)
}

// ReconcileService reconciles a Kubernetes Service resource.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) ReconcileService(
	ctx context.Context,
	log util.Logger,
	service *corev1.Service,
) (bool, error) {
	return r.reconcileResource(ctx, log, service, []string{"Spec"})
}

// ReconcilePodDisruptionBudget reconciles a Kubernetes PodDisruptionBudget resource.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) ReconcilePodDisruptionBudget(
	ctx context.Context,
	log util.Logger,
	pdb *policyv1.PodDisruptionBudget,
) (bool, error) {
	return r.reconcileResource(ctx, log, pdb, []string{"Spec"})
}

// ReconcileConfigMap reconciles a Kubernetes ConfigMap resource.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) ReconcileConfigMap(
	ctx context.Context,
	log util.Logger,
	configMap *corev1.ConfigMap,
) (bool, error) {
	return r.reconcileResource(ctx, log, configMap, []string{"Data", "BinaryData"})
}

// Create creates the given Kubernetes resource and emits events on failure.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) Create(ctx context.Context, resource client.Object) error {
	recorder := r.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := r.GetClient().Create(ctx, resource); err != nil {
		if util.ShouldEmitEvent(err) {
			recorder.Eventf(r.Cluster, corev1.EventTypeWarning, v1.EventReasonFailedCreate,
				"Create %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("create %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// Update updates the given Kubernetes resource and emits events on failure.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) Update(ctx context.Context, resource client.Object) error {
	recorder := r.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := r.GetClient().Update(ctx, resource); err != nil {
		if util.ShouldEmitEvent(err) {
			recorder.Eventf(r.Cluster, corev1.EventTypeWarning, v1.EventReasonFailedUpdate,
				"Update %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("update %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// Delete deletes the given Kubernetes resource and emits events on failure.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) Delete(ctx context.Context, resource client.Object) error {
	recorder := r.GetRecorder()
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := r.GetClient().Delete(ctx, resource); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}

		if util.ShouldEmitEvent(err) {
			recorder.Eventf(r.Cluster, corev1.EventTypeWarning, v1.EventReasonFailedDelete,
				"Delete %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("delete %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// UpdatePVC updates the PersistentVolumeClaim for the given replica ID if it exists and differs from the provided spec.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) UpdatePVC(
	ctx context.Context,
	log util.Logger,
	id ReplicaID,
	volumeSpec corev1.PersistentVolumeClaimSpec,
) error {
	cli := r.GetClient()

	var pvcs corev1.PersistentVolumeClaimList

	req := util.AppRequirements(r.Cluster.GetNamespace(), r.Cluster.SpecificName())
	for k, v := range id.Labels() {
		idReq, _ := labels.NewRequirement(k, selection.Equals, []string{v})
		req.LabelSelector = req.LabelSelector.Add(*idReq)
	}

	log.Debug("listing replica PVCs", "replica_id", id, "selector", req.LabelSelector.String())

	if err := cli.List(ctx, &pvcs, req); err != nil {
		return fmt.Errorf("list replica %v PVCs: %w", id, err)
	}

	if len(pvcs.Items) == 0 {
		log.Info("no PVCs found for replica, skipping update", "replica_id", id)
		return nil
	}

	if len(pvcs.Items) > 1 {
		pvcNames := make([]string, len(pvcs.Items))
		for i, pvc := range pvcs.Items {
			pvcNames[i] = pvc.Name
		}

		return fmt.Errorf("found multiple PVCs for replica %v: %v", id, pvcNames)
	}

	if gcmp.Equal(pvcs.Items[0].Spec, volumeSpec) {
		log.Debug("replica PVC is up to date", "pvc", pvcs.Items[0].Name)
		return nil
	}

	targetSpec := volumeSpec.DeepCopy()
	if err := util.ApplyDefault(targetSpec, pvcs.Items[0].Spec); err != nil {
		return fmt.Errorf("apply patch to replica PVC %v: %w", id, err)
	}

	log.Info("updating replica PVC", "pvc", pvcs.Items[0].Name, "diff", gcmp.Diff(pvcs.Items[0].Spec, targetSpec))

	pvcs.Items[0].Spec = *targetSpec
	if err := r.Update(ctx, &pvcs.Items[0]); err != nil {
		return fmt.Errorf("update replica PVC %v: %w", id, err)
	}

	return nil
}
