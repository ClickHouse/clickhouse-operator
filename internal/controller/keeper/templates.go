package keeper

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"dario.cat/mergo"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller"
	"github.com/clickhouse-operator/internal/util"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TemplateHeadlessService(cr *v1.KeeperCluster) *corev1.Service {
	ports := []corev1.ServicePort{
		{
			Protocol:   corev1.ProtocolTCP,
			Name:       "raft-ipc",
			Port:       PortInterserver,
			TargetPort: intstr.FromInt32(PortInterserver),
		},
	}

	if !cr.Spec.Settings.TLS.Enabled || !cr.Spec.Settings.TLS.Required {
		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "keeper",
			Port:       PortNative,
			TargetPort: intstr.FromInt32(PortNative),
		})
	}

	if cr.Spec.Settings.TLS.Enabled {
		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "keeper-secure",
			Port:       PortNativeSecure,
			TargetPort: intstr.FromInt32(PortNativeSecure),
		})
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.HeadlessServiceName(),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey: cr.SpecificName(),
			}),
			Annotations: util.MergeMaps(cr.Spec.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Ports:     ports,
			ClusterIP: "None",
			// This has to be true to acquire quorum
			PublishNotReadyAddresses: true,
			Selector: map[string]string{
				util.LabelAppKey: cr.SpecificName(),
			},
		},
	}
}

func TemplatePodDisruptionBudget(cr *v1.KeeperCluster) *policyv1.PodDisruptionBudget {
	maxUnavailable := intstr.FromInt32(cr.Replicas() / 2)

	return &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.SpecificName(),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey: cr.SpecificName(),
			}),
			Annotations: util.MergeMaps(cr.Spec.Annotations),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					util.LabelAppKey: cr.SpecificName(),
				},
			},
		},
	}
}

type QuorumConfig []ServerConfig

