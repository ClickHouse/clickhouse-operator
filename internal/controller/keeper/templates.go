package keeper

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TemplateHeadlessService(cr *v1.KeeperCluster) *corev1.Service {
	ports := []corev1.ServicePort{
		{
			Protocol:   "TCP",
			Name:       "keeper",
			Port:       PortNative,
			TargetPort: intstr.FromInt32(PortNative),
		},
		{
			Protocol:   "TCP",
			Name:       "keeper-secure",
			Port:       PortNativeSecure,
			TargetPort: intstr.FromInt32(PortNativeSecure),
		},
		{
			Protocol:   "TCP",
			Name:       "raft-ipc",
			Port:       PortInterserver,
			TargetPort: intstr.FromInt32(PortInterserver),
		},
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.HeadlessServiceName(),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Labels, map[string]string{
				util.LabelAppKey: cr.SpecificName(),
			}),
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
	ListenHost   string           `yaml:"listen_host"`
	Path         string           `yaml:"path"`
	Logger       LoggerConfig     `yaml:"logger"`
	Prometheus   PrometheusConfig `yaml:"prometheus"`
	KeeperServer KeeperServer     `yaml:"keeper_server"`
}

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

type PrometheusConfig struct {
	Endpoint            string `yaml:"endpoint"`
	Port                uint16 `yaml:"port"`
	Metrics             bool   `yaml:"metrics"`
	Events              bool   `yaml:"events"`
	AsynchronousMetrics bool   `yaml:"asynchronous_metrics"`
}

type KeeperServer struct {
	TcpPort              uint16        `yaml:"tcp_port"`
	ServerID             string        `yaml:"server_id"`
	StoragePath          string        `yaml:"storage_path"`
	DigestEnabled        bool          `yaml:"digest_enabled"`
	LogStoragePath       string        `yaml:"log_storage_path"`
	SnapshotStoragePath  string        `yaml:"snapshot_storage_path"`
	CoordinationSettings yaml.MapSlice `yaml:"coordination_settings"`
}

func GetConfigurationRevision(cr *v1.KeeperCluster) (string, error) {
	config := generateConfigForSingleReplica(cr, "template")
	hash, err := util.DeepHashObject(config)
	if err != nil {
		return "", fmt.Errorf("hash template configuration: %w", err)
	}

	return hash, nil
}

func GetStatefulSetRevision(cr *v1.KeeperCluster) (string, error) {
	sts := TemplateStatefulSet(cr, "template")
	hash, err := util.DeepHashObject(sts)
	if err != nil {
		return "", fmt.Errorf("hash template StatefulSet: %w", err)
	}

	return hash, nil
}

func TemplateConfigMap(cr *v1.KeeperCluster, replicaID string) (*corev1.ConfigMap, error) {
	config := generateConfigForSingleReplica(cr, replicaID)
	configData, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config for replica %q: %w", replicaID, err)
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
		},
		Data: map[string]string{
			ConfigFileName: string(configData),
		},
	}, nil
}

