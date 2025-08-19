package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

func ValidateCustomVolumeMounts(volumes []corev1.Volume, VolumeMounts []corev1.VolumeMount, reservedVolumeNames []string) []error {
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

	for _, volumeMount := range VolumeMounts {
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
