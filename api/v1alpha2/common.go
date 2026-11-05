package v1alpha2

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// LoggerConfig defines server logging configuration.
type LoggerConfig struct {
	// If false then disable all logging to file.
	// +optional
	// +kubebuilder:default:=true
	LogToFile *bool `json:"logToFile,omitempty"`

	// If true, then log in JSON format.
	// +optional
	// +kubebuilder:default:=false
	JSONLogs bool `json:"jsonLogs,omitempty"`

	// Server logger verbosity level.
	// +optional
	// +kubebuilder:validation:Enum:=test;trace;debug;information;notice;warning;error;critical;fatal
	// +kubebuilder:default:=information
	Level string `json:"level,omitempty"`

	// Maximum log file size.
	// +optional
	// +kubebuilder:default:="100M"
	Size string `json:"size,omitempty"`

	// Maximum number of log files to keep.
	// +optional
	// +kubebuilder:default:=10
	Count int64 `json:"count,omitempty"`
}

// NetworkPolicyPolicy controls whether the operator manages a NetworkPolicy for the cluster.
// +kubebuilder:validation:Enum=Enabled;Disabled
type NetworkPolicyPolicy string

const (
	// NetworkPolicyEnabled makes the operator manage a NetworkPolicy restricting ingress to the cluster Pods.
	NetworkPolicyEnabled NetworkPolicyPolicy = "Enabled"
	// NetworkPolicyDisabled disables the managed NetworkPolicy, the operator deletes it if present.
	NetworkPolicyDisabled NetworkPolicyPolicy = "Disabled"
)

// PDBPolicy controls whether PodDisruptionBudgets are created.
// +kubebuilder:validation:Enum=Enabled;Disabled;Ignored
type PDBPolicy string

const (
	// PDBPolicyEnabled enables PodDisruptionBudgets creation by the operator.
	PDBPolicyEnabled PDBPolicy = "Enabled"
	// PDBPolicyDisabled disables PodDisruptionBudgets, operator will delete resource with matching labels.
	PDBPolicyDisabled PDBPolicy = "Disabled"
	// PDBPolicyIgnored ignores PodDisruptionBudgets, operator will not create or delete any PDBs, existing PDBs will be left unchanged.
	PDBPolicyIgnored PDBPolicy = "Ignored"
)

// PodDisruptionBudgetTemplate configures the PDB created for the cluster.
// The structure mirrors policyv1.PodDisruptionBudget: Metadata and Spec are merged onto the
// operator-generated PDB, unset fields keep the operator defaults.
type PodDisruptionBudgetTemplate struct {
	// Policy controls whether the operator creates PodDisruptionBudgets.
	// Defaults to "Enabled" when unset. Set it to "Disabled" to skip PDB creation (e.g. for development environments).
	// +optional
	// +kubebuilder:default:=Enabled
	Policy PDBPolicy `json:"policy,omitempty"`

	// Metadata applied to the PodDisruptionBudgets.
	// Labels and annotations set by the operator take precedence.
	// +optional
	Metadata TemplateMeta `json:"metadata,omitempty"`

	// Spec is merged onto the operator-generated PodDisruptionBudgetSpec.
	// The selector is managed by the operator. At most one of minAvailable or maxUnavailable may be set,
	// setting either replaces the operator default. When neither is set, the operator picks a safe
	// default based on replica count.
	// Nested field documentation is omitted from the CRD, see `kubectl explain poddisruptionbudget.spec`.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.selector)",message="podDisruptionBudget.spec.selector is managed by the operator"
	// +kubebuilder:validation:XValidation:rule="!(has(self.minAvailable) && has(self.maxUnavailable))",message="only one of podDisruptionBudget.spec.minAvailable or podDisruptionBudget.spec.maxUnavailable can be set"
	Spec policyv1.PodDisruptionBudgetSpec `json:"spec,omitempty"`
}

// Enabled returns true if the PodDisruptionBudgets should be created.
func (t *PodDisruptionBudgetTemplate) Enabled() bool {
	return t == nil || t.Policy == PDBPolicyEnabled || t.Policy == ""
}

