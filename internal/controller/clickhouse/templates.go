package clickhouse

import (
	"fmt"
	"maps"
	"path"
	"strconv"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller"
	keepercontroller "github.com/clickhouse-operator/internal/controller/keeper"
	"github.com/clickhouse-operator/internal/util"

	"github.com/imdario/mergo"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TemplateHeadlessService(cr *v1.ClickHouseCluster) *corev1.Service {
	ports := []corev1.ServicePort{
		{
			Protocol:   corev1.ProtocolTCP,
			Name:       "prometheus",
			Port:       PortPrometheusScrape,
			TargetPort: intstr.FromInt32(PortPrometheusScrape),
		},
		{
			Protocol:   corev1.ProtocolTCP,
			Name:       "interserver",
			Port:       PortInterserver,
			TargetPort: intstr.FromInt32(PortInterserver),
		},
	}

	if !cr.Spec.Settings.TLS.Enabled || !cr.Spec.Settings.TLS.Required {
		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "native",
			Port:       PortNative,
			TargetPort: intstr.FromInt32(PortNative),
		}, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "http",
			Port:       PortHTTP,
			TargetPort: intstr.FromInt32(PortHTTP),
		})
	}

	if cr.Spec.Settings.TLS.Enabled {
		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "native-secure",
			Port:       PortNativeSecure,
			TargetPort: intstr.FromInt32(PortNativeSecure),
		}, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       "http-secure",
			Port:       PortHTTPSecure,
			TargetPort: intstr.FromInt32(PortHTTPSecure),
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

func TemplatePodDisruptionBudget(cr *v1.ClickHouseCluster, shardID int32) *policyv1.PodDisruptionBudget {
	minAvailable := intstr.FromInt32(1)

	return &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.PodDisruptionBudgetNameByShard(shardID),
			Namespace: cr.Namespace,
			Labels: util.MergeMaps(cr.Spec.Labels, map[string]string{
				util.LabelAppKey: cr.SpecificName(),
			}),
			Annotations: util.MergeMaps(cr.Spec.Annotations),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					util.LabelAppKey:            cr.SpecificName(),
					util.LabelClickHouseShardID: strconv.Itoa(int(shardID)),
				},
			},
		},
	}
}

func TemplateClusterSecrets(cr *v1.ClickHouseCluster, secret *corev1.Secret) (bool, error) {
	secret.Name = cr.SecretName()
	secret.Namespace = cr.Namespace
	secret.Type = corev1.SecretTypeOpaque

	changed := false

	labels := util.MergeMaps(cr.Spec.Labels, map[string]string{
		util.LabelAppKey: cr.SpecificName(),
	})
	if !maps.Equal(labels, secret.Labels) {
		changed = true
		secret.Labels = labels
	}

	annotations := util.MergeMaps(cr.Spec.Annotations)
	if !maps.Equal(annotations, secret.Annotations) {
		changed = true
		secret.Annotations = annotations
	}

	if secret.Data == nil {
		changed = true
		secret.Data = map[string][]byte{}
	}
	for key, template := range SecretsToGenerate {
		if _, ok := secret.Data[key]; !ok {
			changed = true
			secret.Data[key] = []byte(fmt.Sprintf(template, util.GeneratePassword()))
		}
	}
	for key := range secret.Data {
		if _, ok := SecretsToGenerate[key]; !ok {
			changed = true
			delete(secret.Data, key)
		}
	}

	return changed, nil
}

func GetConfigurationRevision(ctx *reconcileContext) (string, error) {
	config, err := generateConfigForSingleReplica(ctx, replicaID{})
	if err != nil {
		return "", fmt.Errorf("generate template configuration: %w", err)
	}

	hash, err := util.DeepHashObject(config)
	if err != nil {
		return "", fmt.Errorf("hash template configuration: %w", err)
	}

	return hash, nil
}

