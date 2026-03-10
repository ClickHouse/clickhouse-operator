package upgrade

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

func TestVersionUpgradeUtils(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Version upgrade utils")
}

var _ = DescribeTable("Parse version", func(raw string, version ClickHouseVersion, channel string) {
	parsedVersion, parsedChannel, err := ParseVersion(raw)
	Expect(err).ToNot(HaveOccurred())
	Expect(parsedVersion).To(Equal(version))
	Expect(parsedChannel).To(Equal(channel))

	numericPart := strings.Split(raw, "-")[0][1:]
	Expect(parsedVersion.Version()).To(Equal(numericPart))
},
	Entry("lts version", "v25.8.16.34-lts", ClickHouseVersion{25, 8, 16, 34}, channelLTS),
	Entry("stable version", "v25.12.5.44-stable", ClickHouseVersion{25, 12, 5, 44}, channelStable),
	Entry("without build info", "v19.3.6-stable", ClickHouseVersion{19, 3, 6, 0}, channelStable),
	Entry("before date semantics", "v1.1.54011-stable", ClickHouseVersion{1, 1, 54011, 0}, channelStable),
)

var _ = DescribeTable("Parse bare version", func(raw string, expected ClickHouseVersion) {
	parsedVersion, err := ParseBareVersion(raw)
	Expect(err).ToNot(HaveOccurred())
	Expect(parsedVersion).To(Equal(expected))
	Expect(parsedVersion.Version()).To(Equal(raw))
},
	Entry("four components", "25.8.2.1", ClickHouseVersion{25, 8, 2, 1}),
	Entry("three components", "25.8.2", ClickHouseVersion{25, 8, 2, 0}),
	Entry("large build number", "26.3.1.100", ClickHouseVersion{26, 3, 1, 100}),
)

var _ = DescribeTable("Parse bare version errors", func(raw string) {
	_, err := ParseBareVersion(raw)
	Expect(err).To(HaveOccurred())
},
	Entry("with channel suffix", "v25.8.2.1-lts"),
	Entry("with v prefix", "v25.8.2.1"),
	Entry("too few components", "25.8"),
	Entry("empty string", ""),
)

var _ = Describe("URL version fetcher", func() {
	const sampleTSV = "v25.8.3.16-lts\t2025-03-01\n" +
		"v25.8.2.15-lts\t2025-02-15\n" +
		"v26.1.2.42-stable\t2026-02-01\n" +
		"v26.2.1.3-stable\t2026-03-01\n"

	It("should parse fetched releases", func(ctx context.Context) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleTSV))
		}))
		defer srv.Close()

		fetcher := NewURLFetcher()
		fetcher.VersionsURL = srv.URL

		releases, err := fetcher.FetchReleases(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(slices.Collect(maps.Keys(releases))).To(ContainElements(channelLTS, channelStable))
		Expect(releases[channelLTS]).To(ConsistOf(
			ClickHouseVersion{25, 8, 3, 16},
			ClickHouseVersion{25, 8, 2, 15},
		))
		Expect(releases[channelStable]).To(ConsistOf(
			ClickHouseVersion{26, 1, 2, 42},
			ClickHouseVersion{26, 2, 1, 3},
		))
	})

	It("should return error on non-200 status", func(ctx context.Context) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		fetcher := NewURLFetcher()
		fetcher.VersionsURL = srv.URL

		_, err := fetcher.FetchReleases(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("500"))
	})
})

