package main

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/yaml"
)

// The fixture deliberately contains a field named "description".
//
//nolint:dupword
const testCRD = `---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: things.example.com
spec:
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        properties:
          spec:
            properties:
              podTemplate:
                description: alpha template
                properties:
                  nodeSelector:
                    description: alpha node selector
                    type: object
                type: object
            type: object
        type: object
  - name: v1alpha2
    schema:
      openAPIV3Schema:
        description: root
        properties:
          spec:
            description: spec
            properties:
              podTemplate:
                description: template
                properties:
                  spec:
                    description: own field doc
                    properties:
                      description:
                        description: a field named description
                        type: string
                      containers:
                        description: containers
                        items:
                          description: container
                          properties:
                            ports:
                              description: ports
                              items:
                                description: port
                                type: object
                              type: array
                          type: object
                        type: array
                      labels:
                        additionalProperties:
                          description: label value
                          type: string
                        description: labels
                        type: object
                      port:
                        anyOf:
                        - description: int
                          type: integer
                        - type: string
                        description: port
                    type: object
                  topologyZoneKey:
                    description: zone key
                    type: string
                type: object
            type: object
        type: object
`

func TestStripCRDDescriptions(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "strip-crd-descriptions Suite")
}

func strip(paths ...string) map[string]any {
	GinkgoHelper()

	out, err := stripCRD([]byte(testCRD), paths)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(out)).To(HavePrefix("---\n"), "output lost the document separator")

	var crd map[string]any
	Expect(yaml.Unmarshal(out, &crd)).To(Succeed())

	return crd
}

func schemaAt(crd map[string]any, version, path string) map[string]any {
	GinkgoHelper()

	root, err := versionSchema(crd, version)
	Expect(err).NotTo(HaveOccurred())

	node, err := lookup(root, strings.Split(path, "."))
	Expect(err).NotTo(HaveOccurred())

	return node
}

func asMap(v any) map[string]any {
	GinkgoHelper()

	m, ok := v.(map[string]any)
	Expect(ok).To(BeTrue(), "expected an object, got %T", v)

	return m
}

func countDescriptions(node map[string]any) int {
	n := 0
	if _, ok := node["description"]; ok {
		n++
	}

	forEachSubschema(node, func(s map[string]any) { n += countDescriptions(s) })

	return n
}

var _ = Describe("stripCRD", func() {
	It("strips only below the path", func() {
		crd := strip("v1alpha2:spec.podTemplate.spec")

		target := schemaAt(crd, "v1alpha2", "spec.podTemplate.spec")
		Expect(target["description"]).To(Equal("own field doc"), "target field lost its own description")
		Expect(countDescriptions(target)).To(Equal(1), "only the target description should remain below the path")

		// The field named "description" must survive as a property, without its doc.
		Expect(asMap(target["properties"])).To(HaveKey("description"), "property named 'description' was removed")

		for path, want := range map[string]string{
			"spec.podTemplate":                 "template",
			"spec.podTemplate.topologyZoneKey": "zone key",
		} {
			Expect(schemaAt(crd, "v1alpha2", path)["description"]).To(Equal(want), path)
		}

		alpha := schemaAt(crd, "v1alpha1", "spec.podTemplate.nodeSelector")
		Expect(alpha["description"]).To(Equal("alpha node selector"), "other version was modified")
	})

	It("keeps validation", func() {
		crd := strip("v1alpha2:spec.podTemplate.spec")
		props := asMap(schemaAt(crd, "v1alpha2", "spec.podTemplate.spec")["properties"])

		Expect(asMap(props["containers"])["type"]).To(Equal("array"), "type removed from containers")
		Expect(asMap(props["port"])["anyOf"]).To(HaveLen(2), "anyOf branches changed")
	})

	DescribeTable("rejects invalid paths",
		func(p string) {
			_, err := stripCRD([]byte(testCRD), []string{p})
			Expect(err).To(HaveOccurred())
		},
		Entry("missing field", "v1alpha2:spec.missing"),
		Entry("unknown version", "v9:spec.podTemplate"),
		Entry("no version", "spec.podTemplate"),
		Entry("empty path", "v1alpha2:"),
	)

	It("traverses list items", func() {
		crd := strip("v1alpha2:spec.podTemplate.spec.containers.ports")

		ports := schemaAt(crd, "v1alpha2", "spec.podTemplate.spec.containers.ports")
		Expect(ports["description"]).To(Equal("ports"), "list field lost its own description")
		Expect(countDescriptions(ports)).To(Equal(1), "only the list field description should remain")

		containers := schemaAt(crd, "v1alpha2", "spec.podTemplate.spec.containers")
		Expect(asMap(containers["items"])["description"]).To(Equal("container"), "descriptions above the path were stripped")
	})
})
