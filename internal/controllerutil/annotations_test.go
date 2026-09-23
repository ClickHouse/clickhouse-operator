package controllerutil

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var _ = DescribeTable("PauseRequested",
	func(annotations map[string]string, expected bool) {
		log := NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
		obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}

		Expect(PauseRequested(obj, log)).To(Equal(expected))
	},
	Entry("no annotations", nil, false),
	Entry("annotation set to true", map[string]string{AnnotationPauseReconciliation: "true"}, true),
	Entry("unsupported value is ignored", map[string]string{AnnotationPauseReconciliation: "True"}, false),
	Entry("empty value is ignored", map[string]string{AnnotationPauseReconciliation: ""}, false),
)
