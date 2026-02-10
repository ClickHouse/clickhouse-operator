package v1alpha1

import (
	"errors"
	"fmt"
	"path"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

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
