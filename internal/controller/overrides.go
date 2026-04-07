package controller

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var (
	podSchema       strategicpatch.LookupPatchMeta
	containerSchema strategicpatch.LookupPatchMeta
)

func init() {
	var err error

	podSchema, err = strategicpatch.NewPatchMetaFromStruct(corev1.PodSpec{})
	if err != nil {
		panic(err)
	}

	containerSchema, err = strategicpatch.NewPatchMetaFromStruct(corev1.Container{})
	if err != nil {
		panic(err)
	}
}

// ApplyPodTemplateOverrides merges the user-provided PodTemplateSpec onto an operator-generated
// PodSpec using strategic merge patch with corev1.PodSpec semantics.
// t is treated as read-only; it is only marshaled, never written to.
//
// Volumes and Affinity are handled manually before SMP and excluded from the patch:
//   - Volumes are replaced by name
//   - Affinity term lists are concatenated.
func ApplyPodTemplateOverrides(podSpec *corev1.PodSpec, t *v1.PodTemplateSpec) (corev1.PodSpec, error) {
	for _, uv := range t.Volumes {
		replaced := false
		for i, bv := range podSpec.Volumes {
			if bv.Name == uv.Name {
				podSpec.Volumes[i] = uv
				replaced = true
				break
			}
		}

		if !replaced {
			podSpec.Volumes = append(podSpec.Volumes, uv)
		}
	}

	podSpec.Affinity = mergeAffinity(podSpec.Affinity, t.Affinity)

	patch := *t
	patch.Volumes = nil
	patch.Affinity = nil

	baseJSON, err := json.Marshal(podSpec)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("marshal pod spec: %w", err)
	}

	patchJSON, err := json.Marshal(&patch)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("marshal pod template: %w", err)
	}

	mergedJSON, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(baseJSON, patchJSON, podSchema)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("strategic merge patch pod spec: %w", err)
	}

	var mergedSpec corev1.PodSpec
	if err := json.Unmarshal(mergedJSON, &mergedSpec); err != nil {
		return corev1.PodSpec{}, fmt.Errorf("unmarshal merged pod spec: %w", err)
	}

	return mergedSpec, nil
}

// mergeAffinity appends the scheduling term lists from patch onto base.
func mergeAffinity(base, patch *corev1.Affinity) *corev1.Affinity {
	if patch == nil {
		return base
	}

	if base == nil {
		return patch
	}

	result := *base

	if naPatch := patch.NodeAffinity; naPatch != nil {
		if result.NodeAffinity == nil {
			result.NodeAffinity = naPatch
		} else {
			na := *result.NodeAffinity
			if naPatch.RequiredDuringSchedulingIgnoredDuringExecution != nil {
				if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
					na.RequiredDuringSchedulingIgnoredDuringExecution = naPatch.RequiredDuringSchedulingIgnoredDuringExecution
				} else {
					req := *na.RequiredDuringSchedulingIgnoredDuringExecution
					req.NodeSelectorTerms = append(req.NodeSelectorTerms, naPatch.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms...)
					na.RequiredDuringSchedulingIgnoredDuringExecution = &req
				}
			}

			na.PreferredDuringSchedulingIgnoredDuringExecution = append(
				na.PreferredDuringSchedulingIgnoredDuringExecution,
				naPatch.PreferredDuringSchedulingIgnoredDuringExecution...,
			)
			result.NodeAffinity = &na
		}
	}

	if paPatch := patch.PodAffinity; paPatch != nil {
		if result.PodAffinity == nil {
			result.PodAffinity = paPatch
		} else {
			pa := *result.PodAffinity
			pa.RequiredDuringSchedulingIgnoredDuringExecution = append(pa.RequiredDuringSchedulingIgnoredDuringExecution, paPatch.RequiredDuringSchedulingIgnoredDuringExecution...)
			pa.PreferredDuringSchedulingIgnoredDuringExecution = append(pa.PreferredDuringSchedulingIgnoredDuringExecution, paPatch.PreferredDuringSchedulingIgnoredDuringExecution...)
			result.PodAffinity = &pa
		}
	}

	if paaPatch := patch.PodAntiAffinity; paaPatch != nil {
		if result.PodAntiAffinity == nil {
			result.PodAntiAffinity = paaPatch
		} else {
			paa := *result.PodAntiAffinity
			paa.RequiredDuringSchedulingIgnoredDuringExecution = append(paa.RequiredDuringSchedulingIgnoredDuringExecution, paaPatch.RequiredDuringSchedulingIgnoredDuringExecution...)
			paa.PreferredDuringSchedulingIgnoredDuringExecution = append(paa.PreferredDuringSchedulingIgnoredDuringExecution, paaPatch.PreferredDuringSchedulingIgnoredDuringExecution...)
			result.PodAntiAffinity = &paa
		}
	}

	return &result
}

// ApplyContainerTemplateOverrides merges the user-provided ContainerTemplateSpec onto an
// operator-generated Container using strategic merge patch with corev1.Container semantics.
// t is treated as read-only; it is only marshaled, never written to.
// VolumeMounts are handled separately to allow multiple mounts at the same path; handled by ProjectVolumes.
func ApplyContainerTemplateOverrides(container *corev1.Container, t *v1.ContainerTemplateSpec) (corev1.Container, error) {
	patchContainer := corev1.Container{
		Name:            container.Name,
		Image:           t.Image.String(),
		ImagePullPolicy: t.ImagePullPolicy,
		Resources:       t.Resources,
		// VolumeMounts are handled separately
		Env:             t.Env,
		SecurityContext: t.SecurityContext,
	}

	patchJSON, err := json.Marshal(patchContainer)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("marshal container template: %w", err)
	}

	baseJSON, err := json.Marshal(container)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("marshal container: %w", err)
	}

	mergedJSON, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(baseJSON, patchJSON, containerSchema)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("strategic merge patch container: %w", err)
	}

	var mergedContainer corev1.Container
	if err := json.Unmarshal(mergedJSON, &mergedContainer); err != nil {
		return corev1.Container{}, fmt.Errorf("unmarshal merged container: %w", err)
	}

	mergedContainer.VolumeMounts = append(mergedContainer.VolumeMounts, t.VolumeMounts...)

	return mergedContainer, nil
}
