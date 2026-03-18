package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
)

var _ = Describe("validateAdditionalDataVolumeClaimSpecs", func() {
	It("should reject name collision with primary data volume", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{
				Name:      internal.PersistentVolumeName,
				MountPath: "/var/lib/clickhouse/disks/disk1",
				Spec:      corev1.PersistentVolumeClaimSpec{},
			},
		})
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Error()).To(ContainSubstring("collides with primary data volume name"))
	})

	It("should reject duplicate names", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{Name: "disk1", MountPath: "/path1", Spec: corev1.PersistentVolumeClaimSpec{}},
			{Name: "disk1", MountPath: "/path2", Spec: corev1.PersistentVolumeClaimSpec{}},
		})
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Error()).To(ContainSubstring("duplicate name"))
	})

	It("should reject empty name", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{Name: "", MountPath: "/path", Spec: corev1.PersistentVolumeClaimSpec{}},
		})
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Error()).To(ContainSubstring("name must not be empty"))
	})

	It("should accept valid specs with explicit mountPath", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{
				Name:      "disk1",
				MountPath: "/var/lib/clickhouse/disks/disk1",
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
					},
				},
			},
		})
		Expect(errs).To(BeEmpty())
	})

	It("should accept valid specs with default mountPath (empty)", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{
				Name: "disk1",
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
					},
				},
			},
		})
		Expect(errs).To(BeEmpty())
	})

	It("should reject duplicate explicit mountPaths", func() {
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{Name: "disk1", MountPath: "/mnt/data", Spec: corev1.PersistentVolumeClaimSpec{}},
			{Name: "disk2", MountPath: "/mnt/data", Spec: corev1.PersistentVolumeClaimSpec{}},
		})
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Error()).To(ContainSubstring("duplicate mountPath"))
	})

	It("should reject duplicate mountPaths where one is implicit default", func() {
		// disk1 has no mountPath so it defaults to /var/lib/clickhouse/disks/disk1;
		// disk2 explicitly sets the same path — both resolve to the same location.
		errs := validateAdditionalDataVolumeClaimSpecs([]v1alpha1.AdditionalVolumeClaimSpec{
			{Name: "disk1", Spec: corev1.PersistentVolumeClaimSpec{}},
			{Name: "disk2", MountPath: "/var/lib/clickhouse/disks/disk1", Spec: corev1.PersistentVolumeClaimSpec{}},
		})
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Error()).To(ContainSubstring("duplicate mountPath"))
	})
})

var _ = Describe("validateAdditionalDataVolumeClaimSpecsChanges", func() {
	It("should reject adding additionalDataVolumeClaimSpecs after creation", func() {
		err := validateAdditionalDataVolumeClaimSpecsChanges(
			nil,
			[]v1alpha1.AdditionalVolumeClaimSpec{{Name: "disk1", MountPath: "/path", Spec: corev1.PersistentVolumeClaimSpec{}}},
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot be added"))
	})

	It("should reject removing additionalDataVolumeClaimSpecs after creation", func() {
		err := validateAdditionalDataVolumeClaimSpecsChanges(
			[]v1alpha1.AdditionalVolumeClaimSpec{{Name: "disk1", MountPath: "/path", Spec: corev1.PersistentVolumeClaimSpec{}}},
			nil,
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot be removed"))
	})

	It("should reject changing count", func() {
		err := validateAdditionalDataVolumeClaimSpecsChanges(
			[]v1alpha1.AdditionalVolumeClaimSpec{{Name: "disk1", MountPath: "/path", Spec: corev1.PersistentVolumeClaimSpec{}}},
			[]v1alpha1.AdditionalVolumeClaimSpec{
				{Name: "disk1", MountPath: "/path", Spec: corev1.PersistentVolumeClaimSpec{}},
				{Name: "disk2", MountPath: "/path2", Spec: corev1.PersistentVolumeClaimSpec{}},
			},
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("count cannot be changed"))
	})

	It("should allow no change when both empty", func() {
		err := validateAdditionalDataVolumeClaimSpecsChanges(nil, nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should allow same specs", func() {
		specs := []v1alpha1.AdditionalVolumeClaimSpec{
			{Name: "disk1", MountPath: "/path1", Spec: corev1.PersistentVolumeClaimSpec{}},
			{Name: "disk2", MountPath: "/path2", Spec: corev1.PersistentVolumeClaimSpec{}},
		}
		err := validateAdditionalDataVolumeClaimSpecsChanges(specs, specs)
		Expect(err).NotTo(HaveOccurred())
	})
})
