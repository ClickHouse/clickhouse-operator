package testutil

import (
	"strings"
	"testing"
)

func TestPodForHostname(t *testing.T) {
	for _, test := range []struct {
		name      string
		hostname  string
		namespace string
		podName   string
	}{
		{
			name:      "pod service hostname",
			hostname:  "test-clickhouse-0-0-0.test-clickhouse-headless.default.svc.cluster.local",
			namespace: "default",
			podName:   "test-clickhouse-0-0-0",
		},
		{
			name:      "internal service hostname",
			hostname:  "test-clickhouse-internal-0-0.default.svc.cluster.local",
			namespace: "default",
			podName:   "test-clickhouse-0-0-0",
		},
		{
			name:      "internal service hostname with internal in cluster name",
			hostname:  "test-internal-clickhouse-internal-0-0.default.svc.cluster.local",
			namespace: "default",
			podName:   "test-internal-clickhouse-0-0-0",
		},
		{
			name:      "pod hostname in svc namespace",
			hostname:  "test-clickhouse-0-0-0.test-clickhouse-headless.svc.svc.cluster.local",
			namespace: "svc",
			podName:   "test-clickhouse-0-0-0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			namespace, podName, err := podForHostname(test.hostname)
			if err != nil {
				t.Fatal(err)
			}

			if namespace != test.namespace || podName != test.podName {
				t.Fatalf("podForHostname() = %s/%s, want %s/%s", namespace, podName, test.namespace, test.podName)
			}
		})
	}
}

func TestPodForHostnameRejectsPotentiallyTruncatedService(t *testing.T) {
	suffix := "-abcd-internal-0-0"
	serviceName := strings.Repeat("a", maxKubernetesResourceNameLength-len(suffix)) + suffix

	_, _, err := podForHostname(serviceName + ".default.svc.cluster.local")
	if err == nil {
		t.Fatal("podForHostname() succeeded, want error")
	}
}

func TestPodForHostnameAcceptsMaxLengthUntruncatedService(t *testing.T) {
	suffix := "-clickhouse-internal-0-0"
	serviceName := strings.Repeat("a", maxKubernetesResourceNameLength-len(suffix)) + suffix

	namespace, podName, err := podForHostname(serviceName + ".default.svc.cluster.local")
	if err != nil {
		t.Fatal(err)
	}

	if namespace != "default" || podName != strings.TrimSuffix(serviceName, "-internal-0-0")+"-0-0-0" {
		t.Fatalf("podForHostname() = %s/%s", namespace, podName)
	}
}
