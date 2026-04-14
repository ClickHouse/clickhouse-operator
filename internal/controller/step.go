package controller

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// StepResult controls reconciliation flow after a step completes without error.
type StepResult struct {
	// RequeueAfter requests the reconciler to requeue after the given duration.
	RequeueAfter time.Duration
	// Blocked prevents subsequent sequential steps from running in this reconciliation.
	Blocked bool
}

// IsZero returns true if the step result requires no requeue and is not blocked.
func (s StepResult) IsZero() bool {
	return s.RequeueAfter == 0 && !s.Blocked
}

// StepContinue indicates the step completed successfully with no further action.
func StepContinue() StepResult { return StepResult{} }

// StepRequeue indicates the step completed but the cluster needs more work on the next reconcile.
func StepRequeue(d time.Duration) StepResult { return StepResult{RequeueAfter: d} }

// StepBlocked indicates the step is waiting for an external dependency.
// Subsequent sequential steps will be skipped; steps marked Always still run.
func StepBlocked(d time.Duration) StepResult { return StepResult{Blocked: true, RequeueAfter: d} }

// ReconcileStep defines a single step in the reconciliation pipeline.
type ReconcileStep struct {
	// Name is used for logging.
	Name string
	// Fn is the step function.
	Fn func(context.Context, controllerutil.Logger) (StepResult, error)
	// Always makes the step run even when the pipeline is blocked by a prior step.
	Always bool
}

// RunSteps executes reconciliation steps sequentially with flow control.
// Blocked steps cause non-Always successors to be skipped.
// Returns the accumulated ctrl.Result (minimum RequeueAfter across all steps).
// On the first error, RunSteps returns immediately with that error.
func RunSteps(ctx context.Context, log controllerutil.Logger, steps []ReconcileStep) (ctrl.Result, error) {
	var (
		requeueAfter time.Duration
		blocked      bool
	)

	for _, step := range steps {
		stepLog := log.With("step", step.Name)

		if blocked && !step.Always {
			stepLog.Debug("skipping step — pipeline blocked")
			continue
		}

		stepLog.Debug("starting reconcile step")

		sr, err := step.Fn(ctx, stepLog)
		if err != nil {
			return ctrl.Result{}, err
		}

		if sr.Blocked {
			blocked = true
		}

		if sr.RequeueAfter > 0 {
			if requeueAfter == 0 || sr.RequeueAfter < requeueAfter {
				requeueAfter = sr.RequeueAfter
			}
		}

		stepLog.Debug("reconcile step done")
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
