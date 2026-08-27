package main

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCommand(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Command Suite")
}

var _ = DescribeTable(
	"Version update interval validation",
	func(interval time.Duration, disableChecks, wantError bool) {
		err := validateVersionUpdateInterval(interval, disableChecks)
		if wantError {
			Expect(err).To(MatchError("--version-update-interval must be greater than 0 when version update checks are enabled"))
		} else {
			Expect(err).ToNot(HaveOccurred())
		}
	},
	Entry("should reject zero interval if checks are enabled", time.Duration(0), false, true),
	Entry("should reject negative interval if checks are enabled", -time.Second, false, true),
	Entry("should accept positive interval if checks are enabled", time.Hour, false, false),
	Entry("should allow zero interval if checks are disabled", time.Duration(0), true, false),
	Entry("should allow negative interval if checks are disabled", -time.Second, true, false),
)
