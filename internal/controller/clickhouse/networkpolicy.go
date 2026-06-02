package clickhouse

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

func templateNetworkPolicy(cr *v1.ClickHouseCluster) *networkingv1.NetworkPolicy {
	app := cr.SpecificName()

	tcp := func(port int32) networkingv1.NetworkPolicyPort {
		proto := corev1.ProtocolTCP
		p := intstr.FromInt32(port)

		return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
	}

	self := networkingv1.NetworkPolicyPeer{
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{controllerutil.LabelAppKey: app},
		},
	}

	clusterNamespace := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": cr.Namespace},
		},
	}

	ingress := []networkingv1.NetworkPolicyIngressRule{
		{
			From:  []networkingv1.NetworkPolicyPeer{self},
			Ports: []networkingv1.NetworkPolicyPort{tcp(PortInterserver), tcp(PortNative)},
		},
		{
			From:  append([]networkingv1.NetworkPolicyPeer{clusterNamespace}, cr.Spec.NetworkPolicy.AllowedClients...),
			Ports: []networkingv1.NetworkPolicyPort{tcp(PortNative), tcp(PortHTTP)},
		},
	}

	if peers := cr.Spec.NetworkPolicy.MonitoringPeers; len(peers) > 0 {
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From:  peers,
			Ports: []networkingv1.NetworkPolicyPort{tcp(PortPrometheusScrape)},
		})
	}

	return &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{
			Kind:       "NetworkPolicy",
			APIVersion: "networking.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      app,
			Namespace: cr.Namespace,
			Labels: controllerutil.MergeMaps(cr.Spec.Labels, map[string]string{
				controllerutil.LabelAppKey: app,
			}),
			Annotations: controllerutil.MergeMaps(cr.Spec.Annotations),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{controllerutil.LabelAppKey: app},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     ingress,
		},
	}
}