// Ignored returns true if the PodDisruptionBudgets should be ignored.
func (t *PodDisruptionBudgetTemplate) Ignored() bool {
	return t != nil && t.Policy == PDBPolicyIgnored
}

// Validate validates the PodDisruptionBudgetTemplate configuration.
func (t *PodDisruptionBudgetTemplate) Validate() error {
	if t == nil {
		return nil
	}

	if t.Spec.Selector != nil {
		return errors.New("podDisruptionBudget.spec.selector is managed by the operator")
	}

	if t.Spec.MinAvailable != nil && t.Spec.MaxUnavailable != nil {
		return errors.New("only one of podDisruptionBudget.spec.minAvailable or podDisruptionBudget.spec.maxUnavailable can be set")
	}

	return nil
}

// ApplyOverrides merges the template onto the operator-generated PodDisruptionBudget.
// The operator-generated selector, labels and annotations take precedence over the template.
func (t *PodDisruptionBudgetTemplate) ApplyOverrides(pdb *policyv1.PodDisruptionBudget) error {
	if t == nil {
		return nil
	}

	pdb.Labels = util.MergeMaps(t.Metadata.Labels, pdb.Labels)
	pdb.Annotations = util.MergeMaps(t.Metadata.Annotations, pdb.Annotations)

	base := pdb.Spec.DeepCopy()
	// minAvailable and maxUnavailable are mutually exclusive: either one replaces the operator default.
	if t.Spec.MinAvailable != nil || t.Spec.MaxUnavailable != nil {
		base.MinAvailable = nil
		base.MaxUnavailable = nil
	}

	patch := t.Spec.DeepCopy()
	patch.Selector = nil

	baseJSON, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("marshal pod disruption budget spec: %w", err)
	}

	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal pod disruption budget template spec: %w", err)
	}

	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patchJSON, policyv1.PodDisruptionBudgetSpec{})
	if err != nil {
		return fmt.Errorf("merge pod disruption budget spec: %w", err)
	}

	var merged policyv1.PodDisruptionBudgetSpec
	if err := json.Unmarshal(mergedJSON, &merged); err != nil {
		return fmt.Errorf("unmarshal merged pod disruption budget spec: %w", err)
	}

	merged.Selector = pdb.Spec.Selector
	pdb.Spec = merged

	return nil
}

// PodTemplateSpec describes the template of the cluster's Pods.
// The operator generates a default Pod (server container, probes, volumes, security context, ...)
// and merges this template on top of it using Kubernetes strategic merge patch semantics,
// so every Pod field can be overridden while unset fields keep the operator defaults.
// Cluster-wide pod spreading is configured in `spec.scheduling`; raw `topologySpreadConstraints`
// and `affinity` set here are merged onto the rules generated from it.
type PodTemplateSpec struct {
	// Metadata contains labels and annotations added to the Pods.
	// +optional
	Metadata TemplateMeta `json:"metadata,omitempty"`

	// Spec is merged onto the operator-generated PodSpec using strategic merge patch.
	// Lists merge by their Kubernetes patch merge key: `containers`, `initContainers`, `volumes`
	// and `imagePullSecrets` by name, `topologySpreadConstraints` by topologyKey.
	// The server container is addressed by name: "clickhouse" for ClickHouseCluster, "keeper" for KeeperCluster;
	// set its `image`, `resources`, `env`, `volumeMounts`, probes etc. there.
	// A volume replaces any operator volume with the same name, affinity scheduling term lists are
	// appended to the operator defaults, and a non-nil securityContext, probe or lifecycle fully
	// replaces the corresponding operator default.
	// The `containers` list is optional here, the operator always provides the server container.
	// Nested field documentation is omitted from the CRD, see `kubectl explain pod.spec`.
	// +optional
	Spec corev1.PodSpec `json:"spec,omitempty"`
}

// NamedPodTemplate is a pod template that is referenced by name.
type NamedPodTemplate struct {
	PodTemplateSpec `json:",inline"`

	// Name identifies the template within the cluster's podTemplates list.
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
}

