package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("RunSteps", func() {
	log := controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	step := func(name string, sr StepResult) ReconcileStep {
		return ReconcileStep{
			Name: name,
			Fn:   func(context.Context, controllerutil.Logger) (StepResult, error) { return sr, nil },
		}
	}

	It("should return the minimum RequeueAfter across steps", func(ctx context.Context) {
		result, err := RunSteps(ctx, log, []ReconcileStep{
			step("a", StepRequeue(30*time.Second)),
			step("b", StepRequeue(5*time.Second)),
			step("c", StepContinue()),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(5 * time.Second))
	})

	It("should skip non-Always steps after a blocked step", func(ctx context.Context) {
		var ran []string

		record := func(name string, sr StepResult, always bool) ReconcileStep {
			return ReconcileStep{
				Name: name,
				Fn: func(context.Context, controllerutil.Logger) (StepResult, error) {
					ran = append(ran, name)
					return sr, nil
				},
				Always: always,
			}
		}

		result, err := RunSteps(ctx, log, []ReconcileStep{
			record("blocking", StepBlocked(RequeueProbePoll), false),
			record("skipped", StepContinue(), false),
			record("always", StepContinue(), true),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran).To(Equal([]string{"blocking", "always"}))
		Expect(result.RequeueAfter).To(Equal(RequeueProbePoll))
	})

	It("should guarantee a wake-up for a blocked step without RequeueAfter", func(ctx context.Context) {
		result, err := RunSteps(ctx, log, []ReconcileStep{
			step("blocking", StepResult{Blocked: true}),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(RequeueProbePoll))
	})

	It("should stop on the first error", func(ctx context.Context) {
		var ran []string

		_, err := RunSteps(ctx, log, []ReconcileStep{
			step("ok", StepContinue()),
			{
				Name: "failing",
				Fn: func(context.Context, controllerutil.Logger) (StepResult, error) {
					ran = append(ran, "failing")
					return StepResult{}, context.DeadlineExceeded
				},
			},
			{
				Name: "unreached",
				Fn: func(context.Context, controllerutil.Logger) (StepResult, error) {
					ran = append(ran, "unreached")
					return StepContinue(), nil
				},
			},
		})
		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(ran).To(Equal([]string{"failing"}))
	})
})
