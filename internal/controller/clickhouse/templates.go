package clickhouse

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TemplateHeadlessService(cr *v1.ClickHouseCluster) *corev1.Service {
	protocols := buildProtocols(cr)
	ports := make([]corev1.ServicePort, 0, len(protocols))
	for name, protocol := range protocols {
		if protocol.Port == 0 {
			continue
		}

		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       name,
			Port:       int32(protocol.Port),
			TargetPort: intstr.FromInt32(int32(protocol.Port)),
		})
	}

	slices.SortFunc(ports, func(a, b corev1.ServicePort) int {
		if a.Name < b.Name {
			return -1
		} else if a.Name > b.Name {
			return 1
		}
		return 0
	})

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
	config, err := generateConfigForSingleReplica(ctx, v1.ReplicaID{})
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
	sts := TemplateStatefulSet(ctx, v1.ReplicaID{})
	hash, err := util.DeepHashObject(sts)
	if err != nil {
		return "", fmt.Errorf("hash template StatefulSet: %w", err)
	}

	return hash, nil
}

func TemplateConfigMap(ctx *reconcileContext, id v1.ReplicaID) (*corev1.ConfigMap, error) {
	configData, err := generateConfigForSingleReplica(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("generate config for replica %v: %w", id, err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctx.Cluster.ConfigMapNameByReplicaID(id),
			Namespace: ctx.Cluster.Namespace,
			Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, labelsFromID(id), map[string]string{
				util.LabelAppKey: ctx.Cluster.SpecificName(),
			}),
			Annotations: ctx.Cluster.Spec.Annotations,
		},
		Data: configData,
	}, nil
}

func TemplateStatefulSet(ctx *reconcileContext, id v1.ReplicaID) *appsv1.StatefulSet {
	volumes, volumeMounts := buildVolumes(ctx, id)
	protocols := buildProtocols(ctx.Cluster)
	var readyCheck string
	if protocol, ok := protocols["http"]; ok && protocol.Port > 0 {
		readyCheck = fmt.Sprintf("wget -qO- http://127.0.0.1:%d | grep -o Ok.", PortHTTP)
	} else {
		readyCheck = fmt.Sprintf("wget --ca-certificate=%s -qO- https://%s:%d | grep -o Ok.",
			path.Join(TLSConfigPath, CABundleFilename), ctx.Cluster.HostnameById(id), PortHTTPSecure)
	}

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
		// TODO do not restart if liveness probe fails?
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/usr/bin/clickhouse", "client",
						"--port", strconv.Itoa(PortManagement),
						// "--user", OperatorManagementUsername, // TODO pass password or generate user for it
						"-q", "SELECT 'liveness'",
					},
				},
			},
			TimeoutSeconds:   10,
			PeriodSeconds:    1,
			SuccessThreshold: 1,
			FailureThreshold: 15,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/bash", "-c", readyCheck},
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

	container.Ports = make([]corev1.ContainerPort, 0, len(protocols))
	for name, protocol := range protocols {
		if protocol.Port == 0 {
			continue
		}
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          name,
			ContainerPort: int32(protocol.Port),
		})
	}
	slices.SortFunc(container.Ports, func(a, b corev1.ContainerPort) int {
		if a.Name < b.Name {
			return -1
		} else if a.Name > b.Name {
			return 1
		}
		return 0
	})

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
			MatchLabels: util.MergeMaps(labelsFromID(id), map[string]string{
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
				Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, labelsFromID(id), map[string]string{
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
			Name:      ctx.Cluster.StatefulSetNameByReplicaID(id),
			Namespace: ctx.Cluster.Namespace,
			Labels: util.MergeMaps(ctx.Cluster.Spec.Labels, labelsFromID(id), map[string]string{
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

func labelsFromID(id v1.ReplicaID) map[string]string {
	return map[string]string{
		util.LabelClickHouseShardID:   strconv.Itoa(int(id.ShardID)),
		util.LabelClickHouseReplicaID: strconv.Itoa(int(id.Index)),
	}
}

func generateConfigForSingleReplica(ctx *reconcileContext, id v1.ReplicaID) (map[string]string, error) {
	configFiles := map[string]string{}
	for _, generator := range generators {
		if !generator.Exists(ctx) {
			continue
		}

		data, err := generator.Generate(ctx, id)
		if err != nil {
			return nil, err
		}

		_, filename := path.Split(generator.Filename())
		configFiles[filename] = data
	}

	return configFiles, nil
}

type Protocol struct {
	Type        string `yaml:"type"`
	Port        uint16 `yaml:"port,omitempty"`
	Impl        string `yaml:"impl,omitempty"`
	Description string `yaml:"description,omitempty"`
}

func buildProtocols(cr *v1.ClickHouseCluster) map[string]Protocol {
	protocols := map[string]Protocol{
		"interserver": {
			Type:        "interserver",
			Port:        PortInterserver,
			Description: "interserver",
		},
		"prometheus": {
			Type:        "prometheus",
			Port:        PortPrometheusScrape,
			Description: "prometheus",
		},
		"management": {
			Type:        "tcp",
			Port:        PortManagement,
			Description: "tcp-management",
		},
		"tcp": {
			Type: "tcp",
		},
		"http": {
			Type: "http",
		},
	}

	if !cr.Spec.Settings.TLS.Enabled || !cr.Spec.Settings.TLS.Required {
		protocols["http"] = Protocol{
			Type:        "http",
			Port:        PortHTTP,
			Description: "http",
		}
		protocols["tcp"] = Protocol{
			Type:        "tcp",
			Port:        PortNative,
			Description: "native protocol",
		}
	}

	if cr.Spec.Settings.TLS.Enabled {
		protocols["tcp-secure"] = Protocol{
			Type:        "tls",
			Port:        PortNativeSecure,
			Impl:        "tcp",
			Description: "secure native protocol",
		}
		protocols["http-secure"] = Protocol{
			Type:        "tls",
			Port:        PortHTTPSecure,
			Impl:        "http",
			Description: "https",
		}
	}

	return protocols
}

func buildVolumes(ctx *reconcileContext, id v1.ReplicaID) ([]corev1.Volume, []corev1.VolumeMount) {
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

	configItems := make([]corev1.KeyToPath, 0, len(generators))
	for _, generator := range generators {
		if !generator.Exists(ctx) {
			continue
		}

		filePath := generator.Filename()
		_, name := path.Split(filePath)
		configItems = append(configItems, corev1.KeyToPath{
			Key:  name,
			Path: filePath,
		})
	}

	defaultConfigMapMode := corev1.ConfigMapVolumeSourceDefaultMode
	volumes := []corev1.Volume{
		{
			Name: ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &defaultConfigMapMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ctx.Cluster.ConfigMapNameByReplicaID(id),
					},
					Items: configItems,
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

	if ctx.Cluster.Spec.Settings.TLS.CABundle != nil {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      CustomCAVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})

		volumes = append(volumes, corev1.Volume{
			Name: CustomCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  ctx.Cluster.Spec.Settings.TLS.CABundle.Name,
					DefaultMode: &TLSFileMode,
					Items: []corev1.KeyToPath{
						{Key: ctx.Cluster.Spec.Settings.TLS.CABundle.Key, Path: CustomCAFilename},
					},
				},
			},
		})
	}

	return volumes, volumeMounts
}
