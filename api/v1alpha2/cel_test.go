package v1alpha2

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// celSchemaValidator runs the CEL rules of a v1alpha2 CRD sub-schema, as the API server does.
// v1alpha2 is not served yet, so its rules cannot be exercised through envtest.
type celSchemaValidator struct {
	structural *schema.Structural
	validator  *cel.Validator
}

// newCELSchemaValidator loads the v1alpha2 schema of the given generated CRD and
// returns a validator for the sub-schema found by following the property path.
func newCELSchemaValidator(crdFile string, path ...string) *celSchemaValidator {
	GinkgoHelper()

	data, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", crdFile)) //nolint:gosec // Test fixture path.
	Expect(err).NotTo(HaveOccurred())

	var crd apiextensionsv1.CustomResourceDefinition
	Expect(yaml.Unmarshal(data, &crd)).To(Succeed())

	var props *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == GroupVersion.Version {
			props = crd.Spec.Versions[i].Schema.OpenAPIV3Schema
		}
	}

	Expect(props).NotTo(BeNil(), "CRD %s has no %s schema", crdFile, GroupVersion.Version)

	for _, name := range path {
		sub, ok := props.Properties[name]
		Expect(ok).To(BeTrue(), "schema has no property %q", name)

		props = &sub
	}

	var internal apiextensions.JSONSchemaProps
	Expect(apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(props, &internal, nil)).To(Succeed())

	structural, err := schema.NewStructural(&internal)
	Expect(err).NotTo(HaveOccurred())

	validator := cel.NewValidator(structural, false, celconfig.PerCallLimit)
	Expect(validator).NotTo(BeNil(), "schema has no CEL rules")

	return &celSchemaValidator{structural: structural, validator: validator}
}

func (v *celSchemaValidator) validate(fldPath *field.Path, obj map[string]any) field.ErrorList {
	return v.validateUpdate(fldPath, obj, nil)
}

// validateUpdate runs the rules for an update from old to obj, including the transition rules.
func (v *celSchemaValidator) validateUpdate(fldPath *field.Path, obj, old map[string]any) field.ErrorList {
	var oldObj any
	if old != nil {
		oldObj = old
	}

	errs, _ := v.validator.Validate(context.Background(), fldPath, v.structural, obj, oldObj, celconfig.RuntimeCELCostBudget)

	return errs
}

// expectCELErrors asserts that errs is empty when expectedMessage is empty, or holds a single error with it.
func expectCELErrors(errs field.ErrorList, expectedMessage string) {
	GinkgoHelper()

	if expectedMessage == "" {
		Expect(errs).To(BeEmpty())
		return
	}

	Expect(errs).To(HaveLen(1), "errors: %v", errs)
	Expect(errs[0].Detail).To(ContainSubstring(expectedMessage))
}

