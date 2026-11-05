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
