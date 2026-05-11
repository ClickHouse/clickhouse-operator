// Package main generates the clickhouse-cluster-helm chart's values.yaml from
// ClickHouseCluster + KeeperCluster CRD schemas via an embedded text template.
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	sigyaml "sigs.k8s.io/yaml"
)

const (
	dirPerm  = 0o750
	filePerm = 0o600
)

var (
	//go:embed templates/values.yaml.tmpl
	valuesTemplateStr string

	valuesTemplate = template.Must(template.New("values").Funcs(template.FuncMap{
		"yaml":    tmplYAML,
		"indent":  tmplIndent,
		"comment": tmplComment,
	}).Parse(valuesTemplateStr))
)

type chartValues struct {
	ClickHouseFields []fieldBlock
	KeeperFields     []fieldBlock
}

type fieldBlock struct {
	Key         string
	Description string
	Constraints string
	HasValue    bool
	Multiline   bool
	Value       any
	Zero        string
}

func main() {
	clickhouseCRD := ""
	keeperCRD := ""
	outDir := ""

	flag.StringVar(&clickhouseCRD, "clickhouse-crd", "", "Path to ClickHouseCluster CRD YAML")
	flag.StringVar(&keeperCRD, "keeper-crd", "", "Path to KeeperCluster CRD YAML")
	flag.StringVar(&outDir, "out", "", "Output chart directory")
	flag.Parse()

	if clickhouseCRD == "" || keeperCRD == "" || outDir == "" {
		fmt.Fprintln(os.Stderr,
			"usage: gen-cluster-chart -clickhouse-crd <path> -keeper-crd <path> -out <chart-dir>")
		os.Exit(2)
	}

	if err := run(clickhouseCRD, keeperCRD, outDir); err != nil {
		log.Fatalf("cluster chart generation failed: %v", err)
	}
}

func run(clickhouseCRD, keeperCRD, outDir string) error {
	chFields, err := buildFields(clickhouseCRD)
	if err != nil {
		return fmt.Errorf("extract clickhouse fields from CRD: %w", err)
	}

	kFields, err := buildFields(keeperCRD)
	if err != nil {
		return fmt.Errorf("extract keeper fields from CRD: %w", err)
	}

	var buf bytes.Buffer
	if err := valuesTemplate.Execute(&buf, chartValues{
		ClickHouseFields: chFields,
		KeeperFields:     kFields,
	}); err != nil {
		return fmt.Errorf("render values template: %w", err)
	}

	if err := os.MkdirAll(outDir, dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	out := filepath.Join(outDir, "values.yaml")

	data := regexp.MustCompile(`\n\n\n+`).ReplaceAll(buf.Bytes(), []byte("\n\n"))
	if err := os.WriteFile(out, data, filePerm); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	if _, err := fmt.Fprintf(os.Stdout, "wrote %s\n", out); err != nil {
		return fmt.Errorf("stdout: %w", err)
	}

	return nil
}

func buildFields(crdPath string) ([]fieldBlock, error) {
	b, err := os.ReadFile(filepath.Clean(crdPath))
	if err != nil {
		return nil, fmt.Errorf("open crd file: %w", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err = sigyaml.Unmarshal(b, &crd); err != nil {
		return nil, fmt.Errorf("unmarshal crd: %w", err)
	}

	spec, err := getSpecSchema(&crd)
	if err != nil {
		return nil, err
	}

	fields := make([]fieldBlock, 0, len(spec.Properties))

	for _, name := range slices.Sorted(maps.Keys(spec.Properties)) {
		f, err := buildField(name, spec.Properties[name])
		if err != nil {
			return nil, fmt.Errorf("build field %s: %w", name, err)
		}

		fields = append(fields, f)
	}

	return fields, nil
}

func getSpecSchema(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.JSONSchemaProps, error) {
	var ver *apiextensionsv1.CustomResourceDefinitionVersion

	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Storage {
			ver = &crd.Spec.Versions[i]
			break
		}
	}

	if ver == nil {
		return nil, fmt.Errorf("no storage version in CRD %s", crd.Name)
	}

	if ver.Schema == nil || ver.Schema.OpenAPIV3Schema == nil {
		return nil, fmt.Errorf("no schema in CRD %s", crd.Name)
	}

	spec, ok := ver.Schema.OpenAPIV3Schema.Properties["spec"]
	if !ok {
		return nil, fmt.Errorf("no spec property in CRD %s", crd.Name)
	}

	return &spec, nil
}

func buildField(name string, prop apiextensionsv1.JSONSchemaProps) (fieldBlock, error) {
	f := fieldBlock{
		Key:         name,
		Description: strings.TrimSpace(prop.Description),
	}

	if prop.Default == nil {
		f.Zero = zeroRepr(prop)
		return f, nil
	}

	v, err := decodeDefault(prop.Default)
	if err != nil {
		return fieldBlock{}, err
	}

	f.HasValue = true
	f.Value = v
	f.Multiline = isMultiline(v)

	return f, nil
}

func zeroRepr(prop apiextensionsv1.JSONSchemaProps) string {
	switch prop.Type {
	case "string":
		return `""`
	case "integer", "number":
		return "0"
	case "boolean":
		return "false"
	case "array":
		return "[]"
	case "object":
		return "{}"
	default:
		return "null"
	}
}

func decodeDefault(def *apiextensionsv1.JSON) (any, error) {
	var v any
	if err := sigyaml.Unmarshal(def.Raw, &v); err != nil {
		return nil, fmt.Errorf("unmarshal default %q: %w", string(def.Raw), err)
	}

	return v, nil
}

func isMultiline(v any) bool {
	switch v.(type) {
	case nil, bool, string, int, int32, int64, float32, float64:
		return false
	}

	b, err := yaml.Marshal(v)
	if err != nil {
		return false
	}

	return strings.Contains(strings.TrimRight(string(b), "\n"), "\n")
}

// Template helpers.

func tmplYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal yaml: %w", err)
	}

	return strings.TrimRight(string(b), "\n"), nil
}

func tmplIndent(countRaw, strRaw any) (string, error) {
	count, ok := countRaw.(int)
	if !ok {
		return "", fmt.Errorf("indent: expected int, got %T", countRaw)
	}

	str, ok := strRaw.(string)
	if !ok {
		return "", fmt.Errorf("indent: expected string, got %T", strRaw)
	}

	pad := strings.Repeat(" ", count)

	b := strings.Builder{}
	for i, line := range strings.Split(str, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}

		if line != "" {
			b.WriteString(pad)
		}

		b.WriteString(line)
	}

	return b.String(), nil
}

func tmplComment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	b := strings.Builder{}
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}

		if line == "" {
			b.WriteByte('#')
			continue
		}

		b.WriteString("# ")
		b.WriteString(line)
	}

	return b.String()
}
