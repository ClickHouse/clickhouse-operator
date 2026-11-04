package main

import (
	"strings"
	"testing"

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

func strip(t *testing.T, paths ...string) map[string]any {
	t.Helper()

	out, err := stripCRD([]byte(testCRD), paths)
	if err != nil {
		t.Fatalf("stripCRD: %v", err)
	}

	if !strings.HasPrefix(string(out), "---\n") {
		t.Fatalf("output lost the document separator")
	}

	var crd map[string]any
	if err := yaml.Unmarshal(out, &crd); err != nil {
		t.Fatalf("parse output: %v", err)
	}

	return crd
}

func schemaAt(t *testing.T, crd map[string]any, version, path string) map[string]any {
	t.Helper()

	root, err := versionSchema(crd, version)
	if err != nil {
		t.Fatal(err)
	}

	node, err := lookup(root, strings.Split(path, "."))
	if err != nil {
		t.Fatal(err)
	}

	return node
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected an object, got %T", v)
	}

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

func TestStripsOnlyBelowPath(t *testing.T) {
	crd := strip(t, "v1alpha2:spec.podTemplate.spec")

	target := schemaAt(t, crd, "v1alpha2", "spec.podTemplate.spec")
	if target["description"] != "own field doc" {
		t.Errorf("target field lost its own description: %v", target["description"])
	}

	if got := countDescriptions(target); got != 1 {
		t.Errorf("expected only the target description to remain below the path, got %d", got)
	}

	// The field named "description" must survive as a property, without its doc.
	props := asMap(t, target["properties"])
	if _, ok := props["description"]; !ok {
		t.Errorf("property named 'description' was removed")
	}

	for path, want := range map[string]string{
		"spec.podTemplate":                 "template",
		"spec.podTemplate.topologyZoneKey": "zone key",
	} {
		if got := schemaAt(t, crd, "v1alpha2", path)["description"]; got != want {
			t.Errorf("%s description = %v, want %q", path, got, want)
		}
	}

	alpha := schemaAt(t, crd, "v1alpha1", "spec.podTemplate.nodeSelector")
	if alpha["description"] != "alpha node selector" {
		t.Errorf("other version was modified")
	}
}

func TestKeepsValidation(t *testing.T) {
	crd := strip(t, "v1alpha2:spec.podTemplate.spec")
	target := schemaAt(t, crd, "v1alpha2", "spec.podTemplate.spec")
	props := asMap(t, target["properties"])

	if asMap(t, props["containers"])["type"] != "array" {
		t.Errorf("type removed from containers")
	}

	if anyOf, _ := asMap(t, props["port"])["anyOf"].([]any); len(anyOf) != 2 {
		t.Errorf("anyOf branches changed")
	}
}

func TestErrors(t *testing.T) {
	for _, p := range []string{
		"v1alpha2:spec.missing",
		"v9:spec.podTemplate",
		"spec.podTemplate",
		"v1alpha2:",
	} {
		if _, err := stripCRD([]byte(testCRD), []string{p}); err == nil {
			t.Errorf("expected error for path %q", p)
		}
	}
}

func TestTraversesListItems(t *testing.T) {
	crd := strip(t, "v1alpha2:spec.podTemplate.spec.containers.ports")

	ports := schemaAt(t, crd, "v1alpha2", "spec.podTemplate.spec.containers.ports")
	if ports["description"] != "ports" {
		t.Errorf("list field lost its own description: %v", ports["description"])
	}

	if got := countDescriptions(ports); got != 1 {
		t.Errorf("expected only the list field description to remain, got %d", got)
	}

	containers := schemaAt(t, crd, "v1alpha2", "spec.podTemplate.spec.containers")
	if asMap(t, containers["items"])["description"] != "container" {
		t.Errorf("descriptions above the path were stripped")
	}
}
