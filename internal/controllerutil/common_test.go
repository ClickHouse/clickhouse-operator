package controllerutil

import (
	"context"
	"errors"
	"sync"
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
	var allSet SampleStruct

	BeforeEach(func() {
		allSet = SampleStruct{
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
	})

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
	It("should execute all tasks", func(ctx context.Context) {
		result := ExecuteParallel(ctx,
			[]int{1, 2, 3},
			func(_ context.Context, item int) (int, error) {
				return item * 2, nil
			})
		Expect(result).To(Equal(map[int]ExecutionResult[int]{
			1: {Result: 2},
			2: {Result: 4},
			3: {Result: 6},
		}))
	})

	It("should report execution errors", func(ctx context.Context) {
		result := ExecuteParallel(ctx,
			[]int{1, 2, 3},
			func(_ context.Context, item int) (int, error) {
				if item%2 == 0 {
					return 0, errors.New("failed")
				}
				return item * 2, nil
			})
		Expect(result).To(Equal(map[int]ExecutionResult[int]{
			1: {Result: 2},
			2: {Err: errors.New("failed")},
			3: {Result: 6},
		}))
	})

	It("should start task timeout when the task starts", func(ctx context.Context) {
		result := ExecuteParallel(ctx,
			[]int{1, 2},
			func(ctx context.Context, _ int) (bool, error) {
				startedInTime := ctx.Err() == nil
				<-ctx.Done()
				return startedInTime, ctx.Err()
			}, WithParallelism(1), WithTaskTimeout(100*time.Millisecond))
		Expect(result).To(Equal(map[int]ExecutionResult[bool]{
			1: {Result: true, Err: context.DeadlineExceeded},
			2: {Result: true, Err: context.DeadlineExceeded},
		}))
	})

	It("should fail tasks not started before context is done", func(ctx context.Context) {
		errStop := errors.New("stop")
		ctx, cancel := context.WithCancelCause(ctx)
		result := ExecuteParallel(ctx,
			[]int{1, 2, 3},
			func(_ context.Context, item int) (int, error) {
				cancel(errStop)
				return item, nil
			}, WithParallelism(1))
		Expect(result).To(Equal(map[int]ExecutionResult[int]{
			1: {Result: 1},
			2: {Err: errStop},
			3: {Err: errStop},
		}))
	})

	It("should fail a nested call sharing the semaphore of the calling task", func(ctx context.Context) {
		shared := WithParallelism(1)
		result := ExecuteParallel(ctx, []int{1}, func(ctx context.Context, _ int) (map[int]ExecutionResult[int], error) {
			return ExecuteParallel(ctx, []int{2}, func(context.Context, int) (int, error) {
				return 2, nil
			}, shared), nil
		}, shared)
		Expect(result[1].Result).To(Equal(map[int]ExecutionResult[int]{2: {Err: errNestedSemaphore}}))
	}, SpecTimeout(time.Second))

	It("should run a nested call with another semaphore", func(ctx context.Context) {
		result := ExecuteParallel(ctx, []int{1}, func(ctx context.Context, _ int) (map[int]ExecutionResult[int], error) {
			return ExecuteParallel(ctx, []int{2}, func(context.Context, int) (int, error) {
				return 2, nil
			}, WithParallelism(1)), nil
		}, WithParallelism(1))
		Expect(result[1].Result).To(Equal(map[int]ExecutionResult[int]{2: {Result: 2}}))
	}, SpecTimeout(time.Second))

	It("should run at most the semaphore size tasks at once", func(ctx context.Context) {
		var (
			mu            sync.Mutex
			running, peak int
		)

		ExecuteParallel(ctx, []int{1, 2, 3, 4, 5, 6}, func(context.Context, int) (struct{}, error) {
			mu.Lock()
			running++
			peak = max(peak, running)
			mu.Unlock()

			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			running--
			mu.Unlock()

			return struct{}{}, nil
		}, WithParallelism(2))
		Expect(peak).To(BeNumerically("<=", 2))
	}, SpecTimeout(time.Second))
})

var _ = DescribeTable("ParseVersion", func(in, out string, isErr bool) {
	result, err := ParseVersion(in)
	Expect(result).To(Equal(out))
	Expect(err != nil).To(Equal(isErr))
},
	Entry("from keeper mntr", "v26.1.3.52-stable-5549f2acae95c6d627654f50e212a85d059a55f9", "26.1.3.52", false),
	Entry("from --version", "ClickHouse local version 25.4.1.1.", "25.4.1.1", false),
	Entry("exact match", "25.4.1.1", "25.4.1.1", false),
	Entry("error", "command not found: clickhouse", "", true),
	Entry("empty result", "", "", true),
)
