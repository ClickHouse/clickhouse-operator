package keeper

import (
	"fmt"
	"net"
	"path"
	"slices"
	"strconv"
	"strings"

	"dario.cat/mergo"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

func templateHeadlessService(cr *v1.KeeperCluster) *corev1.Service {
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
			Labels: controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
				controllerutil.LabelAppKey: cr.SpecificName(),
			}),
			Annotations: controllerutil.MergeMaps(cr.Spec.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Ports:     ports,
			ClusterIP: "None",
			// This has to be true to acquire quorum
			PublishNotReadyAddresses: true,
			Selector: map[string]string{
				controllerutil.LabelAppKey: cr.SpecificName(),
			},
		},
	}
}

func templatePodDisruptionBudget(cr *v1.KeeperCluster) *policyv1.PodDisruptionBudget {
	pdb := &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.SpecificName(),
			Namespace: cr.Namespace,
			Labels: controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
				controllerutil.LabelAppKey: cr.SpecificName(),
			}),
			Annotations: controllerutil.MergeMaps(cr.Spec.Annotations),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					controllerutil.LabelAppKey: cr.SpecificName(),
				},
			},
			MaxUnavailable: new(intstr.FromInt32(cr.Replicas() / 2)),
		},
	}

	cr.Spec.PodDisruptionBudget.ApplyOverrides(&pdb.Spec)

	return pdb
}

type quorumConfig []serverConfig

