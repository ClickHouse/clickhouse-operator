package v1alpha2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("ImageVersionCatalog", func() {
	const customImage = "registry.example.com/clickhouse-server:25.12.1.649"

	templateWith := func(containers ...corev1.Container) NamedPodTemplate {
		return NamedPodTemplate{Name: "default", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}}}
	}

	Describe("ResolveImageVersion", func() {
		It("should resolve the default image from the built-in catalog", func() {
			spec := ClickHouseClusterSpec{}
			version, ok := spec.ResolveImageVersion(DefaultClickHouseImage())
			Expect(ok).To(BeTrue())
			Expect(version).To(Equal(DefaultClickHouseVersion))
		})

		It("should prefer the user catalog over the built-in catalog", func() {
			spec := ClickHouseClusterSpec{ImageVersionCatalog: []ImageVersionEntry{
				{Image: DefaultClickHouseImage(), Version: "25.8"},
			}}
			version, ok := spec.ResolveImageVersion(DefaultClickHouseImage())
			Expect(ok).To(BeTrue())
			Expect(version).To(Equal("25.8"))
		})

		It("should match images by exact string", func() {
			spec := ClickHouseClusterSpec{ImageVersionCatalog: []ImageVersionEntry{
				{Image: "docker.io/clickhouse/clickhouse-server:25.8.2.29", Version: "25.8.2.29"},
			}}
			_, ok := spec.ResolveImageVersion("clickhouse/clickhouse-server:25.8.2.29")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("ValidateImageVersionCatalog", func() {
		It("should accept a spec without pod templates", func() {
			Expect((&ClickHouseClusterSpec{}).ValidateImageVersionCatalog()).To(Succeed())
		})

		It("should accept a clickhouse container without an image", func() {
			spec := ClickHouseClusterSpec{PodTemplates: []NamedPodTemplate{
				templateWith(corev1.Container{Name: ClickHouseServerContainerName}),
			}}
			Expect(spec.ValidateImageVersionCatalog()).To(Succeed())
		})

		It("should accept the default image without a user catalog", func() {
			spec := ClickHouseClusterSpec{PodTemplates: []NamedPodTemplate{
				templateWith(corev1.Container{Name: ClickHouseServerContainerName, Image: DefaultClickHouseImage()}),
			}}
			Expect(spec.ValidateImageVersionCatalog()).To(Succeed())
		})

		It("should reject a custom image without a catalog entry", func() {
			spec := ClickHouseClusterSpec{PodTemplates: []NamedPodTemplate{
				templateWith(corev1.Container{Name: ClickHouseServerContainerName, Image: customImage}),
			}}
			err := spec.ValidateImageVersionCatalog()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.podTemplates[0].spec.containers[0].image"))
			Expect(err.Error()).To(ContainSubstring(customImage))
		})

		It("should accept a custom image with a catalog entry", func() {
			spec := ClickHouseClusterSpec{
				ImageVersionCatalog: []ImageVersionEntry{{Image: customImage, Version: "25.12.1.649"}},
				PodTemplates: []NamedPodTemplate{
					templateWith(corev1.Container{Name: ClickHouseServerContainerName, Image: customImage}),
				},
			}
			Expect(spec.ValidateImageVersionCatalog()).To(Succeed())
		})

		It("should ignore sidecar and init container images", func() {
			tmpl := templateWith(corev1.Container{Name: "log-shipper", Image: "fluent/fluent-bit:3.1"})
			tmpl.Spec.InitContainers = []corev1.Container{{Name: ClickHouseServerContainerName, Image: "busybox:1.36"}}
			spec := ClickHouseClusterSpec{PodTemplates: []NamedPodTemplate{tmpl}}
			Expect(spec.ValidateImageVersionCatalog()).To(Succeed())
		})

		It("should check unreferenced templates too", func() {
			spec := ClickHouseClusterSpec{PodTemplates: []NamedPodTemplate{
				templateWith(corev1.Container{Name: ClickHouseServerContainerName, Image: customImage}),
			}}
			spec.PodTemplateName = ""
			Expect(spec.ValidateImageVersionCatalog()).NotTo(Succeed())
		})
	})
})

var _ = DescribeTable("isPinnedImage",
	func(image string, pinned bool) {
		Expect(isPinnedImage(image)).To(Equal(pinned))
	},
	Entry("short tag", "repo:25.8", true),
	Entry("full reference", "docker.io/clickhouse/clickhouse-server:25.8.2.29", true),
	Entry("registry port with tag", "host:5000/ch/server:25.8", true),
	Entry("digest", "repo@sha256:9f2c1e", true),
	Entry("untagged", "repo", false),
	Entry("latest", "repo:latest", false),
	Entry("registry port untagged", "host:5000/ch/server", false),
	Entry("registry port latest", "host:5000/ch/server:latest", false),
	Entry("empty tag", "repo:", false),
	Entry("major-only tag", "repo:25", false),
	Entry("major-only variant tag", "repo:25-alpine", false),
	Entry("registry port major-only tag", "host:5000/ch/server:25", false),
	Entry("major.minor variant tag", "repo:25.8-alpine", true),
	Entry("custom tag", "repo:prod-2026-11", true),
)

var _ = Describe("ValidateImageVersionCatalog mutable tags", func() {
	for _, image := range []string{"docker.io/clickhouse/clickhouse-server:latest", "docker.io/clickhouse/clickhouse-server"} {
		It("should reject "+image+" even with a catalog entry", func() {
			spec := ClickHouseClusterSpec{
				ImageVersionCatalog: []ImageVersionEntry{{Image: image, Version: "25.8"}},
				PodTemplates: []NamedPodTemplate{{Name: "default", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: ClickHouseServerContainerName, Image: image}},
				}}}},
			}
			err := spec.ValidateImageVersionCatalog()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must be pinned"))
		})
	}
})
