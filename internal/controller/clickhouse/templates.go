package clickhouse

import (
	"fmt"
	"maps"
	"path"
	"strconv"

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

func templateHeadlessService(cr *v1.ClickHouseCluster) *corev1.Service {
	protocols := buildProtocols(cr)

	ports := make([]corev1.ServicePort, 0, len(protocols))
	for name, proto := range protocols {
		if proto.Port == 0 {
			continue
		}

		ports = append(ports, corev1.ServicePort{
			Protocol:   corev1.ProtocolTCP,
			Name:       name,
			Port:       proto.Port,
			TargetPort: intstr.FromInt32(proto.Port),
		})
	}

	controllerutil.SortKey(ports, func(port corev1.ServicePort) string {
		return port.Name
	})

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

func templatePodDisruptionBudget(cr *v1.ClickHouseCluster, shardID int32) *policyv1.PodDisruptionBudget {
	pdb := &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.PodDisruptionBudgetNameByShard(shardID),
			Namespace: cr.Namespace,
			Labels: controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
				controllerutil.LabelAppKey:            cr.SpecificName(),
				controllerutil.LabelClickHouseShardID: strconv.Itoa(int(shardID)),
			}),
			Annotations: controllerutil.MergeMaps(cr.Spec.Annotations),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					controllerutil.LabelAppKey:            cr.SpecificName(),
					controllerutil.LabelClickHouseShardID: strconv.Itoa(int(shardID)),
				},
			},
		},
	}

	// Smart default: single-replica shards use maxUnavailable=1 to avoid
	// drain deadlocks; multi-replica shards use minAvailable=1 for HA.
	if cr.Replicas() <= 1 {
		pdb.Spec.MaxUnavailable = new(intstr.FromInt32(1))
	} else {
		pdb.Spec.MinAvailable = new(intstr.FromInt32(1))
	}

	cr.Spec.PodDisruptionBudget.ApplyOverrides(&pdb.Spec)

	return pdb
}

func templateClusterSecrets(cr *v1.ClickHouseCluster, secret *corev1.Secret) bool {
	secret.Name = cr.SecretName()
	secret.Namespace = cr.Namespace
	secret.Type = corev1.SecretTypeOpaque

	changed := false

	labels := controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
		controllerutil.LabelAppKey: cr.SpecificName(),
	})
	if !maps.Equal(labels, secret.Labels) {
		changed = true
		secret.Labels = labels
	}

	annotations := controllerutil.MergeMaps(cr.Spec.Annotations)
	if !maps.Equal(annotations, secret.Annotations) {
		changed = true
		secret.Annotations = annotations
	}

	if secret.Data == nil {
		changed = true
		secret.Data = map[string][]byte{}
	}

	for key, template := range secretsToGenerate {
		if _, ok := secret.Data[key]; !ok {
			changed = true
			secret.Data[key] = fmt.Appendf(nil, template, controllerutil.GeneratePassword())
		}
	}

	for key := range secret.Data {
		if _, ok := secretsToGenerate[key]; !ok {
			changed = true

			delete(secret.Data, key)
		}
	}

	return changed
}

func getConfigurationRevision(r *clickhouseReconciler) (string, error) {
	config, err := generateConfigForSingleReplica(r, v1.ClickHouseReplicaID{})
	if err != nil {
		return "", fmt.Errorf("generate template configuration: %w", err)
	}

	hash, err := controllerutil.DeepHashObject(config)
	if err != nil {
		return "", fmt.Errorf("hash template configuration: %w", err)
	}

	return hash, nil
}

