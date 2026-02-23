package controller

import (
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// LoggerConfig represents the logger configuration in YAML format.
type LoggerConfig struct {
	Console    bool   `yaml:"console"`
	Level      string `yaml:"level"`
	Formatting struct {
		Type string `yaml:"type"`
	} `yaml:"formatting,omitempty"`
	// File logging settings
	Log      string `yaml:"log,omitempty"`
	ErrorLog string `yaml:"errorlog,omitempty"`
	Size     string `yaml:"size,omitempty"`
	Count    int64  `yaml:"count,omitempty"`
}

// GenerateLoggerConfig generates a LoggerConfig from the given LoggerConfig spec.
func GenerateLoggerConfig(spec v1.LoggerConfig, basePath string, service string) LoggerConfig {
	config := LoggerConfig{
		Console: true,
		Level:   spec.Level,
		Size:    spec.Size,
		Count:   spec.Count,
	}

	if spec.JSONLogs {
		config.Formatting.Type = "json"
	}

	if spec.LogToFile != nil && *spec.LogToFile {
		config.Log = path.Join(basePath, service+".log")
		config.ErrorLog = path.Join(basePath, service+".err.log")
	}

	return config
}

// PrometheusConfig represents the Prometheus configuration in YAML format.
type PrometheusConfig struct {
	Endpoint            string `yaml:"endpoint"`
	Port                uint16 `yaml:"port"`
	Metrics             bool   `yaml:"metrics"`
	Events              bool   `yaml:"events"`
	AsynchronousMetrics bool   `yaml:"asynchronous_metrics"`
}

// DefaultPrometheusConfig returns the default Prometheus configuration for the given port.
func DefaultPrometheusConfig(port uint16) PrometheusConfig {
	return PrometheusConfig{
		Endpoint:            "/metrics",
		Port:                port,
		Metrics:             true,
		Events:              true,
		AsynchronousMetrics: true,
	}
}

// OpenSSLParams represents OpenSSL parameters in YAML format.
type OpenSSLParams struct {
	CertificateFile     string `yaml:"certificateFile"`
	PrivateKeyFile      string `yaml:"privateKeyFile"`
	CAConfig            string `yaml:"caConfig"`
	VerificationMode    string `yaml:"verificationMode"`
	DisableProtocols    string `yaml:"disableProtocols"`
	PreferServerCiphers bool   `yaml:"preferServerCiphers"`
}

// OpenSSLConfig represents the server OpenSSL configuration in YAML format.
type OpenSSLConfig struct {
	Server OpenSSLParams `yaml:"server,omitempty"`
	Client OpenSSLParams `yaml:"client,omitempty"`
}

// ProjectVolumes replaces volumes with the same mount path with a single projected volume.
func ProjectVolumes(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount) ([]corev1.Volume, []corev1.VolumeMount, error) {
	mountPaths := map[string][]corev1.VolumeMount{}

	hasIntersection := false
	for _, mount := range volumeMounts {
		mountPath := strings.TrimRight(mount.MountPath, "/")
		mountPaths[mountPath] = append(mountPaths[mountPath], mount)
		hasIntersection = hasIntersection || len(mountPaths[mountPath]) > 1
	}

	if !hasIntersection {
		return volumes, volumeMounts, nil
	}

	var (
		newVolumes      []corev1.Volume
		newVolumeMounts []corev1.VolumeMount
	)

	volumeMap := map[string]corev1.Volume{}
	for _, volume := range volumes {
		volumeMap[volume.Name] = volume
	}

	addedVolumes := map[string]struct{}{}
	for mountPath, mounts := range mountPaths {
		if len(mounts) == 1 {
			if volume, ok := volumeMap[mounts[0].Name]; ok {
				if _, exists := addedVolumes[volume.Name]; !exists {
					newVolumes = append(newVolumes, volume)
				}
			}

			newVolumeMounts = append(newVolumeMounts, mounts[0])

			continue
		}

		volume := corev1.Volume{
			Name: controllerutil.PathToName(mountPath),
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{},
			},
		}
		volumeMount := corev1.VolumeMount{
			Name:      volume.Name,
			MountPath: mountPath,
		}

		for _, mount := range mounts {
			sourceVolume := volumeMap[mount.Name]
			switch {
			case sourceVolume.ConfigMap != nil:
				volume.Projected.Sources = append(volume.Projected.Sources, corev1.VolumeProjection{
					ConfigMap: &corev1.ConfigMapProjection{
						LocalObjectReference: sourceVolume.ConfigMap.LocalObjectReference,
						Items:                sourceVolume.ConfigMap.Items,
						Optional:             sourceVolume.ConfigMap.Optional,
					},
				})

			case sourceVolume.Secret != nil:
				volume.Projected.Sources = append(volume.Projected.Sources, corev1.VolumeProjection{
					Secret: &corev1.SecretProjection{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: sourceVolume.Secret.SecretName,
						},
						Items:    sourceVolume.Secret.Items,
						Optional: sourceVolume.Secret.Optional,
					},
				})

			default:
				return nil, nil, fmt.Errorf("unsupported volume type for projected volume: %s", sourceVolume.Name)
			}

			volumeMount.ReadOnly = volumeMount.ReadOnly || mount.ReadOnly
		}

		newVolumes = append(newVolumes, volume)
		addedVolumes[volume.Name] = struct{}{}

		newVolumeMounts = append(newVolumeMounts, volumeMount)
	}

	return newVolumes, newVolumeMounts, nil
}
