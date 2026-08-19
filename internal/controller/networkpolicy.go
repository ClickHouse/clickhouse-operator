package controller

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// KeeperClusterReferenceField indexes ClickHouseClusters by their referenced KeeperCluster "namespace/name".
// Registered by the ClickHouse controller setup; the Keeper controller requires it on the same manager.
const KeeperClusterReferenceField = "clickhouse.com/keeperClusterReference"

// RolePeer matches pods carrying the given clickhouse.com/role label in any namespace.
func RolePeer(role string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{},
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{util.LabelRoleKey: role},
		},
	}
}

// TCPPorts builds NetworkPolicy port entries for the given TCP ports.
func TCPPorts(ports ...int32) []networkingv1.NetworkPolicyPort {
	tcp := corev1.ProtocolTCP

	policyPorts := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		p := intstr.FromInt32(port)
		policyPorts = append(policyPorts, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &p})
	}

	return policyPorts
}
