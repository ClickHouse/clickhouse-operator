/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/clickhouse-operator/internal/util"
)

// KeeperClusterSpec defines the desired state of KeeperCluster.
type KeeperClusterSpec struct {
	// Number of replicas in the cluster
	// This is a pointer to distinguish between explicit zero and unspecified.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=0;1;3;5;7;9;11;13;15
	Replicas *int32 `json:"replicas"`

	// Parameters passed to the Keeper pod spec.
	// +optional
	PodTemplate PodTemplateSpec `json:"podTemplate,omitempty"`

	// Parameters passed to the Keeper container spec.
	// +optional
	ContainerTemplate ContainerTemplateSpec `json:"containerTemplate,omitempty"`

	// Settings for the replicas storage.
	// +required
	DataVolumeClaimSpec corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`

	// Additional labels that are added to resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations that are added to resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Configuration parameters for ClickHouse Keeper server.
	// +optional
	Settings KeeperConfig `json:"settings,omitempty"`
}

func (s *KeeperClusterSpec) WithDefaults() {
	defaultSpec := KeeperClusterSpec{
		Replicas: ptr.To[int32](3),
		ContainerTemplate: ContainerTemplateSpec{
			Image: ContainerImage{
				Repository: DefaultKeeperContainerRepository,
				Tag:        DefaultKeeperContainerTag,
			},
			ImagePullPolicy: DefaultKeeperContainerPolicy,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultKeeperCPURequest),
					corev1.ResourceMemory: resource.MustParse(DefaultKeeperMemoryRequest),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultKeeperCPULimit),
					corev1.ResourceMemory: resource.MustParse(DefaultKeeperMemoryLimit),
				},
			},
		},

		DataVolumeClaimSpec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	if err := util.ApplyDefault(s, defaultSpec); err != nil {
		panic(fmt.Sprintf("unable to apply defaults: %v", err))
	}
}

type KeeperConfig struct {
	// Optionally you can lower the logger level or disable logging to file at all.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

	// Additional ClickHouse Keeper configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig runtime.RawExtension `json:"extraConfig,omitempty"`
}

// KeeperClusterStatus defines the observed state of KeeperCluster.
type KeeperClusterStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Replicas that should be present in config
	// TODO probably can be tracked by rereading quorum config.
	// +optional
	Replicas []string `json:"replicas"`
	// ReadyReplicas Total number of replicas ready to server requests.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas"`
	// ConfigurationRevision indicates target configuration revision for every replica.
	ConfigurationRevision string `json:"configurationRevision,omitempty"`
	// StatefulSetRevision indicates target StatefulSet revision for every replica.
	StatefulSetRevision string `json:"StatefulSetRevision,omitempty"`

	// CurrentRevision indicates latest applied KeeperCluster spec revision.
	CurrentRevision string `json:"currentRevision,omitempty"`
	// CurrentRevision indicates latest requested KeeperCluster spec revision.
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates lastest generation observed by controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// KeeperCluster is the Schema for the keeperclusters API
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="ReadyReplicas",type="number",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="number",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type KeeperCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeeperClusterSpec   `json:"spec,omitempty"`
	Status KeeperClusterStatus `json:"status,omitempty"`
}

func (v *KeeperCluster) GetNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: v.Namespace,
		Name:      v.Name,
	}
}

func (v *KeeperCluster) SpecificName() string {
	return fmt.Sprintf("%s-keeper", v.GetName())
}

func (v *KeeperCluster) Replicas() int32 {
	if v.Spec.Replicas == nil {
		// In case of absence, value must be populated by default, if it's nil then some wrong logic in controller erased it.
		panic(".spec.replicas is nil, this is a bug")
	}

	return *v.Spec.Replicas
}

const (
	KeeperConfigMapNameSuffix          = "configmap"
	latestKeeperConfigMapVersion       = 1
	latestKeeperQuorumConfigMapVersion = 1
)

func (v *KeeperCluster) HeadlessServiceName() string {
	return fmt.Sprintf("%s-headless", v.SpecificName())
}

func (v *KeeperCluster) QuorumConfigMapName() string {
	return fmt.Sprintf("%s-quorum-%s-%d", v.SpecificName(), KeeperConfigMapNameSuffix, latestKeeperQuorumConfigMapVersion)
}

func (v *KeeperCluster) ConfigMapNameByReplicaID(replicaID string) string {
	return fmt.Sprintf("%s-%s-%s-v%d", v.SpecificName(), replicaID, KeeperConfigMapNameSuffix, latestKeeperConfigMapVersion)
}

func (v *KeeperCluster) StatefulSetNameByReplicaID(replicaID string) string {
	return fmt.Sprintf("%s-%s", v.SpecificName(), replicaID)
}

func (v *KeeperCluster) HostnameById(replicaID string) string {
	hostnameTemplate := "%s-0.%s.%s.svc.cluster.local"
	return fmt.Sprintf(hostnameTemplate, v.StatefulSetNameByReplicaID(replicaID), v.HeadlessServiceName(), v.Namespace)
}

func (v *KeeperCluster) Hostnames() []string {
	hostnames := make([]string, 0, len(v.Status.Replicas))
	for _, id := range v.Status.Replicas {
		hostnames = append(hostnames, v.HostnameById(id))
	}

	return hostnames
}

func (v *KeeperCluster) HostnamesByID() map[string]string {
	hostnameByID := map[string]string{}
	for _, id := range v.Status.Replicas {
		hostnameByID[id] = v.HostnameById(id)
	}

	return hostnameByID
}

// +kubebuilder:object:root=true

// KeeperClusterList contains a list of KeeperCluster.
type KeeperClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeeperCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KeeperCluster{}, &KeeperClusterList{})
}