// findPodTemplate returns the template with the given name, or nil if the name is empty or not found.
func findPodTemplate(templates []NamedPodTemplate, name string) *PodTemplateSpec {
	if name == "" {
		return nil
	}

	for i := range templates {
		if templates[i].Name == name {
			return &templates[i].PodTemplateSpec
		}
	}

	return nil
}

// SchedulingSpec configures how the operator spreads the cluster's Pods across nodes and topology domains.
// The operator turns these settings into TopologySpreadConstraints and pod (anti-)affinity rules on every Pod,
// so they apply consistently regardless of which pod template a Pod uses.
// +kubebuilder:validation:XValidation:rule="!has(self.minDomains) || has(self.zoneKey)",message="minDomains requires zoneKey"
type SchedulingSpec struct {
	// ZoneKey is the key of node labels.
	// Nodes that have a label with this key and identical values are considered to be in the same topology domain,
	// e.g. the same availability zone for "topology.kubernetes.io/zone" or the same node for "kubernetes.io/hostname".
	// Set it to balance pods holding the same data (replicas of one shard for ClickHouse, all replicas for Keeper)
	// across domains: a required TopologySpreadConstraint (maxSkew 1, DoNotSchedule) plus a preferred PodAntiAffinity rule.
	// Pods of different shards may share a domain, so multiple pods per domain are allowed.
	// With fewer domains than replicas, several replicas share a domain, still evenly balanced.
	// For ClickHouseCluster with keeperClusterRef, pods additionally prefer domains hosting the Keeper pods they use.
	// Recommended to be set to "topology.kubernetes.io/zone".
	// +optional
	ZoneKey *string `json:"zoneKey,omitempty"`

	// MinDomains sets the minDomains field on the TopologySpreadConstraint generated by ZoneKey.
	// The Kubernetes scheduler counts only zones that already have eligible nodes as topology domains.
	// With demand-driven autoscalers (e.g. Karpenter, Cluster Autoscaler) this means that if nodes
	// exist in only two AZs at scheduling time, a maxSkew:1/DoNotSchedule constraint is satisfied with
	// a 2/1 split and the autoscaler never provisions a node in the third AZ.
	// Setting MinDomains to the expected number of zones (typically 3) tells the scheduler to
	// treat that many zones as domains regardless of whether nodes exist in all of them, forcing the
	// autoscaler to provision into the missing zone.
	// Requires ZoneKey to be set. Omit (nil) to preserve the default behaviour
	// of the scheduler counting only zones with existing eligible nodes.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinDomains *int32 `json:"minDomains,omitempty"`

	// NodeHostnameKey is the key of node labels.
	// Nodes that have a label with this key and identical values are considered to be on the same node.
	// Set it to enforce exclusive node occupancy: a required PodAntiAffinity rule across all pods of the cluster,
	// at most one pod per node regardless of shard. Requires at least as many nodes as pods;
	// with fewer nodes the excess pods stay Pending.
	// To spread replicas of the same shard across nodes while packing several pods per node,
	// set ZoneKey to "kubernetes.io/hostname" instead.
	// Recommended to be set to "kubernetes.io/hostname".
	// +optional
	NodeHostnameKey *string `json:"nodeHostnameKey,omitempty"`
}

// NetworkSpec defines the network configuration shared by all cluster kinds.
type NetworkSpec struct {
	// ClusterDomain is the Kubernetes cluster domain suffix used for DNS resolution.
	// +optional
	// +kubebuilder:default:="cluster.local"
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// Policy controls whether the operator manages a NetworkPolicy restricting ingress to the cluster Pods.
	// The managed policy covers cluster-internal traffic only: connectivity between the cluster Pods,
	// access from the operator and, for KeeperCluster, access from the ClickHouse clusters referencing it.
	// Any other ingress, including client connections and metrics scraping, requires user-defined NetworkPolicies.
	// +optional
	// +kubebuilder:default:=Disabled
	Policy NetworkPolicyPolicy `json:"policy,omitempty"`

	// TLS configures secure endpoints and certificate verification for the cluster server.
	// +optional
	// +kubebuilder:default:={}
	TLS TLSSpec `json:"tls,omitempty"`
}

