package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	RequeueOnRefreshTimeout       = time.Second
	TLSFileMode             int32 = 0444
)

var (
	// DefaultLivenessProbeSettings defines default settings for Kubernetes liveness probes.
	//nolint: mnd // Magic numbers are used as constants.
	DefaultLivenessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 60,
		TimeoutSeconds:      10,
		PeriodSeconds:       5,
		FailureThreshold:    10,
	}

	// DefaultReadinessProbeSettings defines default settings for Kubernetes liveness probes.
	//nolint: mnd // Magic numbers are used as constants.
	DefaultReadinessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      10,
		PeriodSeconds:       1,
		SuccessThreshold:    5,
		FailureThreshold:    10,
	}
)