var _ = Describe("ClickHouseNetworkSpec CEL validation", func() {
	const (
		duplicatePort = "additionalPorts must not repeat the same port"
		reservedPort  = "additionalPorts must not use a port reserved for operator-managed endpoints"
		reservedName  = "additionalPorts must not use a name reserved by the operator"
	)

	var network *celSchemaValidator

	BeforeEach(func() {
		network = newCELSchemaValidator("clickhouse.com_clickhouseclusters.yaml", "spec", "network")
	})

	port := func(name string, number int64) map[string]any {
		return map[string]any{"name": name, "port": number}
	}

	DescribeTable("additionalPorts",
		func(ports []any, expectedMessage string) {
			errs := network.validate(field.NewPath("spec", "network"), map[string]any{"additionalPorts": ports})
			if expectedMessage == "" {
				Expect(errs).To(BeEmpty())
				return
			}

			Expect(errs).To(HaveLen(1))
			Expect(errs[0].Detail).To(ContainSubstring(expectedMessage))
		},
		Entry("no ports", []any{}, ""),
		Entry("one custom port", []any{port("metrics2", 9500)}, ""),
		Entry("distinct custom ports", []any{port("metrics2", 9500), port("grpc", 9100)}, ""),
		Entry("same port under different names", []any{port("a", 9500), port("b", 9500)}, duplicatePort),
		Entry("reserved HTTP port", []any{port("custom", 8123)}, reservedPort),
		Entry("reserved HTTPS port", []any{port("custom", 8443)}, reservedPort),
		Entry("reserved native port", []any{port("custom", 9000)}, reservedPort),
		Entry("reserved native TLS port", []any{port("custom", 9440)}, reservedPort),
		Entry("reserved interserver port", []any{port("custom", 9009)}, reservedPort),
		Entry("reserved Prometheus port", []any{port("custom", 9363)}, reservedPort),
		Entry("reserved management port", []any{port("custom", 9001)}, reservedPort),
		Entry("reserved management HTTP port", []any{port("custom", 9002)}, reservedPort),
		Entry("reserved name http", []any{port("http", 9500)}, reservedName),
		Entry("reserved name http-secure", []any{port("http-secure", 9500)}, reservedName),
		Entry("reserved name tcp", []any{port("tcp", 9500)}, reservedName),
		Entry("reserved name tcp-secure", []any{port("tcp-secure", 9500)}, reservedName),
		Entry("reserved name interserver", []any{port("interserver", 9500)}, reservedName),
		Entry("reserved name prometheus", []any{port("prometheus", 9500)}, reservedName),
		Entry("reserved name management", []any{port("management", 9500)}, reservedName),
		Entry("reserved name management-http", []any{port("management-http", 9500)}, reservedName),
	)
})

// Package-level because DescribeTable Entry arguments are evaluated at tree construction.
var (
	celSpecPath       = field.NewPath("spec")
	celCABundleKey    = map[string]any{"name": "ca"}
	celKeeperWithData = map[string]any{"dataVolumeClaimTemplate": map[string]any{}}
)

var _ = Describe("TLSSpec CEL validation", func() {
	const (
		missingCert  = "serverCertSecret must be specified when TLS is enabled"
		caBundleMode = "exactly one of secret or configMap must be set"
	)

	var tls *celSchemaValidator

	BeforeEach(func() {
		tls = newCELSchemaValidator("clickhouse.com_clickhouseclusters.yaml", "spec", "network", "tls")
	})

	DescribeTable("tls",
		func(spec map[string]any, expectedMessage string) {
			expectCELErrors(tls.validate(field.NewPath("spec", "network", "tls"), spec), expectedMessage)
		},
		Entry("disabled without cert", map[string]any{"mode": "Disabled"}, ""),
		Entry("enabled without cert", map[string]any{"mode": "Enabled"}, missingCert),
		Entry("required without cert", map[string]any{"mode": "Required"}, missingCert),
		Entry("enabled with empty cert name", map[string]any{"mode": "Enabled", "serverCertSecret": map[string]any{"name": ""}}, missingCert),
		Entry("enabled with cert", map[string]any{"mode": "Enabled", "serverCertSecret": map[string]any{"name": "cert"}}, ""),
		Entry("CA bundle from secret", map[string]any{"caBundle": map[string]any{"secret": celCABundleKey}}, ""),
		Entry("CA bundle from configMap", map[string]any{"caBundle": map[string]any{"configMap": celCABundleKey}}, ""),
		Entry("CA bundle with no source", map[string]any{"caBundle": map[string]any{}}, caBundleMode),
		Entry("CA bundle with both sources",
			map[string]any{"caBundle": map[string]any{"secret": celCABundleKey, "configMap": celCABundleKey}}, caBundleMode),
	)
})

var _ = Describe("PodDisruptionBudgetTemplate CEL validation", func() {
	var pdb *celSchemaValidator

	BeforeEach(func() {
		pdb = newCELSchemaValidator("clickhouse.com_clickhouseclusters.yaml", "spec", "podDisruptionBudget")
	})

	DescribeTable("podDisruptionBudget",
		func(spec map[string]any, expectedMessage string) {
			errs := pdb.validate(field.NewPath("spec", "podDisruptionBudget"), map[string]any{"spec": spec})
			expectCELErrors(errs, expectedMessage)
		},
		Entry("minAvailable only", map[string]any{"minAvailable": int64(1)}, ""),
		Entry("selector", map[string]any{"selector": map[string]any{}}, "selector is managed by the operator"),
		Entry("minAvailable and maxUnavailable", map[string]any{"minAvailable": int64(1), "maxUnavailable": int64(1)}, "only one of"),
	)
})

