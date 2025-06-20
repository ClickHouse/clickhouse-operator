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

	"github.com/clickhouse-operator/internal/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// ClickHouseClusterSpec defines the desired state of ClickHouseCluster.
type ClickHouseClusterSpec struct {
	// Number of replicas in the cluster
	// This is a pointer to distinguish between explicit zero and unspecified.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=0;1;3;5;7;9;11;13;15
	Replicas *int32 `json:"replicas"`

	// Reference to the KeeperCluster that is used for ClickHouse coordination.
	KeeperClusterRef *corev1.LocalObjectReference `json:"keeperClusterRef"`

	// Parameters passed to the Keeper pod spec.
	// +optional
	PodTemplate PodTemplateSpec `json:"podTemplate,omitempty"`

	// Parameters passed to the ClickHouse container spec.
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

	// Configuration parameters for ClickHouse server.
	// +optional
	Settings ClickHouseConfig `json:"settings,omitempty"`
}

func (s *ClickHouseClusterSpec) WithDefaults() {
	defaultSpec := ClickHouseClusterSpec{
		Replicas: ptr.To[int32](3),
		ContainerTemplate: ContainerTemplateSpec{
			Image: ContainerImage{
				Repository: DefaultClickHouseContainerRepository,
				Tag:        DefaultClickHouseContainerTag,
			},
			ImagePullPolicy: DefaultClickHouseContainerPolicy,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultClickHouseCPURequest),
					corev1.ResourceMemory: resource.MustParse(DefaultClickHouseMemoryRequest),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultClickHouseCPULimit),
					corev1.ResourceMemory: resource.MustParse(DefaultClickHouseMemoryLimit),
				},
			},
		},
	}

	if err := util.ApplyDefault(s, defaultSpec); err != nil {
		panic(fmt.Sprintf("unable to apply defaults: %v", err))
	}
}

type ClickHouseConfig struct {
	// Optionally you can lower the logger level or disable logging to file at all.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

	// TLS settings, allows to enable TLS settings for ClickHouse.
	// +optional
	TLS ClusterTLSSpec `json:"tls,omitempty"`

	// Additional ClickHouse configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig runtime.RawExtension `json:"extraConfig,omitempty"`
}

// ClickHouseClusterStatus defines the observed state of ClickHouseCluster.
type ClickHouseClusterStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ReadyReplicas Total number of replicas ready to server requests.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas"`
	// ConfigurationRevision indicates target configuration revision for every replica.
	ConfigurationRevision string `json:"configurationRevision,omitempty"`
	// StatefulSetRevision indicates target StatefulSet revision for every replica.
	StatefulSetRevision string `json:"statefulSetRevision,omitempty"`

	// CurrentRevision indicates latest applied ClickHouseCluster spec revision.
	CurrentRevision string `json:"currentRevision,omitempty"`
	// UpdateRevision indicates latest requested ClickHouseCluster spec revision.
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates lastest generation observed by controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClickHouseCluster is the Schema for the clickhouseclusters API.
type ClickHouseCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseClusterSpec   `json:"spec,omitempty"`
	Status ClickHouseClusterStatus `json:"status,omitempty"`
}

func (v *ClickHouseCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: v.Namespace,
		Name:      v.Name,
	}
}

func (v *ClickHouseCluster) SpecificName() string {
	return fmt.Sprintf("%s-clickhouse", v.GetName())
}

// +kubebuilder:object:root=true

// ClickHouseClusterList contains a list of ClickHouseCluster.
type ClickHouseClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClickHouseCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClickHouseCluster{}, &ClickHouseClusterList{})
}
