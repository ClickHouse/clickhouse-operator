package v1alpha2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("NetworkSpec", func() {
	It("should report the managed NetworkPolicy only when enabled", func() {
		var nilSpec *NetworkSpec
		Expect(nilSpec.PolicyEnabled()).To(BeFalse())
		Expect((&NetworkSpec{}).PolicyEnabled()).To(BeFalse())
		Expect((&NetworkSpec{Policy: NetworkPolicyDisabled}).PolicyEnabled()).To(BeFalse())
		Expect((&NetworkSpec{Policy: NetworkPolicyEnabled}).PolicyEnabled()).To(BeTrue())
	})
})

// tlsServerCert is package-level because DescribeTable Entry arguments are evaluated at tree construction.
var tlsServerCert = &corev1.LocalObjectReference{Name: "server-cert"}

var _ = Describe("TLSSpec", func() {
	DescribeTable("Enabled and Required",
		func(mode TLSMode, enabled, required bool) {
			spec := &TLSSpec{Mode: mode}
			Expect(spec.Enabled()).To(Equal(enabled))
			Expect(spec.Required()).To(Equal(required))
		},
		Entry("unset", TLSMode(""), false, false),
		Entry("Disabled", TLSModeDisabled, false, false),
		Entry("Enabled", TLSModeEnabled, true, false),
		Entry("Required", TLSModeRequired, true, true),
	)

	It("should treat a nil spec as disabled", func() {
		var spec *TLSSpec
		Expect(spec.Enabled()).To(BeFalse())
		Expect(spec.Required()).To(BeFalse())
		Expect(spec.Validate()).To(Succeed())
	})

	DescribeTable("Validate",
		func(spec TLSSpec, valid bool) {
			if valid {
				Expect(spec.Validate()).To(Succeed())
			} else {
				Expect(spec.Validate()).NotTo(Succeed())
			}
		},
		Entry("disabled without cert", TLSSpec{Mode: TLSModeDisabled}, true),
		Entry("enabled without cert", TLSSpec{Mode: TLSModeEnabled}, false),
		Entry("required without cert", TLSSpec{Mode: TLSModeRequired}, false),
		Entry("enabled with empty cert name", TLSSpec{Mode: TLSModeEnabled, ServerCertSecret: &corev1.LocalObjectReference{}}, false),
		Entry("enabled with cert", TLSSpec{Mode: TLSModeEnabled, ServerCertSecret: tlsServerCert}, true),
		Entry("required with cert", TLSSpec{Mode: TLSModeRequired, ServerCertSecret: tlsServerCert}, true),
		Entry("CA bundle from secret", TLSSpec{
			Mode: TLSModeEnabled, ServerCertSecret: tlsServerCert,
			CABundle: &CABundleSource{Secret: &CABundleKeySelector{Name: "ca"}},
		}, true),
		Entry("CA bundle from configMap", TLSSpec{
			Mode: TLSModeEnabled, ServerCertSecret: tlsServerCert,
			CABundle: &CABundleSource{ConfigMap: &CABundleKeySelector{Name: "ca"}},
		}, true),
		Entry("CA bundle with no source", TLSSpec{
			Mode: TLSModeEnabled, ServerCertSecret: tlsServerCert,
			CABundle: &CABundleSource{},
		}, false),
		Entry("CA bundle with both sources", TLSSpec{
			Mode: TLSModeEnabled, ServerCertSecret: tlsServerCert,
			CABundle: &CABundleSource{
				Secret:    &CABundleKeySelector{Name: "ca"},
				ConfigMap: &CABundleKeySelector{Name: "ca"},
			},
		}, false),
	)
})
