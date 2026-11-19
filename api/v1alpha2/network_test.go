package v1alpha2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
	})
})