func GetStatefulSetRevision(ctx *reconcileContext) (string, error) {
	sts := TemplateStatefulSet(ctx, replicaID{})
	hash, err := util.DeepHashObject(sts)
	if err != nil {
		return "", fmt.Errorf("hash template StatefulSet: %w", err)
	}

	return hash, nil
}

func TemplateConfigMap(ctx *reconcileContext, id replicaID) (*corev1.ConfigMap, error) {
	config, err := generateConfigForSingleReplica(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("generate config for replica %v: %w", id, err)
	}

	userConfig, err := generateUsersConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate user config for replica %v: %w", id, err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctx.Cluster.ConfigMapNameByReplicaID(id.shardID, id.index),
			Namespace: ctx.Cluster.Namespace,
			Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, id.Labels(), map[string]string{
				util.LabelAppKey: ctx.Cluster.SpecificName(),
			}),
			Annotations: ctx.Cluster.Spec.Annotations,
		},
		Data: map[string]string{
			ConfigFileName: config,
			UsersFileName:  userConfig,
		},
	}, nil
}

func TemplateStatefulSet(ctx *reconcileContext, id replicaID) *appsv1.StatefulSet {
	volumes, volumeMounts := buildVolumes(ctx, id)

	container := corev1.Container{
		Name:            ContainerName,
		Image:           ctx.Cluster.Spec.ContainerTemplate.Image.String(),
		ImagePullPolicy: ctx.Cluster.Spec.ContainerTemplate.ImagePullPolicy,
		Resources:       ctx.Cluster.Spec.ContainerTemplate.Resources,
		Env: []corev1.EnvVar{
			{
				Name:  "CLICKHOUSE_CONFIG",
				Value: ConfigPath + ConfigFileName,
			},
			{
				Name:  "CLICKHOUSE_SKIP_USER_SETUP",
				Value: "1",
			},
			{
				Name: EnvInterserverPassword,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ctx.Cluster.SecretName(),
						},
						Key: SecretKeyInterserverPassword,
					},
				},
			},
			{
				Name: EnvKeeperIdentity,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ctx.Cluster.SecretName(),
						},
						Key: SecretKeyKeeperIdentity,
					},
				},
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          "prometheus",
				ContainerPort: PortPrometheusScrape,
			},
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          "interserver",
				ContainerPort: PortInterserver,
			},
		},
		VolumeMounts: volumeMounts,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/bash",
						"-c",
						fmt.Sprintf("wget -qO- http://127.0.0.1:%d | grep -o Ok.", PortHTTP),
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
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
			},
		},
	}

	if !ctx.Cluster.Spec.Settings.TLS.Enabled || !ctx.Cluster.Spec.Settings.TLS.Required {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "native",
			ContainerPort: PortNative,
		}, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "http",
			ContainerPort: PortHTTP,
		})
	}

	if ctx.Cluster.Spec.Settings.TLS.Enabled {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "native-secure",
			ContainerPort: PortNativeSecure,
		}, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          "http-secure",
			ContainerPort: PortHTTPSecure,
		})
	}

	if ctx.Cluster.Spec.Settings.DefaultUserPassword != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: EnvDefaultUserPassword,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ctx.Cluster.Spec.Settings.DefaultUserPassword.Name,
					},
					Key: ctx.Cluster.Spec.Settings.DefaultUserPassword.Key,
				},
			},
		})
	}

	spec := appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: util.MergeMaps(id.Labels(), map[string]string{
				util.LabelAppKey: ctx.Cluster.SpecificName(),
			}),
		},
		ServiceName:         ctx.Cluster.HeadlessServiceName(),
		PodManagementPolicy: appsv1.ParallelPodManagement,
		Replicas:            ptr.To[int32](1),
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: ctx.Cluster.SpecificName(),
				Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, id.Labels(), map[string]string{
					util.LabelAppKey:         ctx.Cluster.SpecificName(),
					util.LabelKindKey:        util.LabelClickHouseValue,
					util.LabelRoleKey:        util.LabelClickHouseValue,
					util.LabelAppK8sKey:      util.LabelClickHouseValue,
					util.LabelInstanceK8sKey: ctx.Cluster.SpecificName(),
				}),
				Annotations: util.MergeMaps(ctx.Cluster.Spec.Annotations, map[string]string{
					"kubectl.kubernetes.io/default-container": ContainerName,
				}),
			},
			Spec: corev1.PodSpec{
				TerminationGracePeriodSeconds: ctx.Cluster.Spec.PodTemplate.TerminationGracePeriodSeconds,
				TopologySpreadConstraints:     ctx.Cluster.Spec.PodTemplate.TopologySpreadConstraints,
				ImagePullSecrets:              ctx.Cluster.Spec.PodTemplate.ImagePullSecrets,
				NodeSelector:                  ctx.Cluster.Spec.PodTemplate.NodeSelector,
				Affinity:                      ctx.Cluster.Spec.PodTemplate.Affinity,
				Tolerations:                   ctx.Cluster.Spec.PodTemplate.Tolerations,
				SchedulerName:                 ctx.Cluster.Spec.PodTemplate.SchedulerName,
				ServiceAccountName:            ctx.Cluster.Spec.PodTemplate.ServiceAccountName,
				RestartPolicy:                 corev1.RestartPolicyAlways,
				DNSPolicy:                     corev1.DNSClusterFirst,
				Volumes:                       volumes,
				Containers: []corev1.Container{
					container,
				},
			},
		},
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: PersistentVolumeName,
				},
				Spec: ctx.Cluster.Spec.DataVolumeClaimSpec,
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
			Name:      ctx.Cluster.StatefulSetNameByReplicaID(id.shardID, id.index),
			Namespace: ctx.Cluster.Namespace,
			Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, id.Labels(), map[string]string{
				util.LabelAppKey:         ctx.Cluster.SpecificName(),
				util.LabelInstanceK8sKey: ctx.Cluster.SpecificName(),
				util.LabelAppK8sKey:      util.LabelClickHouseValue,
			}),
			Annotations: util.MergeMaps(ctx.Cluster.Spec.Annotations, map[string]string{
				util.AnnotationStatefulSetVersion: BreakingStatefulSetVersion.String(),
			}),
		},
		Spec: spec,
	}
}

