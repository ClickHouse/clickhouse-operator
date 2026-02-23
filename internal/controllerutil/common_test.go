package controllerutil

import (
	"errors"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
)

type InternalStruct struct {
	String string
	Number int
}

type SampleStruct struct {
	String  string
	Number  int
	Pointer *int
	Array   []int
	Map     map[int]string
	Struct  InternalStruct
	BoolPtr *bool
}

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Utils Suite")
}

var _ = Describe("ApplyDefault", func() {
	allSet := SampleStruct{
		String:  "default",
		Number:  7,
		Pointer: new(42),
		Array:   []int{1, 2, 3},
		Map:     map[int]string{1: "a", 2: "b", 3: "c"},
		Struct: InternalStruct{
			String: "internal",
			Number: 14,
		},
	}

	It("should default all fields", func() {
		source := SampleStruct{}
		Expect(ApplyDefault(&source, allSet)).To(Succeed())
		Expect(source).To(Equal(allSet))
	})

	It("should update nothing", func() {
		source := allSet
		defaults := SampleStruct{
			String: "another",
			Map:    map[int]string{1: "diff"},
			Struct: InternalStruct{
				Number: 77,
			},
		}
		Expect(ApplyDefault(&source, defaults)).To(Succeed())
		Expect(source).To(Equal(allSet))
	})

	It("should default empty fields", func() {
		source := allSet
		defaults := allSet

		source.String = ""
		source.Struct.Number = 100
		source.Map[7] = "custom"

		Expect(ApplyDefault(&source, defaults)).To(Succeed())
		Expect(source.String).To(Equal(allSet.String))
		Expect(source.Struct.Number).To(Equal(100))
		Expect(source.Map[7]).To(Equal("custom"))
	})

	It("should set bool pointer if it is nil", func() {
		source := SampleStruct{}
		defaults := SampleStruct{
			BoolPtr: new(true),
		}
		Expect(ApplyDefault(&source, defaults)).To(Succeed())
		Expect(*source.BoolPtr).To(BeTrue())
	})

	It("should not update bool pointer if it is not nil", func() {
		source := SampleStruct{
			BoolPtr: new(false),
		}
		defaults := SampleStruct{
			BoolPtr: new(true),
		}
		Expect(ApplyDefault(&source, defaults)).To(Succeed())
		Expect(*source.BoolPtr).To(BeFalse())
	})
})

var _ = Describe("UpdateResult", func() {
	It("should assign to empty", func() {
		result := ctrl.Result{}
		update := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, &update)
		Expect(result.RequeueAfter).To(Equal(update.RequeueAfter))
	})

	It("should ignore empty update", func() {
		result := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, nil)
		Expect(result).To(Equal(ctrl.Result{RequeueAfter: time.Second}))
	})

	It("should update to sooner requeue", func() {
		result := ctrl.Result{RequeueAfter: time.Minute}
		update := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, &update)
		Expect(result.RequeueAfter).To(Equal(time.Second))
	})

	It("should keep sonner requeue", func() {
		result := ctrl.Result{RequeueAfter: time.Second}
		update := ctrl.Result{RequeueAfter: time.Minute}
		UpdateResult(&result, &update)
		Expect(result.RequeueAfter).To(Equal(time.Second))
	})
})

var _ = Describe("ExecuteParallel", func() {
	It("should execute all tasks", func() {
		result := ExecuteParallel(
			[]int{1, 2, 3},
			func(item int) (int, int, error) {
				return item, item * 2, nil
			})
		Expect(result).To(Equal(map[int]ExecutionResult[int, int]{
			1: {Result: 2},
			2: {Result: 4},
			3: {Result: 6},
		}))
	})

	It("should report execution errors", func() {
		result := ExecuteParallel(
			[]int{1, 2, 3},
			func(item int) (int, int, error) {
				if item%2 == 0 {
					return item, 0, errors.New("failed")
				}
				return item, item * 2, nil
			})
		Expect(result).To(Equal(map[int]ExecutionResult[int, int]{
			1: {Result: 2},
			2: {Err: errors.New("failed")},
			3: {Result: 6},
		}))
	})
})