var _ = Describe("Version Checker", func() {
	fetcher := &StaticFetcher{Releases: map[string][]ClickHouseVersion{
		channelStable: {
			{25, 1, 2, 3},
			{25, 2, 1, 21},
			{25, 10, 5, 12},
			{26, 1, 1, 7},
			{26, 1, 2, 42},
			{26, 2, 1, 3},
		},
		channelLTS: {
			{25, 3, 4, 11},
			{25, 8, 1, 5},
			{25, 8, 2, 15},
			{25, 8, 3, 16},
			{26, 3, 1, 1},
		},
	}}

	logger := zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	logf.SetLogger(zapr.NewLogger(logger))
	updater := NewReleaseUpdater(fetcher, time.Hour*24, controllerutil.NewLogger(logger))
	checker := NewChecker(updater)

	BeforeEach(func(ctx context.Context) {
		// Run update loop for the first test and use cached data for the following.
		if updater.cached.Load() != nil {
			return
		}

		updCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			defer GinkgoRecover()

			Expect(updater.Start(updCtx)).To(Succeed())
		}()

		Eventually(updater.GetReleasesData).ShouldNot(BeNil())
	})

	It("should cache latest supported releases", func() {
		_, err := checker.CheckUpdates("25.8.2.1", "25.8")
		Expect(err).ToNot(HaveOccurred())

		data := updater.GetReleasesData()
		Expect(data).ToNot(BeNil())

		releases := releaseMap{
			channelStable: {
				{25, 1}:  {25, 1, 2, 3},
				{25, 2}:  {25, 2, 1, 21},
				{25, 10}: {25, 10, 5, 12},
				{26, 1}:  {26, 1, 2, 42},
				{26, 2}:  {26, 2, 1, 3},
			},
			channelLTS: {
				{25, 3}: {25, 3, 4, 11},
				{25, 8}: {25, 8, 3, 16},
				{26, 3}: {26, 3, 1, 1},
			},
		}
		Expect(data.Releases).To(Equal(releases), "diff:\n"+cmp.Diff(data.Releases, releases))

		supported := map[ClickHouseRelease]bool{
			{26, 3}:  true,
			{25, 8}:  true,
			{26, 2}:  true,
			{26, 1}:  true,
			{25, 10}: true,
		}
		Expect(data.Supported).To(Equal(supported), "diff:\n"+cmp.Diff(data.Supported, supported))
	})

	DescribeTable("expected result", func(current string, channel string, expected CheckResult) {
		result, err := checker.CheckUpdates(current, channel)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(expected), "diff:\n"+cmp.Diff(result, expected))
	},
		Entry("minor updates", "25.8.2.1", "25.8",
			CheckResult{
				MinorUpdate: &ClickHouseVersion{25, 8, 3, 16},
				Outdated:    false,
				OnChannel:   true,
			},
		),
		Entry("minor updates without channel", "25.8.2.1", "",
			CheckResult{
				MinorUpdate: &ClickHouseVersion{25, 8, 3, 16},
				Outdated:    false,
				OnChannel:   true,
			},
		),
		Entry("stable and lts updates on stable channel", "25.10.5.12", channelStable,
			CheckResult{
				MajorUpdates: []ClickHouseVersion{
					{26, 1, 2, 42},
					{26, 2, 1, 3},
					{26, 3, 1, 1},
				},
				Outdated:  false,
				OnChannel: true,
			},
		),
		Entry("lts updates on lts channel", "25.3.4.11", channelLTS,
			CheckResult{
				MajorUpdates: []ClickHouseVersion{
					{25, 8, 3, 16},
					{26, 3, 1, 1},
				},
				Outdated:  true,
				OnChannel: true,
			},
		),
		Entry("mark old versions outdated", "25.1.2.3", "25.1",
			CheckResult{
				Outdated:  true,
				OnChannel: true,
			},
		),
		Entry("outdated checks without channel", "25.1.2.3", "",
			CheckResult{
				Outdated:  true,
				OnChannel: true,
			},
		),
		Entry("not on lts channel if stable version", "26.1.2.42", channelLTS,
			CheckResult{
				MajorUpdates: []ClickHouseVersion{
					{26, 3, 1, 1},
				},
				Outdated:  false,
				OnChannel: false,
			},
		),
		Entry("on stable channel if lts version", "26.3.1.1", channelStable,
			CheckResult{
				Outdated:  false,
				OnChannel: true,
			},
		),
		Entry("not on release channel if release differs", "26.3.1.1", "26.2",
			CheckResult{
				Outdated:  false,
				OnChannel: false,
			},
		),
	)
})