type RemoteCluster struct {
	Discovery ClusterDiscovery `yaml:"discovery"`
}

type ClusterDiscovery struct {
	Path  string `yaml:"path"`
	Shard int32  `yaml:"shard"`
}

type KeeperNode struct {
	Host   string `yaml:"host"`
	Port   int32  `yaml:"port"`
	Secure int32  `yaml:"secure,omitempty"` // 0 for insecure, 1 for secure
}

type ZooKeeper struct {
	Nodes    []KeeperNode `yaml:"node"`
	Identity EnvVal
}

type DistributedDDL struct {
	Path    string `yaml:"path"`
	Profile string `yaml:"profile"`
}

type EnvVal struct {
	FromEnv string `yaml:"@from_env"`
}

type Config struct {
	ListenHost                 string                       `yaml:"listen_host"`
	Path                       string                       `yaml:"path"`
	Logger                     controller.LoggerConfig      `yaml:"logger"`
	Prometheus                 controller.PrometheusConfig  `yaml:"prometheus"`
	OpenSSL                    controller.OpenSSLConfig     `yaml:"openSSL"`
	UserDirectories            map[string]map[string]string `yaml:"user_directories,omitempty"`
	Macros                     map[string]string            `yaml:"macros,omitempty"`
	RemoteServers              map[string]RemoteCluster     `yaml:"remote_servers"`
	DistributedDDL             DistributedDDL               `yaml:"distributed_ddl"`
	ZooKeeper                  ZooKeeper                    `yaml:"zookeeper,omitempty"`
	UserDefinedZookeeperPath   string                       `yaml:"user_defined_zookeeper_path"`
	InterserverHTTPCredentials map[string]any               `yaml:"interserver_http_credentials"`
	// TODO log tables
	// TODO merge tree settings, named collections, engines (kafka/rocksdb/etc)

	// Port settings
	InterserverHTTPPort uint16 `yaml:"interserver_http_port"`
	TCPPort             uint16 `yaml:"tcp_port,omitempty"`
	TCPPortSecure       uint16 `yaml:"tcp_port_secure,omitempty"`
	HTTPPort            uint16 `yaml:"http_port,omitempty"`
	HTTPSPort           uint16 `yaml:"https_port,omitempty"`

	AllowExperimentalClusterDiscovery bool `yaml:"allow_experimental_cluster_discovery"`
}

