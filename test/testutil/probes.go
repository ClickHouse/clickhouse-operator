package testutil

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunConnectivityProbe starts a pod that checks TCP connectivity to the target once.
func (e *Env) RunConnectivityProbe(
	ctx context.Context, ns, name, target string, port int, podLabels map[string]string,
) *corev1.Pod {
	GinkgoHelper()

	pod := &corev1.Pod{
		Namespace: ns, Name: name, Labels: podLabels,
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", fmt.Sprintf("nc -z -w3 %s %d", target, port)},
			}},
		},
	}

	Expect(e.Client.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func(ctx context.Context) { _ = e.Client.Delete(ctx, pod) })

	return pod
}

// ProbePhase polls the probe pod phase for Eventually assertions.
func (e *Env) ProbePhase(ctx context.Context, pod *corev1.Pod) func(Gomega) corev1.PodPhase {
	return func(g Gomega) corev1.PodPhase {
		var p corev1.Pod
		g.Expect(e.Client.Get(ctx, client.ObjectKeyFromObject(pod), &p)).To(Succeed())

		return p.Status.Phase
	}
}
