package controllerutil

import (
	"fmt"
	"sync/atomic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

const openShiftGroupVersion = "security.openshift.io/v1"

var isOpenShift atomic.Bool

// DetectOpenShift probes the API server for the security.openshift.io API
// group, which is registered on OpenShift but not on vanilla Kubernetes.
// A missing group (vanilla k8s) is the expected non-OpenShift case and is not
// returned as an error — only genuine discovery/transport failures are.
func DetectOpenShift(cfg *rest.Config) error {
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build discovery client: %w", err)
	}

	_, err = disc.ServerResourcesForGroupVersion(openShiftGroupVersion)
	switch {
	case err == nil:
		isOpenShift.Store(true)
		return nil
	case apierrors.IsNotFound(err), discovery.IsGroupDiscoveryFailedError(err):
		// Group not served — vanilla Kubernetes. Expected, not an error.
		return nil
	default:
		return fmt.Errorf("OpenShift probe %s: %w", openShiftGroupVersion, err)
	}
}

// IsOpenShift returns the result of OpenShift probe.
func IsOpenShift() bool {
	return isOpenShift.Load()
}

// SetOpenShiftForTest sets the OpenShift detection result.
// For testing usage only.
func SetOpenShiftForTest(state bool) {
	isOpenShift.Store(state)
}
