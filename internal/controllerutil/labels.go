package controllerutil

import (
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Contains common labels keys and helpers to work with.
const (
	LabelAppKey         = "app"
	LabelAppK8sKey      = "app.kubernetes.io/name"
	LabelInstanceK8sKey = "app.kubernetes.io/instance"

	LabelRoleKey             = "clickhouse.com/role"
	LabelKeeperReplicaID     = "clickhouse.com/keeper-replica-id"
	LabelClickHouseShardID   = "clickhouse.com/shard-id"
	LabelClickHouseReplicaID = "clickhouse.com/replica-id"
	LabelDiskName            = "clickhouse.com/disk"
)

const (
	LabelKeeperValue       = "clickhouse-keeper"
	LabelKeeperAllReplicas = "all-replicas"
	LabelClickHouseValue   = "clickhouse-server"
	LabelVersionProbe      = "version-probe"
)

// DiskLabel returns the label set identifying a disk's volumeClaimTemplate/PVC by name.
func DiskLabel(name string) map[string]string {
	return map[string]string{LabelDiskName: name}
}

// AppRequirements returns ListOptions to list resources of the given app.
func AppRequirements(namespace, app string) *client.ListOptions {
	appReq, err := labels.NewRequirement(LabelAppKey, selection.Equals, []string{app})
	if err != nil {
		panic(fmt.Sprintf("make %q requirement to list: %s", LabelAppKey, err))
	}

	return &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: labels.NewSelector().Add(*appReq),
	}
}
