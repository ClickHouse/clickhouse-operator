package v1alpha1

import (
	"errors"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"

	"github.com/ClickHouse/clickhouse-operator/internal"
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

// validateAdditionalVolumeClaimTemplates validates additionalVolumeClaimTemplates not collide with the
// primary data volume name, and be unique within the slice.
func validateAdditionalVolumeClaimTemplates(data *corev1.PersistentVolumeClaimSpec, additional []v1.PersistentVolumeClaimTemplate) []error {
	if len(additional) == 0 {
		return nil
	}

	if data == nil {
		return []error{errors.New("dataVolumeClaimSpec should be set in order to enable additionalVolumeClaimTemplates")}
	}

	var errs []error

	seenNames := make(map[string]struct{})
	for i, tmpl := range additional {
		if tmpl.Name == internal.PersistentVolumeName {
			errs = append(errs, fmt.Errorf("additionalVolumeClaimTemplates[%d].metadata.name %q collides with primary data volume name", i, tmpl.Name))
		}

		if tmpl.Name == "default" {
			errs = append(errs, fmt.Errorf("additionalVolumeClaimTemplates[%d].metadata.name %q is reserved by the ClickHouse default disk", i, tmpl.Name))
		}

		if strings.HasSuffix(tmpl.Name, "-encrypted") {
			errs = append(errs, fmt.Errorf("additionalVolumeClaimTemplates[%d].metadata.name %q is reserved: names ending in \"-encrypted\" collide with generated encrypted disk names", i, tmpl.Name))
		}

		if _, ok := seenNames[tmpl.Name]; ok {
			errs = append(errs, fmt.Errorf("additionalVolumeClaimTemplates has duplicate name %q", tmpl.Name))
		}

		seenNames[tmpl.Name] = struct{}{}
	}

	return errs
}

// validateAdditionalVolumeClaimTemplatesChanges ensures that the set of additional disks is fixed.
func validateAdditionalVolumeClaimTemplatesChanges(oldTemplates, newTemplates []v1.PersistentVolumeClaimTemplate) error {
	if len(oldTemplates) != len(newTemplates) {
		return errors.New("additionalVolumeClaimTemplates cannot be added or removed after cluster creation")
	}

	newNames := make(map[string]struct{}, len(newTemplates))
	for _, t := range newTemplates {
		newNames[t.Name] = struct{}{}
	}

	for _, t := range oldTemplates {
		if _, ok := newNames[t.Name]; !ok {
			return fmt.Errorf("additionalVolumeClaimTemplates names cannot be changed after cluster creation, missing %q", t.Name)
		}
	}

	return nil
}