// PolicyEnabled returns true if the operator should manage the cluster NetworkPolicy.
func (s *NetworkSpec) PolicyEnabled() bool {
	return s != nil && s.Policy == NetworkPolicyEnabled
}

// TLSMode controls whether the server exposes secure and plain text endpoints.
// +kubebuilder:validation:Enum=Disabled;Enabled;Required
type TLSMode string

const (
	// TLSModeDisabled is the default mode: only plain text ports are opened.
	TLSModeDisabled TLSMode = "Disabled"
	// TLSModeEnabled opens secure ports alongside the plain text ones.
	TLSModeEnabled TLSMode = "Enabled"
	// TLSModeRequired opens secure ports only, TLS is enforced for all connections.
	TLSModeRequired TLSMode = "Required"
)

// TLSSpec defines cluster TLS configuration.
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode == 'Disabled' || has(self.serverCertSecret)",message="serverCertSecret must be specified when TLS is enabled"
type TLSSpec struct {
	// Mode controls whether TLS is enabled and whether it is enforced for all connections.
	// "Enabled" opens secure ports alongside the plain text ones, "Required" disables the plain text ports.
	// +optional
	// +kubebuilder:default:=Disabled
	Mode TLSMode `json:"mode,omitempty"`

	// ServerCertSecret is a reference to a TLS Secret containing the server certificate.
	// It is expected that the Secret has the same structure as certificates generated by cert-manager,
	// with the certificate and private key stored under "tls.crt" and "tls.key" keys respectively.
	// Required when mode is not "Disabled".
	// +optional
	ServerCertSecret *corev1.LocalObjectReference `json:"serverCertSecret,omitempty"`

	// CABundle selects a CA bundle used to verify peer certificates.
	// If empty, the system trusted CA bundle is used.
	// +optional
	CABundle *CABundleSource `json:"caBundle,omitempty"`
}

// Enabled returns true if the secure ports should be opened.
func (s *TLSSpec) Enabled() bool {
	return s != nil && (s.Mode == TLSModeEnabled || s.Mode == TLSModeRequired)
}

// Required returns true if TLS must be enforced for all connections.
func (s *TLSSpec) Required() bool {
	return s != nil && s.Mode == TLSModeRequired
}

// Validate validates the TLSSpec configuration.
func (s *TLSSpec) Validate() error {
	if s == nil {
		return nil
	}

	if s.Enabled() && (s.ServerCertSecret == nil || s.ServerCertSecret.Name == "") {
		return errors.New("serverCertSecret must be specified when TLS is enabled")
	}

	return s.CABundle.Validate()
}

// CABundleSource selects the source of a CA bundle, exactly one of secret or configMap must be set.
// +kubebuilder:validation:XValidation:rule="has(self.secret) != has(self.configMap)",message="exactly one of secret or configMap must be set"
type CABundleSource struct {
	// Select the CA bundle from a Secret key.
	// +optional
	Secret *CABundleKeySelector `json:"secret,omitempty"`
	// Select the CA bundle from a ConfigMap key.
	// +optional
	ConfigMap *CABundleKeySelector `json:"configMap,omitempty"`
}

// Validate validates the CABundleSource configuration.
func (s *CABundleSource) Validate() error {
	if s == nil {
		return nil
	}

	if (s.Secret != nil) == (s.ConfigMap != nil) {
		return errors.New("exactly one of caBundle secret or configMap must be set")
	}

	return nil
}

// CABundleKeySelector selects a key holding a CA bundle from an object in the cluster's namespace.
type CABundleKeySelector struct {
	// The name of the object in the cluster's namespace to select from.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// The key to select from. Must be a valid key.
	// +kubebuilder:default:="ca.crt"
	// +optional
	Key string `json:"key,omitempty"`
}

// ExternalSecretPolicy controls how the operator treats the external secret's content.
// +kubebuilder:validation:Enum=Observe;Manage
type ExternalSecretPolicy string