func getStatefulSetRevision(r *clickhouseReconciler) (string, error) {
	sts, err := templateStatefulSet(r, v1.ClickHouseReplicaID{})
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

func templateConfigMap(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (*corev1.ConfigMap, error) {
	configData, err := generateConfigForSingleReplica(r, id)
	if err != nil {
		return nil, fmt.Errorf("generate config for replica %v: %w", id, err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Cluster.ConfigMapNameByReplicaID(id),
			Namespace: r.Cluster.Namespace,
			Labels: controllerutil.MergeMaps(r.Cluster.Spec.Labels, id.Labels(), map[string]string{
				controllerutil.LabelAppKey: r.Cluster.SpecificName(),
			}),
			Annotations: r.Cluster.Spec.Annotations,
		},
		Data: configData,
	}, nil
}

func templateStatefulSet(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (*appsv1.StatefulSet, error) {
	podSpec, err := templatePodSpec(r, id)
	if err != nil {
		return nil, fmt.Errorf("template pod spec: %w", err)
	}

	resourceLabels := controllerutil.MergeMaps(r.Cluster.Spec.Labels, id.Labels(), map[string]string{
		controllerutil.LabelAppKey:         r.Cluster.SpecificName(),
		controllerutil.LabelInstanceK8sKey: r.Cluster.SpecificName(),
		controllerutil.LabelRoleKey:        controllerutil.LabelClickHouseValue,
		controllerutil.LabelAppK8sKey:      controllerutil.LabelClickHouseValue,
	})

	spec := appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: controllerutil.MergeMaps(id.Labels(), map[string]string{
				controllerutil.LabelAppKey: r.Cluster.SpecificName(),
			}),
		},
		ServiceName:         r.Cluster.HeadlessServiceName(),
		PodManagementPolicy: appsv1.ParallelPodManagement,
		Replicas:            new(int32(1)),
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: r.Cluster.SpecificName(),
				Labels:       resourceLabels,
				Annotations: controllerutil.MergeMaps(r.Cluster.Spec.Annotations, map[string]string{
					"kubectl.kubernetes.io/default-container": ContainerName,
				}),
			},
			Spec: podSpec,
		},
		RevisionHistoryLimit: new(int32(DefaultRevisionHistory)),
	}

	if r.Cluster.Spec.DataVolumeClaimSpec != nil {
		spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:        internal.PersistentVolumeName,
				Labels:      resourceLabels,
				Annotations: r.Cluster.Spec.Annotations,
			},
			Spec: *r.Cluster.Spec.DataVolumeClaimSpec.DeepCopy(),
		}}
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Cluster.StatefulSetNameByReplicaID(id),
			Namespace: r.Cluster.Namespace,
			Labels:    resourceLabels,
			Annotations: controllerutil.MergeMaps(r.Cluster.Spec.Annotations, map[string]string{
				controllerutil.AnnotationStatefulSetVersion: breakingStatefulSetVersion.String(),
			}),
		},
		Spec: spec,
	}, nil
}

