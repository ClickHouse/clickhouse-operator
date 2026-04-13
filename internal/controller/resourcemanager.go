package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/blang/semver/v4"
	gcmp "github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// Controller provides access to shared Kubernetes dependencies.
type Controller interface {
	GetClient() client.Client
	GetScheme() *runtime.Scheme
	GetRecorder() events.EventRecorder
}

// ResourceManager provides Kubernetes resource modification helpers.
type ResourceManager struct {
	ctrl         Controller
	owner        client.Object
	specificName string
}

// NewResourceManager creates a new ResourceManager instance.
func NewResourceManager(
	ctrl Controller,
	owner interface {
		client.Object
		SpecificName() string
	},
) ResourceManager {
	return ResourceManager{
		ctrl:         ctrl,
		owner:        owner,
		specificName: owner.SpecificName(),
	}
}

// ReconcileResource reconciles a Kubernetes resource by comparing spec hashes.
func (rm *ResourceManager) ReconcileResource(
	ctx context.Context,
	log util.Logger,
	resource client.Object,
	specFields []string,
	action v1.EventAction,
) (bool, error) {
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	log = log.With(kind, resource.GetName())

	if err := ctrlruntime.SetControllerReference(rm.owner, resource, rm.ctrl.GetScheme()); err != nil {
		return false, fmt.Errorf("set %s/%s Ctrl reference: %w", kind, resource.GetName(), err)
	}

	if len(specFields) == 0 {
		return false, fmt.Errorf("%s specFields is empty", kind)
	}

	resourceHash, err := util.DeepHashResource(resource, specFields)
	if err != nil {
		return false, fmt.Errorf("deep hash %s/%s: %w", kind, resource.GetName(), err)
	}

	util.AddSpecHashToObject(resource, resourceHash)

	foundResource := resource.DeepCopyObject().(client.Object) //nolint:forcetypeassert // safe cast

	err = rm.ctrl.GetClient().Get(ctx, types.NamespacedName{
		Namespace: resource.GetNamespace(),
		Name:      resource.GetName(),
	}, foundResource)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get %s/%s: %w", kind, resource.GetName(), err)
		}

		log.Info("resource not found, creating")

		return true, rm.Create(ctx, resource, action)
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

	return true, rm.Update(ctx, foundResource, action)
}

// ReconcileService reconciles a Kubernetes Service resource.
func (rm *ResourceManager) ReconcileService(
	ctx context.Context,
	log util.Logger,
	service *corev1.Service,
	action v1.EventAction,
) (bool, error) {
	return rm.ReconcileResource(ctx, log, service, []string{"Spec"}, action)
}

// ReconcilePodDisruptionBudget reconciles a Kubernetes PodDisruptionBudget resource.
func (rm *ResourceManager) ReconcilePodDisruptionBudget(
	ctx context.Context,
	log util.Logger,
	pdb *policyv1.PodDisruptionBudget,
	action v1.EventAction,
) (bool, error) {
	return rm.ReconcileResource(ctx, log, pdb, []string{"Spec"}, action)
}

// ReconcileConfigMap reconciles a Kubernetes ConfigMap resource.
func (rm *ResourceManager) ReconcileConfigMap(
	ctx context.Context,
	log util.Logger,
	configMap *corev1.ConfigMap,
	action v1.EventAction,
) (bool, error) {
	return rm.ReconcileResource(ctx, log, configMap, []string{"Data", "BinaryData"}, action)
}

