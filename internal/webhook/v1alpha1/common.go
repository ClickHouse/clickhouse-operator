package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// ValidateCustomVolumeMounts validates that the provided volume mounts correspond to defined volumes and
// do not use any reserved volume names. It returns a slice of errors for any validation issues found.
func ValidateCustomVolumeMounts(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, reservedVolumeNames []string) []error {
	var errs []error

	definedVolumes := make(map[string]corev1.Volume, len(volumes))
	for _, volume := range volumes {
		if _, ok := definedVolumes[volume.Name]; ok {
			err := fmt.Errorf("the volume '%s' is defined multiple times", volume.Name)
			errs = append(errs, err)
			continue
		}

		definedVolumes[volume.Name] = volume
	}

	for _, volumeMount := range volumeMounts {
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

	return errs
}