func generateConfigForSingleReplica(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (map[string]string, error) {
	configFiles := map[string]string{}
	for _, generator := range generators {
		if !generator.Exists(r) {
			continue
		}

		data, err := generator.Generate(r, id)
		if err != nil {
			return nil, fmt.Errorf("generate config file %s: %w", generator.Path(), err)
		}

		configFiles[generator.ConfigKey()] = data
	}

	return configFiles, nil
}

func templatePodSpec(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (corev1.PodSpec, error) {
	cr := r.Cluster

	container, err := templateContainer(r)
	if err != nil {
		return corev1.PodSpec{}, fmt.Errorf("template container: %w", err)
	}

	volumes := buildVolumes(r, id)
	controllerutil.SortKey(volumes, func(v corev1.Volume) string { return v.Name })

	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
		DNSPolicy:     corev1.DNSClusterFirst,
		Volumes:       volumes,
		Containers:    []corev1.Container{container},
		SecurityContext: &corev1.PodSecurityContext{
			FSGroup:    new(controller.DefaultUser),
			RunAsUser:  new(controller.DefaultUser),
			RunAsGroup: new(controller.DefaultUser),
		},
	}

	if cr.Spec.PodTemplate.TopologyZoneKey != nil && *cr.Spec.PodTemplate.TopologyZoneKey != "" {
		zoneKey := *cr.Spec.PodTemplate.TopologyZoneKey

		shardID := strconv.Itoa(int(id.ShardID))
		podSpec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					Weight: MaximalAffinityWeight,
					PodAffinityTerm: corev1.PodAffinityTerm{
						TopologyKey: zoneKey,
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								controllerutil.LabelAppKey:            cr.SpecificName(),
								controllerutil.LabelRoleKey:           controllerutil.LabelClickHouseValue,
								controllerutil.LabelClickHouseShardID: shardID,
							},
						},
					},
				}},
			},
			PodAffinity: &corev1.PodAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					PodAffinityTerm: corev1.PodAffinityTerm{
						TopologyKey: zoneKey,
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								controllerutil.LabelAppKey:  r.keeper.SpecificName(),
								controllerutil.LabelRoleKey: controllerutil.LabelKeeperValue,
							},
						},
					},
					Weight: 1,
				}},
			},
		}

		podSpec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       zoneKey,
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					controllerutil.LabelAppKey:            cr.SpecificName(),
					controllerutil.LabelRoleKey:           controllerutil.LabelClickHouseValue,
					controllerutil.LabelClickHouseShardID: shardID,
				},
			},
		}}
	}

	if cr.Spec.PodTemplate.NodeHostnameKey != nil && *cr.Spec.PodTemplate.NodeHostnameKey != "" {
		if podSpec.Affinity == nil {
			podSpec.Affinity = &corev1.Affinity{}
		}

		if podSpec.Affinity.PodAntiAffinity == nil {
			podSpec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}

		podSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
			podSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			corev1.PodAffinityTerm{
				TopologyKey: *cr.Spec.PodTemplate.NodeHostnameKey,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						controllerutil.LabelAppKey:  cr.SpecificName(),
						controllerutil.LabelRoleKey: controllerutil.LabelClickHouseValue,
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

func templateContainer(r *clickhouseReconciler) (corev1.Container, error) {
	cr := r.Cluster
	protocols := buildProtocols(cr)

	livenessProbe := controller.DefaultLivenessProbeSettings
	livenessProbe.ProbeHandler = corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{
			Port: intstr.FromInt32(PortNative),
		},
	}

	if cr.Spec.Settings.TLS.Enabled && cr.Spec.Settings.TLS.Required {
		livenessProbe.TCPSocket.Port.IntVal = PortNativeSecure
	}

	readinessProbe := controller.DefaultReadinessProbeSettings
	readinessProbe.ProbeHandler = corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path:   "/ping",
			Port:   intstr.FromInt32(PortInterserver),
			Scheme: corev1.URISchemeHTTP,
		},
	}

	container := corev1.Container{
		Name: ContainerName,
		Env: []corev1.EnvVar{
			{
				Name:  "CLICKHOUSE_CONFIG",
				Value: path.Join(ConfigPath, ConfigFileName),
			},
			{
				Name:  "CLICKHOUSE_SKIP_USER_SETUP",
				Value: "1",
			},
		},
		VolumeMounts:             buildMounts(r),
		LivenessProbe:            &livenessProbe,
		ReadinessProbe:           &readinessProbe,
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
			},
		},
	}

	for _, secret := range secretsToEnvMapping {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: secret.Env,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cr.SecretName(),
					},
					Key: secret.Key,
				},
			},
		})
	}

	container.Ports = make([]corev1.ContainerPort, 0, len(protocols))
	for name, proto := range protocols {
		if proto.Port == 0 {
			continue
		}

		container.Ports = append(container.Ports, corev1.ContainerPort{
			Protocol:      corev1.ProtocolTCP,
			Name:          name,
			ContainerPort: proto.Port,
		})
	}

	controllerutil.SortKey(container.Ports, func(port corev1.ContainerPort) string {
		return port.Name
	})

	if cr.Spec.Settings.DefaultUserPassword != nil {
		var (
			secretRef    *corev1.SecretKeySelector
			configMapRef *corev1.ConfigMapKeySelector
		)

		if cr.Spec.Settings.DefaultUserPassword.Secret != nil {
			secretRef = &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cr.Spec.Settings.DefaultUserPassword.Secret.Name,
				},
				Key: cr.Spec.Settings.DefaultUserPassword.Secret.Key,
			}
		}

		if cr.Spec.Settings.DefaultUserPassword.ConfigMap != nil {
			configMapRef = &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cr.Spec.Settings.DefaultUserPassword.ConfigMap.Name,
				},
				Key: cr.Spec.Settings.DefaultUserPassword.ConfigMap.Key,
			}
		}

		container.Env = append(container.Env, corev1.EnvVar{
			Name: EnvDefaultUserPassword,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef:    secretRef,
				ConfigMapKeyRef: configMapRef,
			},
		})
	}

	container, err := controller.ApplyContainerTemplateOverrides(&container, &cr.Spec.ContainerTemplate)
	if err != nil {
		return corev1.Container{}, fmt.Errorf("apply container template overrides: %w", err)
	}

	return container, nil
}

