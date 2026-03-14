package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	RequeueOnRefreshTimeout       = time.Second
	LoadReplicaStateTimeout       = 10 * time.Second
	TLSFileMode             int32 = 0444
)

var (
	// DefaultLivenessProbeSettings defines default settings for Kubernetes liveness probes.
	// SuccessThreshold must be explicitly set to 1 (the K8s default) to prevent
	// reconcile drift — omitting it produces a zero value that differs from what
	// the API server returns, causing an infinite StatefulSet update loop.
	//nolint: mnd // Magic numbers are used as constants.
	DefaultLivenessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 60,
		TimeoutSeconds:      10,
		PeriodSeconds:       5,
		SuccessThreshold:    1,
		FailureThreshold:    10,
	}

	// DefaultReadinessProbeSettings defines default settings for Kubernetes readiness probes.
	// SuccessThreshold is intentionally set to 5 (not the K8s default of 1) to
	// require multiple consecutive successes before marking the pod ready.
	//nolint: mnd // Magic numbers are used as constants.
	DefaultReadinessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      10,
		PeriodSeconds:       1,
		SuccessThreshold:    5,
		FailureThreshold:    10,
	}
)
