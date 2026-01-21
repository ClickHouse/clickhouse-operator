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
	// DefaultProbeSettings defines default settings for Kubernetes liveness and readiness probes.
	//nolint: mnd // Magic numbers are used as constants.
	DefaultProbeSettings = corev1.Probe{
		TimeoutSeconds:   10,
		PeriodSeconds:    1,
		SuccessThreshold: 1,
		FailureThreshold: 15,
	}
)