type serverConfig struct {
	ID       string `yaml:"id"`
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`
}

func templateQuorumConfig(r *keeperReconciler) (*corev1.ConfigMap, error) {
	quorumConfig := generateQuorumConfig(r)
	cr := r.Cluster

	revision, err := controllerutil.DeepHashObject(quorumConfig)
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
			Labels: controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
				controllerutil.LabelAppKey:          cr.SpecificName(),
				controllerutil.LabelKeeperReplicaID: controllerutil.LabelKeeperAllReplicas,
			}),
			Annotations: cr.Spec.Annotations,
		},
		Data: map[string]string{
			QuorumConfigFileName: string(rawConfig),
		},
	}

	controllerutil.AddObjectConfigHash(configmap, revision)

	return configmap, nil
}

func generateQuorumConfig(r *keeperReconciler) quorumConfig {
	hostnamesByID := map[v1.KeeperReplicaID]string{}
	for id := range r.ReplicaState {
		hostnamesByID[id] = r.Cluster.HostnameByID(id)
	}

	quorumConfig := make(quorumConfig, 0, len(hostnamesByID))
	for id, hostname := range hostnamesByID {
		quorumConfig = append(quorumConfig, serverConfig{
			ID:       strconv.FormatInt(int64(id), 10),
			Hostname: hostname,
			Port:     PortInterserver,
		})
	}

	slices.SortFunc(quorumConfig, func(a, b serverConfig) int {
		return strings.Compare(a.ID, b.ID)
	})

	return quorumConfig
}

type config struct {
	ListenHost   string                      `yaml:"listen_host"`
	Path         string                      `yaml:"path"`
	Logger       controller.LoggerConfig     `yaml:"logger"`
	Prometheus   controller.PrometheusConfig `yaml:"prometheus"`
	KeeperServer keeperServer                `yaml:"keeper_server"`
	OpenSSL      controller.OpenSSLConfig    `yaml:"openSSL"`
}

type httpControl struct {
	Port uint16 `yaml:"port"`
}

type keeperServer struct {
	TCPPort              uint16         `yaml:"tcp_port,omitempty"`
	TCPPortSecure        uint16         `yaml:"tcp_port_secure,omitempty"`
	ServerID             string         `yaml:"server_id"`
	StoragePath          string         `yaml:"storage_path"`
	DigestEnabled        bool           `yaml:"digest_enabled"`
	LogStoragePath       string         `yaml:"log_storage_path"`
	SnapshotStoragePath  string         `yaml:"snapshot_storage_path"`
	CoordinationSettings map[string]any `yaml:"coordination_settings"`
	HTTPControl          httpControl    `yaml:"http_control"`
}

func getConfigurationRevision(cr *v1.KeeperCluster, extraConfig map[string]any) (string, error) {
	config, err := generateConfigForSingleReplica(cr, extraConfig, 0)
	if err != nil {
		return "", fmt.Errorf("generate template configuration: %w", err)
	}

	hash, err := controllerutil.DeepHashObject(config)
	if err != nil {
		return "", fmt.Errorf("hash template configuration: %w", err)
	}

	return hash, nil
}

func getStatefulSetRevision(cr *v1.KeeperCluster) (string, error) {
	sts, err := templateStatefulSet(cr, 0)
	if err != nil {
		return "", fmt.Errorf("generate template StatefulSet: %w", err)
	}

	sts.Spec.VolumeClaimTemplates = nil

	hash, err := controllerutil.DeepHashObject(sts)
	if err != nil {
		return "", fmt.Errorf("hash template StatefulSet: %w", err)
	}

	return hash, nil
}

func templateConfigMap(cr *v1.KeeperCluster, extraConfig map[string]any, id v1.KeeperReplicaID) (*corev1.ConfigMap, error) {
	config, err := generateConfigForSingleReplica(cr, extraConfig, id)
	if err != nil {
		return nil, fmt.Errorf("generate configmap for replica %q: %w", id, err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        cr.ConfigMapNameByReplicaID(id),
			Namespace:   cr.Namespace,
			Labels:      controllerutil.MergeMaps(cr.Spec.Labels, replicaLabels(cr, id)),
			Annotations: cr.Spec.Annotations,
		},
		Data: map[string]string{
			ConfigFileName: config,
		},
	}, nil
}

func templateStatefulSet(cr *v1.KeeperCluster, id v1.KeeperReplicaID) (*appsv1.StatefulSet, error) {
	podSpec, err := templatePodSpec(cr, id)
	if err != nil {
		return nil, fmt.Errorf("template pod spec: %w", err)
	}

	resourceLabels := controllerutil.MergeMaps(cr.Spec.Labels, replicaLabels(cr, id), map[string]string{
		controllerutil.LabelRoleKey:        controllerutil.LabelKeeperValue,
		controllerutil.LabelAppK8sKey:      controllerutil.LabelKeeperValue,
		controllerutil.LabelInstanceK8sKey: cr.SpecificName(),
	})

	spec := appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: replicaLabels(cr, id),
		},
		ServiceName:         cr.HeadlessServiceName(),
		PodManagementPolicy: appsv1.ParallelPodManagement,
		Replicas:            new(int32(1)),
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: cr.SpecificName(),
				Labels:       resourceLabels,
				Annotations: controllerutil.MergeMaps(cr.Spec.Annotations, map[string]string{
					"kubectl.kubernetes.io/default-container": ContainerName,
				}),
			},
			Spec: podSpec,
		},
		RevisionHistoryLimit: new(int32(DefaultRevisionHistory)),
	}

	if cr.Spec.DataVolumeClaimSpec != nil {
		spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:        internal.PersistentVolumeName,
				Labels:      resourceLabels,
				Annotations: cr.Spec.Annotations,
			},
			Spec: *cr.Spec.DataVolumeClaimSpec.DeepCopy(),
		}}
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.StatefulSetNameByReplicaID(id),
			Namespace: cr.Namespace,
			Labels:    resourceLabels,
			Annotations: controllerutil.MergeMaps(cr.Spec.Annotations, map[string]string{
				controllerutil.AnnotationStatefulSetVersion: breakingStatefulSetVersion.String(),
			}),
		},
		Spec: spec,
	}, nil
}

func replicaLabels(cr *v1.KeeperCluster, id v1.KeeperReplicaID) map[string]string {
	labels := id.Labels()
	labels[controllerutil.LabelAppKey] = cr.SpecificName()
	return labels
}

func generateConfigForSingleReplica(cr *v1.KeeperCluster, extraConfig map[string]any, id v1.KeeperReplicaID) (string, error) {
	config := config{
		ListenHost: "0.0.0.0",
		Path:       internal.KeeperDataPath,
		Prometheus: controller.DefaultPrometheusConfig(PortPrometheusScrape),
		Logger:     controller.GenerateLoggerConfig(cr.Spec.Settings.Logger, LogPath, "clickhouse-keeper"),
		KeeperServer: keeperServer{
			TCPPort:             PortNative,
			ServerID:            strconv.FormatInt(int64(id), 10),
			StoragePath:         internal.KeeperDataPath,
			DigestEnabled:       true,
			LogStoragePath:      StorageLogPath,
			SnapshotStoragePath: StorageSnapshotPath,
			CoordinationSettings: map[string]any{
				"raft_logs_level": "trace",
				"compress_logs":   false,
			},
			HTTPControl: httpControl{
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

func templatePodSpec(cr *v1.KeeperCluster, id v1.KeeperReplicaID) (corev1.PodSpec, error) {
	container, err := templateContainer(cr)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("template container: %w", err)
	}

	volumes := buildVolumes(cr, id)
	controllerutil.SortKey(volumes, func(v corev1.Volume) string { return v.Name })

	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
		DNSPolicy:     corev1.DNSClusterFirst,
		Volumes:       volumes,
		Containers:    []corev1.Container{container},
	}

	podTemplate := cr.Spec.PodTemplate

	if podTemplate.TopologyZoneKey != nil && *podTemplate.TopologyZoneKey != "" {
		if podSpec.Affinity == nil {
			podSpec.Affinity = &corev1.Affinity{}
		}

		if podSpec.Affinity.PodAntiAffinity == nil {
			podSpec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}

		podSpec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			podSpec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			corev1.WeightedPodAffinityTerm{
				Weight: MaximalAffinityWeight,
				PodAffinityTerm: corev1.PodAffinityTerm{
					TopologyKey: *podTemplate.TopologyZoneKey,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							controllerutil.LabelAppKey:  cr.SpecificName(),
							controllerutil.LabelRoleKey: controllerutil.LabelKeeperValue,
						},
					},
				},
			})

		podSpec.TopologySpreadConstraints = append(
			podSpec.TopologySpreadConstraints,
			corev1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       *podTemplate.TopologyZoneKey,
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						controllerutil.LabelAppKey:  cr.SpecificName(),
						controllerutil.LabelRoleKey: controllerutil.LabelKeeperValue,
					},
				},
			})
	}

	if podTemplate.NodeHostnameKey != nil && *podTemplate.NodeHostnameKey != "" {
		if podSpec.Affinity == nil {
			podSpec.Affinity = &corev1.Affinity{}
		}

		if podSpec.Affinity.PodAntiAffinity == nil {
			podSpec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}

		podSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
			podSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			corev1.PodAffinityTerm{
				TopologyKey: *podTemplate.NodeHostnameKey,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						controllerutil.LabelAppKey:  cr.SpecificName(),
						controllerutil.LabelRoleKey: controllerutil.LabelKeeperValue,
					},
				},
			})
	}

	podSpec, err = controller.ApplyPodTemplateOverrides(&podSpec, &cr.Spec.PodTemplate)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("apply pod template overrides: %w", err)
	}

	podSpec.Volumes, podSpec.Containers[0].VolumeMounts, err = controller.ProjectVolumes(
		podSpec.Volumes, podSpec.Containers[0].VolumeMounts)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("project volumes: %w", err)
	}

	return podSpec, nil
}

func templateContainer(cr *v1.KeeperCluster) (corev1.Container, error) {
	probeAction := corev1.ExecAction{
		Command: []string{"/bin/bash", "-c",
			fmt.Sprintf("wget -qO- http://%s/ready | grep -o '\"status\":\"ok\"'",
				net.JoinHostPort("127.0.0.1", strconv.Itoa(PortHTTPControl)),
			),
		},
	}

	livenessProbe := controller.DefaultLivenessProbeSettings
	livenessProbe.ProbeHandler = corev1.ProbeHandler{
		Exec: &probeAction,
	}

	readinessProbe := controller.DefaultReadinessProbeSettings
	readinessProbe.ProbeHandler = corev1.ProbeHandler{
		Exec: &probeAction,
	}

	container := corev1.Container{
		Name: ContainerName,
		Env: []corev1.EnvVar{
			{
				Name:  "KEEPER_CONFIG",
				Value: QuorumConfigPath + QuorumConfigFileName,
			},
		},
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
		VolumeMounts:             buildMounts(cr),
		LivenessProbe:            &livenessProbe,
		ReadinessProbe:           &readinessProbe,
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		// Default capabilities given to ClickHouse keeper.
		// For more information, see https://unofficial-kubernetes.readthedocs.io/en/latest/concepts/policy/container-capabilities/
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
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "keeper",
			ContainerPort: PortNative,
		})
	}

	if cr.Spec.Settings.TLS.Enabled {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "keeper-secure",
			ContainerPort: PortNativeSecure,
		})
	}

	container, err := controller.ApplyContainerTemplateOverrides(&container, &cr.Spec.ContainerTemplate)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("apply container template overrides: %w", err)
	}

	return container, nil
}

// buildVolumes returns the operator-generated volumes for the pod.
// User volumes from PodTemplate are NOT included here; they are merged
// by ApplyPodTemplateOverrides in templatePodSpec.
func buildVolumes(cr *v1.KeeperCluster, id v1.KeeperReplicaID) []corev1.Volume {
	defaultConfigMapMode := corev1.ConfigMapVolumeSourceDefaultMode
	volumes := []corev1.Volume{
		{
			Name: internal.QuorumConfigVolumeName,
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
			Name: internal.ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &defaultConfigMapMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cr.ConfigMapNameByReplicaID(id),
					},
				},
			},
		},
	}

	if cr.Spec.Settings.TLS.Enabled {
		volumes = append(volumes, corev1.Volume{
			Name: internal.TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  cr.Spec.Settings.TLS.ServerCertSecret.Name,
					DefaultMode: new(controller.TLSFileMode),
					Items: []corev1.KeyToPath{
						{Key: "ca.crt", Path: CABundleFilename},
						{Key: "tls.crt", Path: CertificateFilename},
						{Key: "tls.key", Path: KeyFilename},
					},
				},
			},
		})
	}

	return volumes
}

// buildMounts returns the operator-generated volume mounts for the main container.
// User mounts from ContainerTemplate are NOT included here; they are merged
// by ApplyContainerTemplateOverrides in templatePodSpec.
func buildMounts(cr *v1.KeeperCluster) []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      internal.QuorumConfigVolumeName,
			MountPath: QuorumConfigPath,
			ReadOnly:  true,
		},
		{
			Name:      internal.ConfigVolumeName,
			MountPath: ConfigPath,
			ReadOnly:  true,
		},
	}

	if cr.Spec.DataVolumeClaimSpec != nil {
		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{
				Name:      internal.PersistentVolumeName,
				MountPath: internal.KeeperDataPath,
				SubPath:   "var-lib-clickhouse",
			},
			corev1.VolumeMount{
				Name:      internal.PersistentVolumeName,
				MountPath: "/var/log/clickhouse-keeper",
				SubPath:   "var-log-clickhouse",
			})
	}

	if cr.Spec.Settings.TLS.Enabled {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      internal.TLSVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})
	}

	return volumeMounts
}