type protocol struct {
	Type        string `yaml:"type"`
	Port        int32  `yaml:"port,omitempty"`
	Impl        string `yaml:"impl,omitempty"`
	Description string `yaml:"description,omitempty"`
}

func buildProtocols(cr *v1.ClickHouseCluster) map[string]protocol {
	protocols := map[string]protocol{
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
		protocols["http"] = protocol{
			Type:        "http",
			Port:        PortHTTP,
			Description: "http",
		}
		protocols["tcp"] = protocol{
			Type:        "tcp",
			Port:        PortNative,
			Description: "native protocol",
		}
	}

	if cr.Spec.Settings.TLS.Enabled {
		protocols["tcp-secure"] = protocol{
			Type:        "tls",
			Port:        PortNativeSecure,
			Impl:        "tcp",
			Description: "secure native protocol",
		}
		protocols["http-secure"] = protocol{
			Type:        "tls",
			Port:        PortHTTPSecure,
			Impl:        "http",
			Description: "https",
		}
	}

	return protocols
}

func buildVolumes(r *clickhouseReconciler, id v1.ClickHouseReplicaID) []corev1.Volume {
	defaultConfigMapMode := corev1.ConfigMapVolumeSourceDefaultMode

	configVolumes := map[string]corev1.Volume{}
	for _, generator := range generators {
		if !generator.Exists(r) {
			continue
		}

		volume, ok := configVolumes[generator.Path()]
		if !ok {
			volume = corev1.Volume{
				Name: controllerutil.PathToName(generator.Path()),
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						DefaultMode: &defaultConfigMapMode,
						LocalObjectReference: corev1.LocalObjectReference{
							Name: r.Cluster.ConfigMapNameByReplicaID(id),
						},
					},
				},
			}
		}

		volume.ConfigMap.Items = append(volume.ConfigMap.Items, corev1.KeyToPath{
			Key:  generator.ConfigKey(),
			Path: generator.Filename(),
		})
		configVolumes[generator.Path()] = volume
	}

	var volumes []corev1.Volume
	for _, volume := range configVolumes {
		controllerutil.SortKey(volume.ConfigMap.Items, func(item corev1.KeyToPath) string {
			return item.Key
		})
		volumes = append(volumes, volume)
	}

	if r.Cluster.Spec.Settings.TLS.Enabled {
		volumes = append(volumes, corev1.Volume{
			Name: internal.TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  r.Cluster.Spec.Settings.TLS.ServerCertSecret.Name,
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

	if r.Cluster.Spec.Settings.TLS.CABundle != nil {
		volumes = append(volumes, corev1.Volume{
			Name: internal.CustomCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  r.Cluster.Spec.Settings.TLS.CABundle.Name,
					DefaultMode: new(controller.TLSFileMode),
					Items: []corev1.KeyToPath{
						{Key: r.Cluster.Spec.Settings.TLS.CABundle.Key, Path: CustomCAFilename},
					},
				},
			},
		})
	}

	return volumes
}

func buildMounts(r *clickhouseReconciler) []corev1.VolumeMount {
	var volumeMounts []corev1.VolumeMount

	if r.Cluster.Spec.DataVolumeClaimSpec != nil {
		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{
				Name:      internal.PersistentVolumeName,
				MountPath: internal.ClickHouseDataPath,
				SubPath:   "var-lib-clickhouse",
			},
			corev1.VolumeMount{
				Name:      internal.PersistentVolumeName,
				MountPath: "/var/log/clickhouse-server",
				SubPath:   "var-log-clickhouse",
			},
		)
	}

	seenPaths := map[string]struct{}{}
	for _, generator := range generators {
		if !generator.Exists(r) {
			continue
		}

		if _, ok := seenPaths[generator.Path()]; !ok {
			seenPaths[generator.Path()] = struct{}{}
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      controllerutil.PathToName(generator.Path()),
				MountPath: generator.Path(),
				ReadOnly:  true,
			})
		}
	}

	if r.Cluster.Spec.Settings.TLS.Enabled {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      internal.TLSVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})
	}

	if r.Cluster.Spec.Settings.TLS.CABundle != nil {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      internal.CustomCAVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		})
	}

	return volumeMounts
}