// Create creates the given Kubernetes resource and emits events on failure.
func (rm *ResourceManager) Create(ctx context.Context, resource client.Object, action v1.EventAction) error {
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := rm.ctrl.GetClient().Create(ctx, resource); err != nil {
		if util.ShouldEmitEvent(err) {
			rm.ctrl.GetRecorder().Eventf(rm.owner, resource, corev1.EventTypeWarning, v1.EventReasonFailedCreate, action,
				"Create %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("create %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// Update updates the given Kubernetes resource and emits events on failure.
func (rm *ResourceManager) Update(ctx context.Context, resource client.Object, action v1.EventAction) error {
	kind := resource.GetObjectKind().GroupVersionKind().Kind
	fetched := resource.DeepCopyObject().(client.Object) //nolint:forcetypeassert
	first := true

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if !first {
			if err := rm.ctrl.GetClient().Get(ctx, client.ObjectKeyFromObject(fetched), fetched); err != nil {
				return fmt.Errorf("get %s/%s: %w", kind, resource.GetName(), err)
			}

			resource.SetResourceVersion(fetched.GetResourceVersion())
		}

		first = false

		return rm.ctrl.GetClient().Update(ctx, resource)
	})
	if err != nil {
		if util.ShouldEmitEvent(err) {
			rm.ctrl.GetRecorder().Eventf(rm.owner, resource, corev1.EventTypeWarning, v1.EventReasonFailedUpdate, action,
				"Update %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("update %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// Delete deletes the given Kubernetes resource and emits events on failure.
func (rm *ResourceManager) Delete(ctx context.Context, resource client.Object, action v1.EventAction, opts ...client.DeleteOption) error {
	kind := resource.GetObjectKind().GroupVersionKind().Kind

	if err := rm.ctrl.GetClient().Delete(ctx, resource, opts...); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}

		if util.ShouldEmitEvent(err) {
			rm.ctrl.GetRecorder().Eventf(rm.owner, resource, corev1.EventTypeWarning, v1.EventReasonFailedDelete, action,
				"Delete %s %s failed: %s", kind, resource.GetName(), err.Error())
		}

		return fmt.Errorf("delete %s/%s: %w", kind, resource.GetName(), err)
	}

	return nil
}

// GetPVCByStatefulSet returns the PersistentVolumeClaim created by given StatefulSet.
func (rm *ResourceManager) GetPVCByStatefulSet(
	ctx context.Context,
	log util.Logger,
	sts *appsv1.StatefulSet,
) (*corev1.PersistentVolumeClaim, error) {
	if len(sts.Spec.VolumeClaimTemplates) == 0 {
		return nil, fmt.Errorf("StatefulSet %s does not have volume claim templates", sts.Name)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := rm.ctrl.GetClient().Get(ctx, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      fmt.Sprintf("%s-%s-0", sts.Spec.VolumeClaimTemplates[0].Name, sts.Name),
	}, &pvc); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Info("PVC not found for StatefulSet", "statefulset", sts.Name)
			return nil, nil
		}

		return nil, fmt.Errorf("get PVC for StatefulSet %s: %w", sts.Name, err)
	}

	return &pvc, nil
}

// ReplicaUpdateInput contains the parameters needed to reconcile a StatefulSet for a replica.
type ReplicaUpdateInput struct {
	ConfigurationRevision string
	DesiredConfigMap      *corev1.ConfigMap

	StatefulSetRevision string
	ExistingSTS         *appsv1.StatefulSet
	DesiredSTS          *appsv1.StatefulSet
	HasError            bool
	BreakingSTSVersion  semver.Version

	PVCRevision    string
	ExistingPVC    *corev1.PersistentVolumeClaim
	DesiredPVCSpec *corev1.PersistentVolumeClaimSpec
}

// UpdatePVC updates the PersistentVolumeClaim for the given replica ID if it exists and differs from the provided spec.
func (rm *ResourceManager) UpdatePVC(ctx context.Context, log util.Logger, input ReplicaUpdateInput) error {
	if input.DesiredPVCSpec == nil {
		return nil
	}

	if input.ExistingPVC == nil {
		log.Debug("replica PVC not found, skipping update")
		return nil
	}

	log = log.With("pvc", input.ExistingPVC.Name)

	if util.GetSpecHashFromObject(input.ExistingPVC) == input.PVCRevision {
		log.Debug("PVC is up to date")
		return nil
	}

	targetSpec := input.DesiredPVCSpec.DeepCopy()
	if err := util.ApplyDefault(targetSpec, input.ExistingPVC.Spec); err != nil {
		return fmt.Errorf("patch PVC spec: %w", err)
	}

	log.Info("updating PVC", "diff", gcmp.Diff(input.ExistingPVC.Spec, *targetSpec))

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: *input.ExistingPVC.ObjectMeta.DeepCopy(),
		Spec:       *targetSpec,
	}

	util.AddSpecHashToObject(pvc, input.PVCRevision)

	if err := rm.Update(ctx, pvc, v1.EventActionReconciling); err != nil {
		return fmt.Errorf("update PVC: %w", err)
	}

	return nil
}

// ReconcileReplicaResources reconciles a replica's ConfigMap, StatefulSet and PVC.
// Handling Pod restarts on config changes.
func (rm *ResourceManager) ReconcileReplicaResources(
	ctx context.Context,
	log util.Logger,
	input ReplicaUpdateInput,
) (*ctrlruntime.Result, error) {
	configChanged, err := rm.ReconcileConfigMap(ctx, log, input.DesiredConfigMap, v1.EventActionReconciling)
	if err != nil {
		return nil, fmt.Errorf("update replica ConfigMap: %w", err)
	}

	statefulSet := input.DesiredSTS

	if err = ctrlruntime.SetControllerReference(rm.owner, statefulSet, rm.ctrl.GetScheme()); err != nil {
		return nil, fmt.Errorf("set replica StatefulSet controller reference: %w", err)
	}

	if input.ExistingSTS == nil {
		log.Info("replica StatefulSet not found, creating", "statefulset", statefulSet.Name)
		util.AddObjectConfigHash(statefulSet, input.ConfigurationRevision)
		util.AddSpecHashToObject(statefulSet, input.StatefulSetRevision)

		if input.DesiredPVCSpec != nil {
			util.AddSpecHashToObject(&statefulSet.Spec.VolumeClaimTemplates[0], input.PVCRevision)
		}

		if err := rm.Create(ctx, statefulSet, v1.EventActionReconciling); err != nil {
			return nil, fmt.Errorf("create replica: %w", err)
		}

		return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// PVC update failures are non-fatal: the next reconciliation will detect the mismatch
	// via HasDiff and retry. This avoids blocking the STS update on transient PVC conflicts.
	if err = rm.UpdatePVC(ctx, log, input); err != nil {
		log.Warn("failed to update replica PVC", "error", err)
	}

	statefulSet.Spec.VolumeClaimTemplates = input.ExistingSTS.Spec.VolumeClaimTemplates

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(input.ExistingSTS.Annotations[util.AnnotationStatefulSetVersion])
	if err != nil || input.BreakingSTSVersion.GT(v) {
		log.Warn("removing StatefulSet because of a breaking change",
			"found_version", input.ExistingSTS.Annotations[util.AnnotationStatefulSetVersion],
			"expected_version", input.BreakingSTSVersion.String(),
		)

		if err := rm.Delete(ctx, input.ExistingSTS, v1.EventActionReconciling); err != nil {
			return nil, fmt.Errorf("recreate replica: %w", err)
		}

		return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	stsNeedsUpdate := util.GetSpecHashFromObject(input.ExistingSTS) != input.StatefulSetRevision

	// Trigger Pod restart if config changed
	// Always restarts on config changes, need to add check if it is needed.
	if util.GetConfigHashFromObject(input.ExistingSTS) != input.ConfigurationRevision {
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing Pod restart, because of config changes")

		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)

		util.AddObjectConfigHash(statefulSet, input.ConfigurationRevision)

		stsNeedsUpdate = true
	} else if restartedAt, ok := input.ExistingSTS.Spec.Template.Annotations[util.AnnotationRestartedAt]; ok {
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = restartedAt
	}

	if !stsNeedsUpdate {
		log.Debug("StatefulSet is up to date", "statefulset", statefulSet.Name)

		if configChanged {
			return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}

		// Delete stuck pod if in error state so the StatefulSet controller can recreate it
		if input.HasError {
			podName := input.ExistingSTS.Name + "-0"
			pod := &corev1.Pod{}

			err = rm.ctrl.GetClient().Get(ctx, types.NamespacedName{Namespace: input.ExistingSTS.Namespace, Name: podName}, pod)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
				}

				log.Warn("failed to get error pod", "pod", podName, "error", err)

				return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}

			if pod.Labels[appsv1.ControllerRevisionHashLabelKey] != input.ExistingSTS.Status.UpdateRevision {
				log.Info("deleting pod stuck in error state", "pod", podName)

				if err = rm.ctrl.GetClient().Delete(ctx, pod); err != nil {
					log.Warn("failed to delete stuck pod", "pod", podName, "error", err)
				}

				return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
		}

		return nil, nil
	}

	log.Info("updating replica StatefulSet", "statefulset", statefulSet.Name)

	updatedSTS := input.ExistingSTS.DeepCopy()
	updatedSTS.Spec = statefulSet.Spec
	updatedSTS.Annotations = util.MergeMaps(updatedSTS.Annotations, statefulSet.Annotations)
	updatedSTS.Labels = util.MergeMaps(updatedSTS.Labels, statefulSet.Labels)
	util.AddSpecHashToObject(updatedSTS, input.StatefulSetRevision)
	log.Debug("replica StatefulSet diff", "diff", gcmp.Diff(input.ExistingSTS, updatedSTS))

	if err = rm.Update(ctx, updatedSTS, v1.EventActionReconciling); err != nil {
		return nil, fmt.Errorf("update replica: %w", err)
	}

	return &ctrlruntime.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
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
