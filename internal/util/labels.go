package util

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
)

const (
	LabelKeeperValue       = "clickhouse-keeper"
	LabelKeeperAllReplicas = "all-replicas"

	LabelClickHouseValue = "clickhouse-server"
)

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