const (
	// ExternalSecretPolicyObserve is the default policy: the operator reads and validates the secret;
	// reconciliation is blocked if any required key is absent.
	// Missing required keys and their expected formats are reported via the ExternalSecretValid status condition at runtime.
	ExternalSecretPolicyObserve ExternalSecretPolicy = "Observe"
	// ExternalSecretPolicyManage is the policy where the operator fills in any missing required keys by generating
	// values for them. The secret is updated but never owned or deleted by the operator.
	ExternalSecretPolicyManage ExternalSecretPolicy = "Manage"
)

// ExternalSecret is a reference to a Secret in the same namespace.
type ExternalSecret struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Policy controls how the operator treats the secret's content.
	// Observe (default): blocks reconciliation if any required key is missing.
	// Manage: generates missing required keys into the existing secret.
	// +optional
	// +kubebuilder:default:=Observe
	Policy ExternalSecretPolicy `json:"policy,omitempty"`
}

// SecretKeySelector selects a key of a Secret.
type SecretKeySelector struct {
	// The name of the secret in the cluster's namespace to select from.
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// The key of the secret to select from.  Must be a valid secret key.
	// +kubebuilder:validation:Required
	Key string `json:"key,omitempty"`
}

// DefaultPasswordSelector selects the source for the default user's password.
type DefaultPasswordSelector struct {
	// Type of the provided password. Consider documentation for possible values https://clickhouse.com/docs/operations/settings/settings-users#user-namepassword
	// +kubebuilder:default:=password
	PasswordType string `json:"passwordType,omitempty"`
	// Select password value from a Secret key
	// +kubebuilder:validation:Required
	Secret *SecretKeySelector `json:"secret,omitempty"`
}

// Validate validates the DefaultPasswordSelector configuration.
func (s *DefaultPasswordSelector) Validate() error {
	if s == nil {
		return nil
	}

	if s.Secret == nil {
		return errors.New("default user password secret must be specified")
	}

	if s.Secret.Name == "" || s.Secret.Key == "" {
		return errors.New("default user secret name and key must be specified")
	}

	return nil
}

const (
	maxResourceNameLen = 63
	hashLen            = 4
)

// resourceName normalizes name and appends suffix, truncating the prefix to a max limit.
func resourceName(name, suffix string) string {
	var fullName string
	switch {
	case len(suffix) == 0:
		fullName = name
	case len(name)+len(suffix)+1 <= maxResourceNameLen:
		fullName = name + "-" + suffix
	default:
		hash := util.Sha256Hash([]byte(name))[:hashLen]
		name = name[:maxResourceNameLen-len(suffix)-hashLen-2]
		fullName = fmt.Sprintf("%s-%s-%s", name, hash, suffix)
	}

	return strings.ReplaceAll(fullName, ".", "-")
}

// formatPodHostname returns hostname for the first pod in the StatefulSet.
func formatPodHostname(stsName, serviceName, namespace, domain string) string {
	if domain == "" {
		domain = DefaultClusterDomain
	}

	return fmt.Sprintf("%s-0.%s.%s.svc.%s", stsName, serviceName, namespace, domain)
}

func formatServiceHostname(serviceName, namespace, domain string) string {
	if domain == "" {
		domain = DefaultClusterDomain
	}

	return fmt.Sprintf("%s.%s.svc.%s", serviceName, namespace, domain)
}

