package controller

import (
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var (
	podSchema       strategicpatch.LookupPatchMeta
	containerSchema strategicpatch.LookupPatchMeta
	jobSchema       strategicpatch.LookupPatchMeta
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

	jobSchema, err = strategicpatch.NewPatchMetaFromStruct(batchv1.Job{})
	if err != nil {
		panic(err)
	}
}

// ApplyPodTemplateOverrides merges PodTemplateSpec onto an operator-generated PodSpec via SMP.
// Inputs are treated as read-only.
//
// Volumes (replaced by name) and Affinity (term lists concatenated) are merged manually and excluded from the patch.
// A non-nil SecurityContext fully replaces the default, see ApplyContainerTemplateOverrides for the rationale.
func ApplyPodTemplateOverrides(podSpec *corev1.PodSpec, t *v1.PodTemplateSpec) (corev1.PodSpec, error) {
	base := podSpec.DeepCopy()

	for _, uv := range t.Volumes {
		replaced := false
		for i, bv := range base.Volumes {
			if bv.Name == uv.Name {
				base.Volumes[i] = uv
				replaced = true
				break
			}
		}

		if !replaced {
			base.Volumes = append(base.Volumes, uv)
		}
	}

	base.Affinity = mergeAffinity(base.Affinity, t.Affinity)

	if t.SecurityContext != nil {
		base.SecurityContext = nil
	}

	patch := *t
	patch.Volumes = nil
	patch.Affinity = nil

	mergedSpec, err := patchResource(base, patch, podSchema)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("patch pod spec: %w", err)
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

// ApplyContainerTemplateOverrides merges ContainerTemplateSpec onto an operator-generated Container via SMP.
// Inputs are treated as read-only.
//
// VolumeMounts are appended; same-path merging is handled by ProjectVolumes.
// SecurityContext and probes are pre-cleared on base before SMP so a non-nil user value
// fully replaces the operator default — deep-merging Capabilities would otherwise leave
// operator-defaulted Add survivors under user Drop:[ALL], breaking `restricted` PodSecurity.
func ApplyContainerTemplateOverrides(container *corev1.Container, t *v1.ContainerTemplateSpec) (corev1.Container, error) {
	base := container.DeepCopy()

	if t.SecurityContext != nil {
		base.SecurityContext = nil
	}

	if t.LivenessProbe != nil {
		base.LivenessProbe = nil
	}

	if t.ReadinessProbe != nil {
		base.ReadinessProbe = nil
	}

	patchContainer := corev1.Container{
		Name:            base.Name,
		Image:           t.Image.String(),
		ImagePullPolicy: t.ImagePullPolicy,
		Resources:       t.Resources,
		// VolumeMounts are handled separately
		Env:             t.Env,
		SecurityContext: t.SecurityContext,
		LivenessProbe:   t.LivenessProbe,
		ReadinessProbe:  t.ReadinessProbe,
	}

	mergedContainer, err := patchResource(base, patchContainer, containerSchema)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("patch container: %w", err)
	}

	mergedContainer.VolumeMounts = append(mergedContainer.VolumeMounts, t.VolumeMounts...)

	return mergedContainer, nil
}

func patchResource[B any, P any](base *B, patch P, schema strategicpatch.LookupPatchMeta) (B, error) {
	var result B

	baseJSON, err := json.Marshal(base)
	if err != nil {
		return result, fmt.Errorf("marshal base: %w", err)
	}

	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return result, fmt.Errorf("marshal patch: %w", err)
	}

	mergedJSON, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(baseJSON, patchJSON, schema)
	if err != nil {
		return result, fmt.Errorf("strategic merge patch: %w", err)
	}

	if err := json.Unmarshal(mergedJSON, &result); err != nil {
		return result, fmt.Errorf("unmarshal merged: %w", err)
	}

	return result, nil
}
