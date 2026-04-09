package keeper

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/randfill"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("ServerRevision", func() {
	var (
		baseCR          *v1.KeeperCluster
		baseCfgRevision string
		baseStsRevision string
	)

	BeforeEach(func() {
		var err error

		baseCR = &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
			},
		}

		baseCfgRevision, err = getConfigurationRevision(baseCR)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseCfgRevision).ToNot(BeEmpty())

		baseStsRevision, err = getStatefulSetRevision(baseCR)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseStsRevision).ToNot(BeEmpty())
	})

	It("should not change config revision if only replica count changes", func() {
		cr := baseCR.DeepCopy()
		cr.Spec.Replicas = new(int32(3))
		cfgRevisionUpdated, err := getConfigurationRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseCfgRevision).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).To(Equal(baseCfgRevision), "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(baseStsRevision), "StatefulSet config revision shouldn't depend on replica count")
	})

	It("should not change sts revision if only config changes", func() {
		cr := baseCR.DeepCopy()
		cr.Spec.Settings.Logger.Level = "warning"
		cfgRevisionUpdated, err := getConfigurationRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfgRevisionUpdated).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).ToNot(Equal(baseCfgRevision), "configuration change should update config revision")

		stsRevisionUpdated, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(baseStsRevision), "StatefulSet config revision shouldn't change with config")
	})
})

var _ = Describe("ExtraConfig", func() {
	It("should add extra config as a separate ConfigMap key", func() {
		cr := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				Settings: v1.KeeperSettings{
					ExtraConfig: runtime.RawExtension{Raw: []byte(`{"keeper_server": {"coordination_settings": {"quorum_reads": true}}}`)},
				},
			},
		}
		data, err := generateConfigForSingleReplica(cr, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(HaveKey(ConfigFileName))
		Expect(data).To(HaveKey(ExtraConfigFileName))
		Expect(data[ExtraConfigFileName]).To(ContainSubstring("quorum_reads"))
	})

	It("should not include extra config key when empty", func() {
		cr := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec:       v1.KeeperClusterSpec{Replicas: new(int32(1))},
		}
		data, err := generateConfigForSingleReplica(cr, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(HaveKey(ConfigFileName))
		Expect(data).ToNot(HaveKey(ExtraConfigFileName))
	})
})

var _ = Describe("templatePodDisruptionBudget", func() {
	var cr *v1.KeeperCluster

	BeforeEach(func() {
		cr = &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "default",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(3)),
			},
		}
	})

	It("should default to maxUnavailable=replicas/2 for 3-node cluster", func() {
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1)) // 3/2 = 1
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should default to maxUnavailable=replicas/2 for 5-node cluster", func() {
		cr.Spec.Replicas = new(int32(5))
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(2)) // 5/2 = 2
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom maxUnavailable", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MaxUnavailable: new(intstr.FromInt32(2)),
		}
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom minAvailable", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(2)),
		}
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
		Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MaxUnavailable).To(BeNil())
	})

	It("should support percentage-based values", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MaxUnavailable: new(intstr.FromString("50%")),
		}
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.String()).To(Equal("50%"))
	})

	It("should set correct name, labels, and selector", func() {
		cr.Spec.Labels = map[string]string{"env": "test"}
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Name).To(Equal("test-keeper"))
		Expect(pdb.Namespace).To(Equal("default"))
		Expect(pdb.Labels).To(HaveKeyWithValue("env", "test"))
		Expect(pdb.Spec.Selector.MatchLabels).To(HaveKeyWithValue(controllerutil.LabelAppKey, "test-keeper"))
	})

	It("should set unhealthyPodEvictionPolicy when specified", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			UnhealthyPodEvictionPolicy: new(policyv1.AlwaysAllow),
		}
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.UnhealthyPodEvictionPolicy).NotTo(BeNil())
		Expect(*pdb.Spec.UnhealthyPodEvictionPolicy).To(Equal(policyv1.AlwaysAllow))
	})

	It("should not set unhealthyPodEvictionPolicy when not specified", func() {
		pdb := templatePodDisruptionBudget(cr)

		Expect(pdb.Spec.UnhealthyPodEvictionPolicy).To(BeNil())
	})
})

var _ = Describe("getStatefulSetRevision", func() {
	It("should not depend on data disk spec", func() {
		cr := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
		}

		rev, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(rev).ToNot(BeEmpty())

		cr.Spec.DataVolumeClaimSpec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("20Gi")
		rev2, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())

		Expect(rev2).To(Equal(rev), "StatefulSet revision should not change when data disk spec changes")
	})
})

func FuzzClusterSpec(f *testing.F) {
	// Manually added cases
	f.Add([]byte("2"))

	f.Fuzz(func(t *testing.T, data []byte) {
		fill := testutil.NewSpecFiller(data)
		cr := newKeeperCluster(fill)
		id := v1.KeeperReplicaID(1)

		crBefore := cr.DeepCopy()

		stsFirst, err1 := templateStatefulSet(cr, id)
		if diff := cmp.Diff(crBefore.Spec, cr.Spec); diff != "" {
			t.Errorf("ClusterSpec mutated:\n%s", diff)
		}

		stsSecond, err2 := templateStatefulSet(cr, id)
		if diff := cmp.Diff(crBefore.Spec, cr.Spec); diff != "" {
			t.Errorf("ClusterSpec mutated:\n%s", diff)
		}

		if err1 == nil {
			if diff := cmp.Diff(stsFirst, stsSecond); diff != "" {
				t.Errorf("result differs:\n%s", diff)
			}
		} else {
			if err1.Error() != err2.Error() {
				t.Errorf("errors differ: %v vs %v", err1, err2)
			}
		}
	})
}

func newKeeperCluster(f *randfill.Filler) *v1.KeeperCluster {
	cr := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Labels: map[string]string{
				"app": "clickhouse-operator",
			},
			Annotations: map[string]string{
				"annotation1": "value1",
			},
		},
	}
	f.Fill(&cr.Spec)
	cr.Spec.WithDefaults()

	return cr
}