// TemplateMeta defines supported metadata settings for template objects.
type TemplateMeta struct {
	// Labels are labels applied to the template objects.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are annotations applied to the template objects.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ImageVersionEntry maps a ClickHouse server container image to the ClickHouse version it contains.
// +kubebuilder:validation:XValidation:rule="self.image.contains('@') || (self.image.matches(':[^/]+$') && !self.image.endsWith(':latest') && (!self.image.matches(':[0-9][^/]*$') || self.image.matches(':[0-9]+[.][0-9]+[^/]*$')))",message="image must be pinned by a digest or by a tag other than 'latest'; a numeric tag must start with major.minor (e.g. 25.8)"
type ImageVersionEntry struct {
	// Image is the full image reference, compared by exact string match with the ClickHouse server container image
	// (e.g. "docker.io/clickhouse/clickhouse-server:25.8.2.29").
	// Must be pinned by a digest or by a tag other than `latest`; a tag starting with a digit must start with
	// major.minor (e.g. "25.8", "25.8.2.29", "25.8-alpine"), so major-only tags like "25" are rejected.
	// Tags are checked by naming heuristics only and can still be moved by the registry:
	// a digest ("repo@sha256:...") is the only reference that is guaranteed to be immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Image string `json:"image"`

	// Version is the ClickHouse version contained in the image (e.g. "25.8.2.29").
	// +kubebuilder:validation:Pattern=`^\d+\.\d+(\.\d+){0,2}$`
	Version string `json:"version"`
}

// NamedTemplateMeta defines supported metadata settings for template objects that require a name.
type NamedTemplateMeta struct {
	// Name is the resource identifier.
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Labels are labels applied to the template objects.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are annotations applied to the template objects.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DataVolumeClaimTemplate is the template of the per-replica data PersistentVolumeClaim.
// The claim name is fixed by the operator.
type DataVolumeClaimTemplate struct {
	// Metadata applied to the data volume claim.
	// +optional
	Metadata TemplateMeta `json:"metadata,omitempty"`

	// Spec defines the desired characteristics of the data volume.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#persistentvolumeclaims
	// Nested field documentation is omitted from the CRD, see `kubectl explain persistentvolumeclaim.spec`.
	// +optional
	Spec corev1.PersistentVolumeClaimSpec `json:"spec,omitempty"`
}

// NamedDataVolumeClaimTemplate is a data volume claim template that is referenced by name.
type NamedDataVolumeClaimTemplate struct {
	DataVolumeClaimTemplate `json:",inline"`

	// Name identifies the template within the cluster's dataVolumeClaimTemplates list.
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
}

// findDataVolumeClaimTemplate returns the template with the given name, or nil if the name is empty or not found.
func findDataVolumeClaimTemplate(templates []NamedDataVolumeClaimTemplate, name string) *DataVolumeClaimTemplate {
	if name == "" {
		return nil
	}

	for i := range templates {
		if templates[i].Name == name {
			return &templates[i].DataVolumeClaimTemplate
		}
	}

	return nil
}

// NamedVolumeClaimTemplateSet is a named group of additional per-pod PVC templates for JBOD / multi-disk storage.
type NamedVolumeClaimTemplateSet struct {
	// Name identifies the set within the cluster's additionalVolumeClaimTemplateSets list.
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Templates of the additional per-pod PVCs.
	// Each entry is propagated in StatefulSet volumeClaimTemplate, mounted at `/var/lib/clickhouse/disks/<name>` and
	// added to the generated JBOD storage policy.
	// The set of disks of a replica is fixed at its creation.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:XValidation:rule="self.all(t, !has(t.metadata) || !t.metadata.name.endsWith('-encrypted'))",message="names ending in '-encrypted' are reserved for generated encrypted disk names"
	Templates []PersistentVolumeClaimTemplate `json:"templates"`
}

// findVolumeClaimTemplateSet returns the templates of the set with the given name, or nil if the name is empty or
// not found.
func findVolumeClaimTemplateSet(sets []NamedVolumeClaimTemplateSet, name string) []PersistentVolumeClaimTemplate {
	if name == "" {
		return nil
	}

	for i := range sets {
		if sets[i].Name == name {
			return sets[i].Templates
		}
	}

	return nil
}

// PersistentVolumeClaimTemplate is a named template for a per-replica PersistentVolumeClaim.
type PersistentVolumeClaimTemplate struct {
	// Metadata template of the volume claim.
	NamedTemplateMeta `json:"metadata,omitempty"`

	// Spec defines the desired characteristics of a volume requested by a pod author.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#persistentvolumeclaims
	// Nested field documentation is omitted from the CRD, see `kubectl explain persistentvolumeclaim.spec`.
	Spec corev1.PersistentVolumeClaimSpec `json:"spec,omitempty" protobuf:"bytes,2,opt,name=spec"`
}
