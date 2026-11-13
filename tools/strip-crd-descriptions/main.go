// Package main removes field descriptions from selected subtrees of generated CRD schemas.
//
// Embedded Kubernetes types (e.g. corev1.PodSpec) carry the full upstream documentation, which
// dominates the CRD size. Stripping descriptions below such fields keeps validation intact and
// keeps the description of the field itself and of every operator-defined field.
//
// Usage:
//
//	strip-crd-descriptions -crd <file> -path <version>:<field.path> [-path ...]
//
// The field path is a dot-separated list of property names starting at the root of the object schema,
// e.g. "v1alpha2:spec.podTemplate.spec". List fields are traversed into their items, e.g.
// "v1alpha2:spec.additionalVolumeClaimTemplateSets.templates.spec".
// Every path must exist, so schema renames fail loudly.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

const filePerm = 0o600

type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }

func (p *pathList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func main() {
	var (
		crdPath string
		paths   pathList
	)

	flag.StringVar(&crdPath, "crd", "", "path to the CRD YAML file, rewritten in place")
	flag.Var(&paths, "path", "<version>:<dot.separated.field.path> whose children lose descriptions (repeatable)")
	flag.Parse()

	if crdPath == "" || len(paths) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(crdPath, paths); err != nil {
		log.Fatalf("strip-crd-descriptions: %v", err)
	}
}

func run(crdPath string, paths []string) error {
	in, err := os.ReadFile(crdPath) //nolint:gosec // path comes from the Makefile
	if err != nil {
		return fmt.Errorf("read %s: %w", crdPath, err)
	}

	out, err := stripCRD(in, paths)
	if err != nil {
		return fmt.Errorf("%s: %w", crdPath, err)
	}

	if err := os.WriteFile(crdPath, out, filePerm); err != nil {
		return fmt.Errorf("write %s: %w", crdPath, err)
	}

	return nil
}

// stripCRD removes descriptions below each requested path and returns the re-encoded CRD
// in the same format controller-gen emits.
func stripCRD(in []byte, paths []string) ([]byte, error) {
	var crd map[string]any
	if err := yaml.Unmarshal(in, &crd); err != nil {
		return nil, fmt.Errorf("parse CRD: %w", err)
	}

	for _, p := range paths {
		version, fieldPath, ok := strings.Cut(p, ":")
		if !ok || version == "" || fieldPath == "" {
			return nil, fmt.Errorf("invalid path %q, expected <version>:<field.path>", p)
		}

		root, err := versionSchema(crd, version)
		if err != nil {
			return nil, err
		}

		node, err := lookup(root, strings.Split(fieldPath, "."))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}

		stripChildren(node)
	}

	out, err := yaml.Marshal(crd)
	if err != nil {
		return nil, fmt.Errorf("encode CRD: %w", err)
	}

	return append([]byte("---\n"), out...), nil
}

func versionSchema(crd map[string]any, version string) (map[string]any, error) {
	spec, _ := crd["spec"].(map[string]any)
	versions, _ := spec["versions"].([]any)

	for _, v := range versions {
		ver, _ := v.(map[string]any)
		if ver["name"] != version {
			continue
		}

		schema, _ := ver["schema"].(map[string]any)
		if root, ok := schema["openAPIV3Schema"].(map[string]any); ok {
			return root, nil
		}

		return nil, fmt.Errorf("version %q has no openAPIV3Schema", version)
	}

	return nil, fmt.Errorf("version %q not found", version)
}

// lookup resolves a dot-separated field path. List fields are traversed transparently:
// a segment following a list field refers to a property of its items.
func lookup(node map[string]any, segments []string) (map[string]any, error) {
	for i, seg := range segments {
		if items, ok := node["items"].(map[string]any); ok {
			node = items
		}

		props, _ := node["properties"].(map[string]any)

		next, ok := props[seg].(map[string]any)
		if !ok {
			return nil, errors.New("field " + strings.Join(segments[:i+1], ".") + " not found")
		}

		node = next
	}

	return node, nil
}

// stripChildren removes descriptions from every schema nested below node, keeping node's own description.
func stripChildren(node map[string]any) {
	forEachSubschema(node, stripSchema)
}

func stripSchema(node map[string]any) {
	delete(node, "description")
	forEachSubschema(node, stripSchema)
}

// forEachSubschema calls fn for every directly nested JSONSchemaProps of node.
// Keys under "properties" are field names, not schema keywords, so a field named
// "description" is never mistaken for a description.
func forEachSubschema(node map[string]any, fn func(map[string]any)) {
	if props, ok := node["properties"].(map[string]any); ok {
		for _, p := range props {
			if s, ok := p.(map[string]any); ok {
				fn(s)
			}
		}
	}

	for _, key := range []string{"items", "additionalProperties", "not"} {
		if s, ok := node[key].(map[string]any); ok {
			fn(s)
		}
	}

	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := node[key].([]any); ok {
			for _, item := range list {
				if s, ok := item.(map[string]any); ok {
					fn(s)
				}
			}
		}
	}
}