var _ = Describe("additional volume claim template sets CEL validation", func() {
	const (
		encryptedDisk  = "names ending in '-encrypted' are reserved"
		reservedDisk   = "disk names 'clickhouse-storage-volume' and 'default' are reserved"
		duplicateDisk  = "disk names must be unique within a set"
		disksChanged   = "disks of an existing set cannot be added, removed or renamed"
		clusterChanged = "additionalVolumeClaimTemplateSetName cannot be changed"
		shardChanged   = "shard additionalVolumeClaimTemplateSetName cannot be changed"
		replicaChanged = "replica additionalVolumeClaimTemplateSetName cannot be changed"
	)

	var spec *celSchemaValidator

	BeforeEach(func() {
		spec = newCELSchemaValidator("clickhouse.com_clickhouseclusters.yaml", "spec")
	})

	set := func(name string, disks ...string) map[string]any {
		templates := make([]any, 0, len(disks))
		for _, d := range disks {
			templates = append(templates, map[string]any{"metadata": map[string]any{"name": d}})
		}

		return map[string]any{"name": name, "templates": templates}
	}

	sets := func(s ...map[string]any) []any {
		out := make([]any, 0, len(s))
		for _, v := range s {
			out = append(out, v)
		}

		return out
	}

	DescribeTable("disk names",
		func(disks []string, expectedMessage string) {
			errs := spec.validate(celSpecPath, map[string]any{"additionalVolumeClaimTemplateSets": sets(set("jbod", disks...))})
			expectCELErrors(errs, expectedMessage)
		},
		Entry("distinct disks", []string{"disk1", "disk2"}, ""),
		Entry("duplicate disk", []string{"disk1", "disk1"}, duplicateDisk),
		Entry("encrypted suffix", []string{"disk1-encrypted"}, encryptedDisk),
		Entry("data volume name", []string{"clickhouse-storage-volume"}, reservedDisk),
		Entry("default disk name", []string{"default"}, reservedDisk),
	)

	DescribeTable("disks of an existing set",
		func(oldDisks, newDisks []string, expectedMessage string) {
			errs := spec.validateUpdate(celSpecPath,
				map[string]any{"additionalVolumeClaimTemplateSets": sets(set("jbod", newDisks...))},
				map[string]any{"additionalVolumeClaimTemplateSets": sets(set("jbod", oldDisks...))},
			)
			expectCELErrors(errs, expectedMessage)
		},
		Entry("unchanged", []string{"disk1", "disk2"}, []string{"disk1", "disk2"}, ""),
		Entry("reordered", []string{"disk1", "disk2"}, []string{"disk2", "disk1"}, ""),
		Entry("added", []string{"disk1"}, []string{"disk1", "disk2"}, disksChanged),
		Entry("removed", []string{"disk1", "disk2"}, []string{"disk1"}, disksChanged),
		Entry("renamed", []string{"disk1"}, []string{"disk2"}, disksChanged),
	)

	It("should allow a new set with any disks", func() {
		errs := spec.validateUpdate(celSpecPath,
			map[string]any{"additionalVolumeClaimTemplateSets": sets(set("a", "disk1"), set("b", "disk1", "disk2"))},
			map[string]any{"additionalVolumeClaimTemplateSets": sets(set("a", "disk1"))},
		)
		Expect(errs).To(BeEmpty())
	})

	cluster := func(setName string, shards ...any) map[string]any {
		obj := map[string]any{"additionalVolumeClaimTemplateSets": sets(set("a", "disk1"), set("b", "disk2"))}
		if setName != "" {
			obj["additionalVolumeClaimTemplateSetName"] = setName
		}

		if len(shards) > 0 {
			obj["shardSpecs"] = shards
		}

		return obj
	}

	shard := func(name, setName string, replicas ...any) map[string]any {
		obj := map[string]any{"name": name}
		if setName != "" {
			obj["additionalVolumeClaimTemplateSetName"] = setName
		}

		if len(replicas) > 0 {
			obj["replicaSpecs"] = replicas
		} else {
			obj["replicas"] = int64(1)
		}

		return obj
	}

	replica := func(name, setName string) map[string]any {
		obj := map[string]any{"name": name}
		if setName != "" {
			obj["additionalVolumeClaimTemplateSetName"] = setName
		}

		return obj
	}

	DescribeTable("set references",
		func(oldSpec, newSpec map[string]any, expectedMessage string) {
			expectCELErrors(spec.validateUpdate(celSpecPath, newSpec, oldSpec), expectedMessage)
		},
		Entry("cluster unchanged", cluster("a"), cluster("a"), ""),
		Entry("cluster changed", cluster("a"), cluster("b"), clusterChanged),
		Entry("cluster set", cluster(""), cluster("a"), clusterChanged),
		Entry("cluster unset", cluster("a"), cluster(""), clusterChanged),
		Entry("shard changed", cluster("", shard("s", "a")), cluster("", shard("s", "b")), shardChanged),
		Entry("shard set", cluster("", shard("s", "")), cluster("", shard("s", "a")), shardChanged),
		Entry("new shard with any set",
			cluster("", shard("s", "a")), cluster("", shard("s", "a"), shard("t", "b")), ""),
		Entry("replica changed",
			cluster("", shard("s", "", replica("r", "a"))), cluster("", shard("s", "", replica("r", "b"))), replicaChanged),
		Entry("replica unset",
			cluster("", shard("s", "", replica("r", "a"))), cluster("", shard("s", "", replica("r", ""))), replicaChanged),
		Entry("new replica with any set",
			cluster("", shard("s", "", replica("r", "a"))),
			cluster("", shard("s", "", replica("r", "a"), replica("q", "b"))), ""),
		Entry("other replica removed",
			cluster("", shard("s", "", replica("p", "b"), replica("r", "a"))),
			cluster("", shard("s", "", replica("r", "a"))), ""),
	)
})

