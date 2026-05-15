package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("DefaultContainerSecurityContext", func() {
	It("should drop all capabilities", func() {
		sc := DefaultContainerSecurityContext()
		Expect(sc.Capabilities).NotTo(BeNil())
		Expect(sc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
		Expect(sc.Capabilities.Add).To(BeEmpty())
	})

	It("should disable privilege escalation", func() {
		sc := DefaultContainerSecurityContext()
		Expect(sc.AllowPrivilegeEscalation).NotTo(BeNil())
		Expect(*sc.AllowPrivilegeEscalation).To(BeFalse())
	})

	It("should require non-root execution", func() {
		sc := DefaultContainerSecurityContext()
		Expect(sc.RunAsNonRoot).NotTo(BeNil())
		Expect(*sc.RunAsNonRoot).To(BeTrue())
	})

	It("should set runtime-default seccomp profile", func() {
		sc := DefaultContainerSecurityContext()
		Expect(sc.SeccompProfile).NotTo(BeNil())
		Expect(sc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
	})
})

var _ = Describe("DefaultPodSecurityContext", func() {
	Context("on vanilla Kubernetes", func() {
		BeforeEach(func() { controllerutil.SetOpenShiftForTest(false) })
		AfterEach(func() { controllerutil.SetOpenShiftForTest(false) })

		It("should set FSGroup, RunAsUser and RunAsGroup to the ClickHouse user (101)", func() {
			sc := DefaultPodSecurityContext()
			Expect(sc.FSGroup).NotTo(BeNil())
			Expect(*sc.FSGroup).To(Equal(DefaultUser))
			Expect(sc.RunAsUser).NotTo(BeNil())
			Expect(*sc.RunAsUser).To(Equal(DefaultUser))
			Expect(sc.RunAsGroup).NotTo(BeNil())
			Expect(*sc.RunAsGroup).To(Equal(DefaultUser))
		})

		It("should require non-root execution", func() {
			sc := DefaultPodSecurityContext()
			Expect(sc.RunAsNonRoot).NotTo(BeNil())
			Expect(*sc.RunAsNonRoot).To(BeTrue())
		})

		It("should set runtime-default seccomp profile", func() {
			sc := DefaultPodSecurityContext()
			Expect(sc.SeccompProfile).NotTo(BeNil())
			Expect(sc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		})
	})

	Context("on OpenShift", func() {
		BeforeEach(func() { controllerutil.SetOpenShiftForTest(true) })
		AfterEach(func() { controllerutil.SetOpenShiftForTest(false) })

		It("should omit FSGroup, RunAsUser and RunAsGroup so restricted-v2 SCC can inject them", func() {
			sc := DefaultPodSecurityContext()
			Expect(sc.FSGroup).To(BeNil())
			Expect(sc.RunAsUser).To(BeNil())
			Expect(sc.RunAsGroup).To(BeNil())
		})

		It("should still require non-root and runtime-default seccomp", func() {
			sc := DefaultPodSecurityContext()
			Expect(sc.RunAsNonRoot).NotTo(BeNil())
			Expect(*sc.RunAsNonRoot).To(BeTrue())
			Expect(sc.SeccompProfile).NotTo(BeNil())
			Expect(sc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		})
	})
})
