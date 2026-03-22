package v1alpha1

import (
	"errors"
	"fmt"
	"path"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
)

// additionalVolumeNameRe matches names that are valid as Kubernetes volume / PVC names
// (DNS label subset: lowercase alphanumeric and hyphens, must start and end with alphanumeric).
// Hyphens are automatically converted to underscores when the name is written into the
// ClickHouse disk configuration, so users only need to follow Kubernetes naming rules here.
var additionalVolumeNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validateCustomVolumeMounts validates that the provided volume mounts correspond to defined volumes and
// do not use any reserved volume names. It returns a slice of errors for any validation issues found.
func validateVolumes(
	volumes []corev1.Volume,
	volumeMounts []corev1.VolumeMount,
	reservedVolumeNames []string,
	dataPath string,
	hasDataVolume bool,
) (admission.Warnings, []error) {
	var (
		warnings admission.Warnings
		errs     []error
	)

	dataPath = path.Clean(dataPath)

	definedVolumes := make(map[string]corev1.Volume, len(volumes))
	for _, volume := range volumes {
		if _, ok := definedVolumes[volume.Name]; ok {
			err := fmt.Errorf("the volume '%s' is defined multiple times", volume.Name)
			errs = append(errs, err)
			continue
		}

		definedVolumes[volume.Name] = volume
	}

	hasMountAtDataPath := false
	for _, volumeMount := range volumeMounts {
		if path.Clean(volumeMount.MountPath) == dataPath {
			hasMountAtDataPath = true
		}

		if _, ok := definedVolumes[volumeMount.Name]; !ok {
			err := fmt.Errorf("the volume mount '%s' is invalid because the volume is not defined", volumeMount.Name)
			errs = append(errs, err)
		}
	}

	for _, reservedName := range reservedVolumeNames {
		if _, ok := definedVolumes[reservedName]; ok {
			err := fmt.Errorf("the volume '%s' is reserved and cannot be used", reservedName)
			errs = append(errs, err)
		}
	}

	if hasDataVolume && hasMountAtDataPath {
		errs = append(errs, fmt.Errorf("cannot mount a custom volume at the data path %q when a data volume is defined", dataPath))
	}

	if !hasDataVolume && !hasMountAtDataPath {
		warnings = append(warnings, fmt.Sprintf("no volume is mounted at the data path %q, which may lead to data loss if the cluster is restarted", dataPath))
	}

	return warnings, errs
}

// validateDataVolumeSpecChanges validates that changes to the DataVolumeClaimSpec after cluster creation.
func validateDataVolumeSpecChanges(oldSpec, newSpec *corev1.PersistentVolumeClaimSpec) error {
	if oldSpec == nil && newSpec != nil {
		return errors.New("data volume cannot be added after cluster creation")
	}

	if oldSpec != nil && newSpec == nil {
		return errors.New("data volume cannot be removed after cluster creation")
	}

	return nil
}

// validateAdditionalDataVolumeClaimSpecs validates additionalDataVolumeClaimSpecs:
// - names must not collide with the primary data volume name
// - no duplicate names in the slice
// - no duplicate mount paths in the slice (would cause two PVCs to mount at the same path).
func validateAdditionalDataVolumeClaimSpecs(specs []v1alpha1.AdditionalVolumeClaimSpec) []error {
	var errs []error

	seenNames := make(map[string]struct{})

	seenPaths := make(map[string]struct{})
	for i, spec := range specs {
		if spec.Name == "" {
			errs = append(errs, fmt.Errorf("additionalDataVolumeClaimSpecs[%d].name must not be empty", i))
		} else if !additionalVolumeNameRe.MatchString(spec.Name) {
			errs = append(errs, fmt.Errorf("additionalDataVolumeClaimSpecs[%d].name %q is invalid: must consist of lowercase alphanumeric characters or hyphens, and start and end with an alphanumeric character", i, spec.Name))
		}

		if spec.Name == internal.PersistentVolumeName {
			errs = append(errs, fmt.Errorf("additionalDataVolumeClaimSpecs[%d].name %q collides with primary data volume name", i, spec.Name))
		}

		if _, ok := seenNames[spec.Name]; ok {
			errs = append(errs, fmt.Errorf("additionalDataVolumeClaimSpecs has duplicate name %q", spec.Name))
		}

		seenNames[spec.Name] = struct{}{}

		// Resolve the effective mount path (mirrors WithDefaults logic) for duplicate detection.
		mountPath := spec.MountPath
		if mountPath == "" {
			mountPath = internal.AdditionalDiskBasePath + spec.Name
		}

		if _, ok := seenPaths[mountPath]; ok {
			errs = append(errs, fmt.Errorf("additionalDataVolumeClaimSpecs[%d] has duplicate mountPath %q", i, mountPath))
		}

		seenPaths[mountPath] = struct{}{}
	}

	return errs
}

// validateAdditionalDataVolumeClaimSpecsChanges validates update policy for additionalDataVolumeClaimSpecs:
// - adding new disks is allowed
// - removing existing disks is rejected
// - renaming existing disks is rejected (equivalent to remove+add)
// - updating specs for existing names is allowed.
func validateAdditionalDataVolumeClaimSpecsChanges(oldSpecs, newSpecs []v1alpha1.AdditionalVolumeClaimSpec) error {
	if len(oldSpecs) > 0 && len(newSpecs) == 0 {
		return errors.New("additionalDataVolumeClaimSpecs cannot be removed after cluster creation")
	}

	newNames := make(map[string]struct{}, len(newSpecs))
	for _, s := range newSpecs {
		newNames[s.Name] = struct{}{}
	}

	for _, s := range oldSpecs {
		if _, ok := newNames[s.Name]; !ok {
			return errors.New("additionalDataVolumeClaimSpecs names cannot be removed or renamed after cluster creation")
		}
	}

	return nil
}
