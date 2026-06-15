package controller

import (
	"context"
	"fmt"

	gcmp "github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// DesiredPVCs returns the desired PVCs (spec only) keyed by volumeClaimTemplate name.
func DesiredPVCs(dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec, additional []v1.PersistentVolumeClaimTemplate) map[string]*corev1.PersistentVolumeClaim {
	if dataVolumeClaimSpec == nil {
		return nil
	}

	pvcs := make(map[string]*corev1.PersistentVolumeClaim, len(additional)+1)
	pvcs[internal.PersistentVolumeName] = &corev1.PersistentVolumeClaim{Spec: *dataVolumeClaimSpec.DeepCopy()}

	for i := range additional {
		pvcs[additional[i].Name] = &corev1.PersistentVolumeClaim{Spec: *additional[i].Spec.DeepCopy()}
	}

	return pvcs
}

// PVCRevisions computes the desired spec revision for each PVC, keyed by name.
func PVCRevisions(pvcs map[string]*corev1.PersistentVolumeClaim) (map[string]string, error) {
	if len(pvcs) == 0 {
		return nil, nil
	}

	revisions := make(map[string]string, len(pvcs))
	for name, pvc := range pvcs {
		revision, err := util.DeepHashObject(pvc.Spec)
		if err != nil {
			return nil, fmt.Errorf("hash PVC %q spec: %w", name, err)
		}

		revisions[name] = revision
	}

	return revisions, nil
}

// GetReplicaPVCs returns the StatefulSet's PVCs keyed by disk name, matched by disk label.
func (rm *ResourceManager) GetReplicaPVCs(ctx context.Context, sts *appsv1.StatefulSet) (map[string]*corev1.PersistentVolumeClaim, error) {
	if len(sts.Spec.VolumeClaimTemplates) == 0 || sts.Spec.Selector == nil {
		return nil, nil
	}

	var list corev1.PersistentVolumeClaimList
	if err := rm.ctrl.GetClient().List(ctx, &list,
		client.InNamespace(sts.Namespace),
		client.MatchingLabels(sts.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, fmt.Errorf("list PVCs for StatefulSet %s: %w", sts.Name, err)
	}

	pvcs := make(map[string]*corev1.PersistentVolumeClaim, len(list.Items))
	for i := range list.Items {
		name, ok := list.Items[i].Labels[util.LabelDiskName]
		if !ok {
			name = internal.PersistentVolumeName
		}

		if _, ok = pvcs[name]; ok {
			return nil, fmt.Errorf("duplicate PVC %q", name)
		}

		pvcs[name] = &list.Items[i]
	}

	return pvcs, nil
}

func (rm *ResourceManager) updatePVC(
	ctx context.Context,
	log util.Logger,
	existing, desired *corev1.PersistentVolumeClaim,
	revision string,
) error {
	if existing == nil || desired == nil {
		return nil
	}

	if util.GetSpecHashFromObject(existing) == revision {
		return nil
	}

	targetSpec := desired.Spec.DeepCopy()
	if err := util.ApplyDefault(targetSpec, existing.Spec); err != nil {
		return fmt.Errorf("patch PVC %s spec: %w", existing.Name, err)
	}

	log.Info("updating PVC", "pvc", existing.Name, "diff", gcmp.Diff(existing.Spec, *targetSpec))

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: *existing.ObjectMeta.DeepCopy(),
		Spec:       *targetSpec,
	}
	util.AddSpecHashToObject(pvc, revision)

	if err := rm.Update(ctx, pvc, v1.EventActionReconciling); err != nil {
		return fmt.Errorf("update PVC %s: %w", existing.Name, err)
	}

	return nil
}
