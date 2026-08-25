package testutil

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// ClickHouseBuilder assembles a ClickHouseCluster CR for tests.
// Defaults: 1 replica, image tag BaseVersion.
type ClickHouseBuilder struct {
	cr v1.ClickHouseCluster
}

// NewClickHouseCluster returns a builder for a minimal valid cluster.
func NewClickHouseCluster(namespace, name string) *ClickHouseBuilder {
	return &ClickHouseBuilder{cr: v1.ClickHouseCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1.ClickHouseClusterSpec{
			Replicas: new(int32(1)),
			ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: BaseVersion},
			},
		},
	}}
}

// WithReplicas sets the replica count.
func (b *ClickHouseBuilder) WithReplicas(replicas int32) *ClickHouseBuilder {
	b.cr.Spec.Replicas = &replicas
	return b
}

// WithShards sets the shard count.
func (b *ClickHouseBuilder) WithShards(shards int32) *ClickHouseBuilder {
	b.cr.Spec.Shards = &shards
	return b
}

// WithTag sets the image tag.
func (b *ClickHouseBuilder) WithTag(tag string) *ClickHouseBuilder {
	b.cr.Spec.ContainerTemplate.Image.Tag = tag
	return b
}

// WithStorage sets the data volume claim spec.
func (b *ClickHouseBuilder) WithStorage(storage corev1.PersistentVolumeClaimSpec) *ClickHouseBuilder {
	b.cr.Spec.DataVolumeClaimSpec = &storage
	return b
}

// WithKeeper sets the KeeperCluster reference.
func (b *ClickHouseBuilder) WithKeeper(name string) *ClickHouseBuilder {
	b.cr.Spec.KeeperClusterRef = v1.KeeperClusterReference{Name: name}
	return b
}

// WithNetworkPolicy sets the managed NetworkPolicy mode.
func (b *ClickHouseBuilder) WithNetworkPolicy(policy v1.NetworkPolicyPolicy) *ClickHouseBuilder {
	b.cr.Spec.NetworkPolicy = &v1.ClickHouseNetworkPolicySpec{Policy: policy}
	return b
}

// WithPodTemplate sets the pod template.
func (b *ClickHouseBuilder) WithPodTemplate(template v1.PodTemplateSpec) *ClickHouseBuilder {
	b.cr.Spec.PodTemplate = template
	return b
}

// WithContainerTemplate replaces the container template wholesale, keeping the image tag.
func (b *ClickHouseBuilder) WithContainerTemplate(template v1.ContainerTemplateSpec) *ClickHouseBuilder {
	if template.Image.Tag == "" {
		template.Image = b.cr.Spec.ContainerTemplate.Image
	}

	b.cr.Spec.ContainerTemplate = template

	return b
}

// Cluster returns the assembled CR.
func (b *ClickHouseBuilder) Cluster() v1.ClickHouseCluster {
	return b.cr
}

// KeeperBuilder assembles a KeeperCluster CR for tests.
// Defaults: 1 replica, image tag BaseVersion.
type KeeperBuilder struct {
	cr v1.KeeperCluster
}

// NewKeeperCluster returns a builder for a minimal valid keeper.
func NewKeeperCluster(namespace, name string) *KeeperBuilder {
	return &KeeperBuilder{cr: v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1.KeeperClusterSpec{
			Replicas: new(int32(1)),
			ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: BaseVersion},
			},
		},
	}}
}

// WithReplicas sets the replica count.
func (b *KeeperBuilder) WithReplicas(replicas int32) *KeeperBuilder {
	b.cr.Spec.Replicas = &replicas
	return b
}

// WithTag sets the image tag.
func (b *KeeperBuilder) WithTag(tag string) *KeeperBuilder {
	b.cr.Spec.ContainerTemplate.Image.Tag = tag
	return b
}

// WithStorage sets the data volume claim spec.
func (b *KeeperBuilder) WithStorage(storage corev1.PersistentVolumeClaimSpec) *KeeperBuilder {
	b.cr.Spec.DataVolumeClaimSpec = &storage
	return b
}

// WithSettings sets the keeper settings.
func (b *KeeperBuilder) WithSettings(settings v1.KeeperSettings) *KeeperBuilder {
	b.cr.Spec.Settings = settings
	return b
}

// WithNetworkPolicy sets the managed NetworkPolicy mode.
func (b *KeeperBuilder) WithNetworkPolicy(policy v1.NetworkPolicyPolicy) *KeeperBuilder {
	b.cr.Spec.NetworkPolicy = &v1.KeeperNetworkPolicySpec{Policy: policy}
	return b
}

// WithPodTemplate sets the pod template.
func (b *KeeperBuilder) WithPodTemplate(template v1.PodTemplateSpec) *KeeperBuilder {
	b.cr.Spec.PodTemplate = template
	return b
}

// WithContainerTemplate replaces the container template wholesale, keeping the image tag.
func (b *KeeperBuilder) WithContainerTemplate(template v1.ContainerTemplateSpec) *KeeperBuilder {
	if template.Image.Tag == "" {
		template.Image = b.cr.Spec.ContainerTemplate.Image
	}

	b.cr.Spec.ContainerTemplate = template

	return b
}

// Cluster returns the assembled CR.
func (b *KeeperBuilder) Cluster() v1.KeeperCluster {
	return b.cr
}
