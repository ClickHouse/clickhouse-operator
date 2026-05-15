package controller

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// DefaultPodSecurityContext returns the operator's default PodSecurityContext.
// It generates different defaults for OpenShift installations, so default settings conform restricted-v2.
func DefaultPodSecurityContext() *corev1.PodSecurityContext {
	psc := &corev1.PodSecurityContext{
		RunAsNonRoot:   new(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}

	if !controllerutil.IsOpenShift() {
		psc.FSGroup = new(DefaultUser)
		psc.RunAsUser = new(DefaultUser)
		psc.RunAsGroup = new(DefaultUser)
	}

	return psc
}

// DefaultContainerSecurityContext returns the operator's default container-level SecurityContext.
func DefaultContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             new(true),
		AllowPrivilegeEscalation: new(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}
