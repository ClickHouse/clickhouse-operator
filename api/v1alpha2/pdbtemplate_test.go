package v1alpha2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var _ = Describe("PodDisruptionBudgetTemplate", func() {
	operatorPDB := func() *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"app": "ch"},
				Annotations: map[string]string{"owner": "operator"},
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ch"}},
				MaxUnavailable: new(intstr.FromInt32(1)),
			},
		}
	}

	It("should keep operator defaults for a nil template", func() {
		var tmpl *PodDisruptionBudgetTemplate

		pdb := operatorPDB()
		Expect(tmpl.ApplyOverrides(pdb)).To(Succeed())
		Expect(pdb).To(Equal(operatorPDB()))
		Expect(tmpl.Enabled()).To(BeTrue())
	})

	It("should replace maxUnavailable default with minAvailable", func() {
		tmpl := &PodDisruptionBudgetTemplate{Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable:               new(intstr.FromString("50%")),
			UnhealthyPodEvictionPolicy: new(policyv1.AlwaysAllow),
		}}
		pdb := operatorPDB()
		Expect(tmpl.ApplyOverrides(pdb)).To(Succeed())
		Expect(pdb.Spec.MaxUnavailable).To(BeNil())
		Expect(*pdb.Spec.MinAvailable).To(Equal(intstr.FromString("50%")))
		Expect(*pdb.Spec.UnhealthyPodEvictionPolicy).To(Equal(policyv1.AlwaysAllow))
		Expect(pdb.Spec.Selector).To(Equal(operatorPDB().Spec.Selector))
	})

	It("should keep the operator selector, labels and annotations", func() {
		tmpl := &PodDisruptionBudgetTemplate{
			Metadata: TemplateMeta{
				Labels:      map[string]string{"app": "other", "team": "db"},
				Annotations: map[string]string{"owner": "user", "note": "x"},
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}},
			},
		}
		pdb := operatorPDB()
		Expect(tmpl.ApplyOverrides(pdb)).To(Succeed())
		Expect(pdb.Labels).To(Equal(map[string]string{"app": "ch", "team": "db"}))
		Expect(pdb.Annotations).To(Equal(map[string]string{"owner": "operator", "note": "x"}))
		Expect(pdb.Spec.Selector).To(Equal(operatorPDB().Spec.Selector))
		Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromInt32(1)))
	})

	It("should reject a selector and both minAvailable and maxUnavailable", func() {
		Expect((&PodDisruptionBudgetTemplate{Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{},
		}}).Validate()).To(MatchError(ContainSubstring("selector")))
		Expect((&PodDisruptionBudgetTemplate{Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable:   new(intstr.FromInt32(1)),
			MaxUnavailable: new(intstr.FromInt32(1)),
		}}).Validate()).To(MatchError(ContainSubstring("only one of")))
	})
})