var _ = Describe("KeeperClusterSpec CEL validation", func() {
	const (
		dataVolumeChanged = "dataVolumeClaimTemplate cannot be added or removed after cluster creation"
		reservedVolume    = "podTemplate volumes must not use a name reserved by the operator"
	)

	var spec *celSchemaValidator

	BeforeEach(func() {
		spec = newCELSchemaValidator("clickhouse.com_keeperclusters.yaml", "spec")
	})

	DescribeTable("dataVolumeClaimTemplate",
		func(oldSpec, newSpec map[string]any, expectedMessage string) {
			expectCELErrors(spec.validateUpdate(celSpecPath, newSpec, oldSpec), expectedMessage)
		},
		Entry("kept", celKeeperWithData, celKeeperWithData, ""),
		Entry("kept absent", map[string]any{}, map[string]any{}, ""),
		Entry("added", map[string]any{}, celKeeperWithData, dataVolumeChanged),
		Entry("removed", celKeeperWithData, map[string]any{}, dataVolumeChanged),
	)

	DescribeTable("podTemplate volumes",
		func(name, expectedMessage string) {
			obj := map[string]any{"podTemplate": map[string]any{"spec": map[string]any{
				"volumes": []any{map[string]any{"name": name}},
			}}}
			expectCELErrors(spec.validate(celSpecPath, obj), expectedMessage)
		},
		Entry("custom volume", "extra", ""),
		Entry("quorum config volume", "clickhouse-keeper-quorum-config-volume", reservedVolume),
		Entry("data volume", "clickhouse-storage-volume", reservedVolume),
		Entry("config volume", "clickhouse-keeper-config-volume", reservedVolume),
		Entry("TLS volume", "clickhouse-server-tls-volume", reservedVolume),
	)
})