func TemplateStatefulSet(cr *v1.KeeperCluster, replicaID string) *appsv1.StatefulSet {
	volumes, volumeMounts := buildVolumes(cr, replicaID)

	expectedState := "standalone"
	if len(cr.Status.Replicas) > 1 {
		expectedState = "leader\\|follower"
	}

	keeperContainer := corev1.Container{
		Name:            ContainerName,
		Image:           cr.Spec.Image.String(),
		ImagePullPolicy: cr.Spec.Image.PullPolicy,
		Resources:       cr.Spec.PodPolicy.Resources,
		Env: []corev1.EnvVar{
			{
				Name:  "KEEPER_CONFIG",
				Value: "/etc/clickhouse-keeper/config.yaml",
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Protocol:      "TCP",
				Name:          "keeper",
				ContainerPort: PortNative,
			},
			{
				Protocol:      "TCP",
				Name:          "keeper-secure",
				ContainerPort: PortNativeSecure,
			},
			{
				Protocol:      "TCP",
				Name:          "raft-ipc",
				ContainerPort: PortInterserver,
			},
			{
				Protocol:      "TCP",
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
						fmt.Sprintf("echo 'mntr' | nc 127.0.0.1 -w 4 %d | grep -e 'zk_server_state\\t\\(%s\\)';", PortNative, expectedState),
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
					util.LabelKindKey:         util.LabelKeeperValue,
					util.LabelRoleKey:         util.LabelKeeperValue,
					util.LabelAppK8sKey:       util.LabelKeeperValue,
					util.LabelInstanceK8sKey:  cr.SpecificName(),
					util.LabelKeeperReplicaID: replicaID,
				}),
				Annotations: map[string]string{
					"cluster-autoscaler.kubernetes.io/safe-to-evict": strconv.FormatBool(cr.Spec.SafeToEvict),
					"kubectl.kubernetes.io/default-container":        ContainerName,
				},
			},
			Spec: corev1.PodSpec{
				TerminationGracePeriodSeconds: &cr.Spec.KeeperTerminationGracePeriod,
				TopologySpreadConstraints:     cr.Spec.TopologySpreadConstraints,
				ImagePullSecrets:              cr.Spec.ImagePullSecrets,
				NodeSelector:                  cr.Spec.PodPolicy.NodeSelector,
				Affinity:                      cr.Spec.Affinity,
				RestartPolicy:                 corev1.RestartPolicyAlways,
				DNSPolicy:                     corev1.DNSClusterFirst,
				Tolerations:                   cr.Spec.Tolerations,
				SchedulerName:                 cr.Spec.SchedulerName,
				ServiceAccountName:            cr.Spec.ServiceAccountName,
				Volumes:                       volumes,
				Containers: []corev1.Container{
					keeperContainer,
				},
			},
		},
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ch-storage-volume",
				},
				Spec: cr.Spec.Storage,
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
			Annotations: map[string]string{
				util.AnnotationStatefulSetVersion: BreakingStatefulSetVersion.String(),
			},
		},
		Spec: spec,
	}
}

func generateConfigForSingleReplica(cr *v1.KeeperCluster, replicaID string) Config {
	config := Config{
		ListenHost: "0.0.0.0",
		Path:       BasePath,
		Prometheus: PrometheusConfig{
			Endpoint:            "/metrics",
			Port:                PortPrometheusScrape,
			Metrics:             true,
			Events:              true,
			AsynchronousMetrics: true,
		},
		Logger: LoggerConfig{
			Console: true,
			Level:   "trace",
		},
		KeeperServer: KeeperServer{
			TcpPort:             PortNative,
			ServerID:            replicaID,
			StoragePath:         StoragePath,
			DigestEnabled:       true,
			LogStoragePath:      StorageLogPath,
			SnapshotStoragePath: StorageSnapshotPath,
			CoordinationSettings: yaml.MapSlice{
				yaml.MapItem{Key: "raft_logs_level", Value: "trace"},
				yaml.MapItem{Key: "compress_logs", Value: false},
			},
		},
	}

	if cr.Spec.LoggerConfig.JSONLogs {
		config.Logger.Formatting.Type = "json"
	}

	if cr.Spec.LoggerConfig.LoggerLevel != "" {
		config.Logger.Level = cr.Spec.LoggerConfig.LoggerLevel
	}

	if cr.Spec.LoggerConfig.LogToFile {
		config.Logger.Log = "/var/log/clickhouse-keeper/clickhouse-keeper.log"
		config.Logger.ErrorLog = "/var/log/clickhouse-keeper/clickhouse-keeper.err.log"
		config.Logger.Size = "1000M"
		config.Logger.Count = 50
	}

	return config
}

func buildVolumes(cr *v1.KeeperCluster, replicaID string) ([]corev1.Volume, []corev1.VolumeMount) {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      QuorumConfigVolumeName,
			MountPath: QuorumConfigPath,
		},
		{
			Name:      ConfigVolumeName,
			MountPath: ConfigPath,
		},
		{
			Name:      "ch-storage-volume",
			MountPath: StoragePath,
			SubPath:   "var-lib-clickhouse",
		},
		{
			Name:      "ch-storage-volume",
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

	return volumes, volumeMounts
}