func generateConfigForSingleReplica(ctx *reconcileContext, id replicaID) (string, error) {
	config := Config{
		ListenHost: "0.0.0.0",
		Path:       BaseDataPath,
		Prometheus: controller.DefaultPrometheusConfig(PortPrometheusScrape),
		Logger:     controller.GenerateLoggerConfig(ctx.Cluster.Spec.Settings.Logger, LogPath, "clickhouse-server"),
		UserDirectories: map[string]map[string]string{
			"users_xml": {
				"path": UsersFileName,
			},
			"replicated": {
				"zookeeper_path": KeeperPathUsers,
			},
		},
		Macros: map[string]string{
			"cluster": DefaultClusterName,
			"shard":   strconv.Itoa(int(id.shardID)),
			"replica": strconv.Itoa(int(id.index)),
		},
		RemoteServers: map[string]RemoteCluster{
			DefaultClusterName: {
				Discovery: ClusterDiscovery{
					Path:  KeeperPathDiscovery,
					Shard: id.shardID,
				},
			},
		},
		DistributedDDL: DistributedDDL{
			Path:    KeeperPathDistributedDDL,
			Profile: DefaultProfileName,
		},
		ZooKeeper: ZooKeeper{
			Identity: EnvVal{
				FromEnv: EnvKeeperIdentity,
			},
		},
		UserDefinedZookeeperPath: KeeperPathUDF,
		InterserverHTTPCredentials: map[string]any{
			"user":        InterserverUserName,
			"allow_empty": false,
			"password": EnvVal{
				FromEnv: EnvInterserverPassword,
			},
		},

		// Port settings
		InterserverHTTPPort: PortInterserver,
		TCPPort:             PortNative,
		HTTPPort:            PortHTTP,

		AllowExperimentalClusterDiscovery: true,
	}

	if ctx.Cluster.Spec.Settings.TLS.Enabled {
		if ctx.Cluster.Spec.Settings.TLS.Required {
			config.TCPPort = 0
		}

		config.TCPPortSecure = PortNativeSecure
		config.HTTPSPort = PortHTTPSecure
		config.OpenSSL = controller.OpenSSLConfig{
			Server: controller.OpenSSLParams{
				CertificateFile:     path.Join(TLSConfigPath, CertificateFilename),
				PrivateKeyFile:      path.Join(TLSConfigPath, KeyFilename),
				CAConfig:            path.Join(TLSConfigPath, CABundleFilename),
				VerificationMode:    "relaxed",
				DisableProtocols:    "sslv2,sslv3",
				PreferServerCiphers: true,
			},
			Client: controller.OpenSSLParams{
				CertificateFile:     path.Join(TLSConfigPath, CertificateFilename),
				PrivateKeyFile:      path.Join(TLSConfigPath, KeyFilename),
				CAConfig:            path.Join(TLSConfigPath, CABundleFilename),
				VerificationMode:    "relaxed",
				DisableProtocols:    "sslv2,sslv3",
				PreferServerCiphers: true,
			},
		}
	}

	for _, host := range ctx.keeper.Hostnames() {
		if ctx.Cluster.Spec.Settings.TLS.Enabled {
			config.ZooKeeper.Nodes = append(config.ZooKeeper.Nodes, KeeperNode{
				Host:   host,
				Port:   keepercontroller.PortNativeSecure,
				Secure: 1,
			})
		} else {
			config.ZooKeeper.Nodes = append(config.ZooKeeper.Nodes, KeeperNode{
				Host: host,
				Port: keepercontroller.PortNative,
			})
		}
	}

	yamlConfig, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("error marshalling config to yaml: %w", err)
	}

	if len(ctx.ExtraConfig) > 0 {
		configMap := map[string]any{}
		if err := yaml.Unmarshal(yamlConfig, &configMap); err != nil {
			return "", fmt.Errorf("error unmarshalling config from yaml: %w", err)
		}

		if err := mergo.Merge(&configMap, ctx.ExtraConfig, mergo.WithOverride); err != nil {
			return "", fmt.Errorf("error merging config with extraConfig: %w", err)
		}

		yamlConfig, err = yaml.Marshal(configMap)
		if err != nil {
			return "", fmt.Errorf("error marshalling merged config to yaml: %w", err)
		}
	}

	return string(yamlConfig), nil
}