type ServerConfig struct {
	ID       string `yaml:"id"`
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`
}

func TemplateQuorumConfig(cr *v1.KeeperCluster) (*corev1.ConfigMap, error) {
	quorumConfig := generateQuorumConfig(cr)
	revision, err := util.DeepHashObject(quorumConfig)
	if err != nil {
		return nil, fmt.Errorf("hash quorum config: %w", err)
	}

	config := yaml.MapSlice{
		yaml.MapItem{Key: "keeper_server", Value: yaml.MapSlice{
			yaml.MapItem{Key: "raft_configuration", Value: yaml.MapSlice{
				yaml.MapItem{Key: "server", Value: quorumConfig},
			}},
		}},
	}

	rawConfig, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal quorum config: %w", err)
	}

	configmap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.QuorumConfigMapName(),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey:          cr.SpecificName(),
				util.LabelKeeperReplicaID: util.LabelKeeperAllReplicas,
			}),
			Annotations: cr.Spec.Annotations,
		},
		Data: map[string]string{
			QuorumConfigFileName: string(rawConfig),
		},
	}

	util.AddObjectConfigHash(configmap, revision)
	return configmap, nil
}

func generateQuorumConfig(cr *v1.KeeperCluster) QuorumConfig {
	hostnamesByID := cr.HostnamesByID()
	quorumConfig := make(QuorumConfig, 0, len(hostnamesByID))
	for id, hostname := range hostnamesByID {
		quorumConfig = append(quorumConfig, ServerConfig{
			ID:       id,
			Hostname: hostname,
			Port:     PortInterserver,
		})
	}

	slices.SortFunc(quorumConfig, func(a, b ServerConfig) int {
		return strings.Compare(a.ID, b.ID)
	})

	return quorumConfig
}

type Config struct {
	ListenHost   string                      `yaml:"listen_host"`
	Path         string                      `yaml:"path"`
	Logger       controller.LoggerConfig     `yaml:"logger"`
	Prometheus   controller.PrometheusConfig `yaml:"prometheus"`
	KeeperServer KeeperServer                `yaml:"keeper_server"`
	OpenSSL      controller.OpenSSLConfig    `yaml:"openSSL"`
}

type HTTPControl struct {
	Port uint16 `yaml:"port"`
}

type KeeperServer struct {
	TCPPort              uint16         `yaml:"tcp_port,omitempty"`
	TCPPortSecure        uint16         `yaml:"tcp_port_secure,omitempty"`
	ServerID             string         `yaml:"server_id"`
	StoragePath          string         `yaml:"storage_path"`
	DigestEnabled        bool           `yaml:"digest_enabled"`
	LogStoragePath       string         `yaml:"log_storage_path"`
	SnapshotStoragePath  string         `yaml:"snapshot_storage_path"`
	CoordinationSettings map[string]any `yaml:"coordination_settings"`
	HTTPControl          HTTPControl    `yaml:"http_control"`
}

func GetConfigurationRevision(cr *v1.KeeperCluster, extraConfig map[string]any) (string, error) {
	config, err := generateConfigForSingleReplica(cr, extraConfig, "template")
	if err != nil {
		return "", fmt.Errorf("generate template configuration: %w", err)
	}

	hash, err := util.DeepHashObject(config)
	if err != nil {
		return "", fmt.Errorf("hash template configuration: %w", err)
	}

	return hash, nil
}

func GetStatefulSetRevision(cr *v1.KeeperCluster) (string, error) {
	sts, err := TemplateStatefulSet(cr, "template")
	if err != nil {
		return "", fmt.Errorf("generate template StatefulSet: %w", err)
	}

	hash, err := util.DeepHashObject(sts)
	if err != nil {
		return "", fmt.Errorf("hash template StatefulSet: %w", err)
	}

	return hash, nil
}

func TemplateConfigMap(cr *v1.KeeperCluster, extraConfig map[string]any, replicaID string) (*corev1.ConfigMap, error) {
	config, err := generateConfigForSingleReplica(cr, extraConfig, replicaID)
	if err != nil {
		return nil, fmt.Errorf("generate configmap for replica %q: %w", replicaID, err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.ConfigMapNameByReplicaID(replicaID),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey:          cr.SpecificName(),
				util.LabelKeeperReplicaID: replicaID,
			}),
			Annotations: cr.Spec.Annotations,
		},
		Data: map[string]string{
			ConfigFileName: config,
		},
	}, nil
}

func TemplateStatefulSet(cr *v1.KeeperCluster, replicaID string) (*appsv1.StatefulSet, error) {
	volumes, volumeMounts, err := buildVolumes(cr, replicaID)
	if err != nil {
		return nil, fmt.Errorf("build volumes for StatefulSet: %w", err)
	}

	keeperContainer := corev1.Container{
		Name:            ContainerName,
		Image:           cr.Spec.ContainerTemplate.Image.String(),
		ImagePullPolicy: cr.Spec.ContainerTemplate.ImagePullPolicy,
		Resources:       cr.Spec.ContainerTemplate.Resources,
		Env: append([]corev1.EnvVar{
			{
				Name:  "KEEPER_CONFIG",
				Value: QuorumConfigPath + QuorumConfigFileName,
			},
		}, cr.Spec.ContainerTemplate.Env...),
		Ports: []corev1.ContainerPort{
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          "raft-ipc",
				ContainerPort: PortInterserver,
			},
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          "prometheus",
				ContainerPort: PortPrometheusScrape,
			},
		},
		VolumeMounts: volumeMounts,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/bash",
						"-c",
						fmt.Sprintf("wget -qO- http://127.0.0.1:%d/ready | grep -o '\"status\":\"ok\"'", PortHTTPControl),
					},
				},
			},
			TimeoutSeconds:   10,
			PeriodSeconds:    1,
			SuccessThreshold: 1,
			FailureThreshold: 15,
		},
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		// Default capabilities given to ClickHouse keeper.
		// For more informtaion, See https://unofficial-kubernetes.readthedocs.io/en/latest/concepts/policy/container-capabilities/
		// IPC_LOCK
		// •  Lock memory (mlock(2), mlockall(2), mmap(2), shmctl(2));
		// •  Allocate memory using huge pages (memfd_create(2), mmap(2), shmctl(2)).
		// ^^ Needed for better performance.
		//
		// SYS_PTRACE
		// •  Trace arbitrary processes using ptrace(2);
		// •  apply get_robust_list(2) to arbitrary processes;
		// •  transfer data to or from the memory of arbitrary processes using process_vm_readv(2) and process_vm_writev(2);
		// •  inspect processes using kcmp(2).
		// ^^ Needed to get Kernel's performance counters from inside the container (to use perf)
		//
		// PERFMON
		// 	 Employ various performance-monitoring mechanisms, including:
		// •  call perf_event_open(2);
		// •  employ various BPF operations that have performance implications.
		// ^^ Needed to get Kernel's performance counters from inside the container (to use perf)
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
			},
		},
	}

	if !cr.Spec.Settings.TLS.Enabled || !cr.Spec.Settings.TLS.Required {
		keeperContainer.Ports = append(keeperContainer.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "keeper",
			ContainerPort: PortNative,
		})
	}

	if cr.Spec.Settings.TLS.Enabled {
		keeperContainer.Ports = append(keeperContainer.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "keeper-secure",
			ContainerPort: PortNativeSecure,
		})
	}

	keeperPodSpec := corev1.PodSpec{
		TerminationGracePeriodSeconds: cr.Spec.PodTemplate.TerminationGracePeriodSeconds,
		TopologySpreadConstraints:     cr.Spec.PodTemplate.TopologySpreadConstraints,
		ImagePullSecrets:              cr.Spec.PodTemplate.ImagePullSecrets,
		NodeSelector:                  cr.Spec.PodTemplate.NodeSelector,
		Affinity:                      cr.Spec.PodTemplate.Affinity,
		Tolerations:                   cr.Spec.PodTemplate.Tolerations,
		SchedulerName:                 cr.Spec.PodTemplate.SchedulerName,
		ServiceAccountName:            cr.Spec.PodTemplate.ServiceAccountName,
		RestartPolicy:                 corev1.RestartPolicyAlways,
		DNSPolicy:                     corev1.DNSClusterFirst,
		Volumes:                       volumes,
		Containers: []corev1.Container{
			keeperContainer,
		},
	}

	if cr.Spec.PodTemplate.TopologyZoneKey != nil && *cr.Spec.PodTemplate.TopologyZoneKey != "" {
		if keeperPodSpec.Affinity == nil {
			keeperPodSpec.Affinity = &corev1.Affinity{}
		}
		if keeperPodSpec.Affinity.PodAntiAffinity == nil {
			keeperPodSpec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}

		keeperPodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(keeperPodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, corev1.PodAffinityTerm{
			TopologyKey: *cr.Spec.PodTemplate.TopologyZoneKey,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					util.LabelAppKey:  cr.SpecificName(),
					util.LabelRoleKey: util.LabelKeeperValue,
				},
			},
		})

		keeperPodSpec.TopologySpreadConstraints = append(keeperPodSpec.TopologySpreadConstraints, corev1.TopologySpreadConstraint{
			MaxSkew:           1,
			TopologyKey:       *cr.Spec.PodTemplate.TopologyZoneKey,
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					util.LabelAppKey:  cr.SpecificName(),
					util.LabelRoleKey: util.LabelKeeperValue,
				},
			},
		})
	}

	if cr.Spec.PodTemplate.NodeHostnameKey != nil && *cr.Spec.PodTemplate.NodeHostnameKey != "" {
		if keeperPodSpec.Affinity == nil {
			keeperPodSpec.Affinity = &corev1.Affinity{}
		}
		if keeperPodSpec.Affinity.PodAntiAffinity == nil {
			keeperPodSpec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}

		keeperPodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(keeperPodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, corev1.PodAffinityTerm{
			TopologyKey: *cr.Spec.PodTemplate.NodeHostnameKey,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					util.LabelAppKey:  cr.SpecificName(),
					util.LabelRoleKey: util.LabelKeeperValue,
				},
			},
		})
	}

	spec := appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				util.LabelAppKey:          cr.SpecificName(),
				util.LabelKeeperReplicaID: replicaID,
			},
		},
		ServiceName:         cr.HeadlessServiceName(),
		PodManagementPolicy: appsv1.ParallelPodManagement,
		Replicas:            ptr.To[int32](1),
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: cr.SpecificName(),
				Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
					util.LabelAppKey:          cr.SpecificName(),
					util.LabelRoleKey:         util.LabelKeeperValue,
					util.LabelAppK8sKey:       util.LabelKeeperValue,
					util.LabelInstanceK8sKey:  cr.SpecificName(),
					util.LabelKeeperReplicaID: replicaID,
				}),
				Annotations: util.MergeMaps(cr.Spec.Annotations, map[string]string{
					"kubectl.kubernetes.io/default-container": ContainerName,
				}),
			},
			Spec: keeperPodSpec,
		},
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: PersistentVolumeName,
				},
				Spec: cr.Spec.DataVolumeClaimSpec,
			},
		},
		RevisionHistoryLimit: ptr.To[int32](DefaultRevisionHistory),
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.StatefulSetNameByReplicaID(replicaID),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey:          cr.SpecificName(),
				util.LabelAppK8sKey:       util.LabelKeeperValue,
				util.LabelInstanceK8sKey:  cr.SpecificName(),
				util.LabelKeeperReplicaID: replicaID,
			}),
			Annotations: util.MergeMaps(cr.Spec.Annotations, map[string]string{
				util.AnnotationStatefulSetVersion: BreakingStatefulSetVersion.String(),
			}),
		},
		Spec: spec,
	}, nil
}

func generateConfigForSingleReplica(cr *v1.KeeperCluster, extraConfig map[string]any, replicaID string) (string, error) {
	config := Config{
		ListenHost: "0.0.0.0",
		Path:       BaseDataPath,
		Prometheus: controller.DefaultPrometheusConfig(PortPrometheusScrape),
		Logger:     controller.GenerateLoggerConfig(cr.Spec.Settings.Logger, LogPath, "clickhouse-keeper"),
		KeeperServer: KeeperServer{
			TCPPort:             PortNative,
			ServerID:            replicaID,
			StoragePath:         BaseDataPath,
			DigestEnabled:       true,
			LogStoragePath:      StorageLogPath,
			SnapshotStoragePath: StorageSnapshotPath,
			CoordinationSettings: map[string]any{
				"raft_logs_level": "trace",
				"compress_logs":   false,
			},
			HTTPControl: HTTPControl{
				Port: PortHTTPControl,
			},
		},
	}

	if cr.Spec.Settings.TLS.Enabled {
		if cr.Spec.Settings.TLS.Required {
			config.KeeperServer.TCPPort = 0
		}

		config.KeeperServer.TCPPortSecure = PortNativeSecure
		config.OpenSSL = controller.OpenSSLConfig{
			Server: controller.OpenSSLParams{
				CertificateFile:     path.Join(TLSConfigPath, CertificateFilename),
				PrivateKeyFile:      path.Join(TLSConfigPath, KeyFilename),
				CAConfig:            path.Join(TLSConfigPath, CABundleFilename),
				VerificationMode:    "relaxed",
				DisableProtocols:    "sslv2,sslv3",
				PreferServerCiphers: true,
			},
		}
	}

	yamlConfig, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("error marshalling config to yaml: %w", err)
	}

	if len(extraConfig) > 0 {
		configMap := map[string]any{}
		if err := yaml.Unmarshal(yamlConfig, &configMap); err != nil {
			return "", fmt.Errorf("error unmarshalling config from yaml: %w", err)
		}

		if err := mergo.Merge(&configMap, extraConfig, mergo.WithOverride); err != nil {
			return "", fmt.Errorf("error merging config with extraConfig: %w", err)
		}

		yamlConfig, err = yaml.Marshal(configMap)
		if err != nil {
			return "", fmt.Errorf("error marshalling merged config to yaml: %w", err)
		}
	}

	return string(yamlConfig), nil
}

func buildVolumes(cr *v1.KeeperCluster, replicaID string) ([]corev1.Volume, []corev1.VolumeMount, error) {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      QuorumConfigVolumeName,
			MountPath: QuorumConfigPath,
			ReadOnly:  true,
		},
		{
			Name:      ConfigVolumeName,
			MountPath: ConfigPath,
			ReadOnly:  true,
		},
		{
			Name:      PersistentVolumeName,
			MountPath: BaseDataPath,
			SubPath:   "var-lib-clickhouse",
		},
		{
			Name:      PersistentVolumeName,
			MountPath: "/var/log/clickhouse-keeper",
			SubPath:   "var-log-clickhouse",
		},
	}

	defaultConfigMapMode := corev1.ConfigMapVolumeSourceDefaultMode
	volumes := []corev1.Volume{
		{
			Name: QuorumConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &defaultConfigMapMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cr.QuorumConfigMapName(),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  QuorumConfigFileName,
							Path: QuorumConfigFileName,
						},
					},
				},
			},
		},
		{
			Name: ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &defaultConfigMapMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cr.ConfigMapNameByReplicaID(replicaID),
					},
				},
			},
		},
	}

	if cr.Spec.Settings.TLS.Enabled {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      TLSVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})

		volumes = append(volumes, corev1.Volume{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  cr.Spec.Settings.TLS.ServerCertSecret.Name,
					DefaultMode: &TLSFileMode,
					Items: []corev1.KeyToPath{
						{Key: "ca.crt", Path: CABundleFilename},
						{Key: "tls.crt", Path: CertificateFilename},
						{Key: "tls.key", Path: KeyFilename},
					},
				},
			},
		})
	}

	volumes, volumeMounts, err := controller.ProjectVolumes(
		append(volumes, cr.Spec.PodTemplate.Volumes...),
		append(volumeMounts, cr.Spec.ContainerTemplate.VolumeMounts...),
	)
	util.SortKey(volumes, func(volume corev1.Volume) string {
		return volume.Name
	})
	util.SortKey(volumeMounts, func(mount corev1.VolumeMount) string {
		return mount.MountPath
	})

	return volumes, volumeMounts, err
}