type querySpec struct {
	Query string `yaml:"query"`
}

type User struct {
	PasswordSha256 string      `yaml:"password_sha256_hex,omitempty"`
	Password       EnvVal      `yaml:"password,omitempty"`
	NoPassword     *struct{}   `yaml:"no_password,omitempty"`
	Profile        string      `yaml:"profile,omitempty"`
	Quota          string      `yaml:"quota,omitempty"`
	Grants         []querySpec `yaml:"grants,omitempty"`
	// TODO add user settings
}

type Profile struct {
	// TODO add profile settings
}

type Quota struct {
	// TODO add quota settings
}

type UserConfig struct {
	Users    map[string]User    `yaml:"users"`
	Profiles map[string]Profile `yaml:"profiles"`
	Quotas   map[string]Quota   `yaml:"quotas"`
}

func generateUsersConfig(ctx *reconcileContext) (string, error) {
	defaultUser := User{
		Profile: DefaultProfileName,
		Quota:   "default",
		Grants: []querySpec{
			{Query: "GRANT ALL ON *.*"},
		},
	}
	if ctx.Cluster.Spec.Settings.DefaultUserPassword != nil {
		defaultUser.Password = EnvVal{FromEnv: EnvDefaultUserPassword}
	} else {
		defaultUser.NoPassword = &struct{}{}
	}

	config := UserConfig{
		Users: map[string]User{
			"default": defaultUser,
			OperatorManagementUsername: {
				Profile:        DefaultProfileName,
				Quota:          "default",
				PasswordSha256: util.Sha256Hash(ctx.secret.Data[SecretKeyManagementPassword]),
				Grants: []querySpec{
					// TODO keep only necessary grants
					{Query: "GRANT ALL ON *.*"},
				},
			},
		},
		Profiles: map[string]Profile{
			DefaultProfileName: {},
		},
		Quotas: map[string]Quota{
			"default": {},
		},
	}

	// TODO pass from CR

	yamlConfig, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("error marshalling config to yaml: %w", err)
	}

	return string(yamlConfig), nil
}

func buildVolumes(ctx *reconcileContext, id replicaID) ([]corev1.Volume, []corev1.VolumeMount) {
	volumeMounts := []corev1.VolumeMount{
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
			MountPath: "/var/log/clickhouse-server",
			SubPath:   "var-log-clickhouse",
		},
	}

	defaultConfigMapMode := corev1.ConfigMapVolumeSourceDefaultMode
	volumes := []corev1.Volume{
		{
			Name: ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &defaultConfigMapMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ctx.Cluster.ConfigMapNameByReplicaID(id.shardID, id.index),
					},
				},
			},
		},
	}

	if ctx.Cluster.Spec.Settings.TLS.Enabled {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      TLSVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})

		volumes = append(volumes, corev1.Volume{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  ctx.Cluster.Spec.Settings.TLS.ServerCertSecret.Name,
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

	return volumes, volumeMounts
}
