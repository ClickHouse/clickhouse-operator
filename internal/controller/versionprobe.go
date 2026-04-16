package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	DefaultProbeCPULimit      = "1"
	DefaultProbeCPURequest    = "250m"
	DefaultProbeMemoryLimit   = "1Gi"
	DefaultProbeMemoryRequest = "256Mi"
)

// VersionProbeConfig holds parameters for the version probe Job.
type VersionProbeConfig struct {
	// Name of the binary to run.
	Binary string
	// Labels to apply to the Job, inherited from the cluster spec.
	Labels map[string]string
	// Annotations to apply to the Job, inherited from the cluster spec.
	Annotations map[string]string
	// PodTemplate to apply to the Job, inherited from the cluster spec.
	PodTemplate v1.PodTemplateSpec
	// ContainerTemplate to apply to the Job, inherited from the cluster spec.
	ContainerTemplate v1.ContainerTemplateSpec
	// VersionProbe is the user-provided override for the version probe Job.
	VersionProbe *v1.VersionProbeTemplate
}

// VersionProbeResult holds the outcome of a version probe reconciliation.
type VersionProbeResult struct {
	// Version is the detected version string, empty if not yet available.
	Version string
	// Pending is true when the Job is still running or being created.
	Pending bool
	// Err if version probe failed it contains the error.
	Err error
}

// Completed returns true if probe completed successfully with a detected version, false otherwise.
func (r *VersionProbeResult) Completed() bool {
	return r.Err == nil && !r.Pending && r.Version != ""
}

// VersionProbe manages a one-time Job to detect the version from a container image.
// Returns the version string when available, or empty string if the Job is pending/running.
func (rm *ResourceManager) VersionProbe(
	ctx context.Context,
	log controllerutil.Logger,
	cfg VersionProbeConfig,
) (VersionProbeResult, error) {
	job, err := rm.buildVersionProbeJob(cfg)
	if err != nil {
		return VersionProbeResult{}, fmt.Errorf("build version probe job: %w", err)
	}

	rm.cleanupVersionProbeJobs(ctx, log, &job)

	cli := rm.ctrl.GetClient()
	log = log.With("job", job.Name)

	var existingJob batchv1.Job
	if err = cli.Get(ctx, types.NamespacedName{Namespace: rm.owner.GetNamespace(), Name: job.Name}, &existingJob); err != nil {
		if !k8serrors.IsNotFound(err) {
			return VersionProbeResult{}, fmt.Errorf("get version probe job: %w", err)
		}

		log.Debug("creating version probe job")

		if err = rm.Create(ctx, &job, v1.EventActionVersionCheck); err != nil {
			return VersionProbeResult{}, fmt.Errorf("create version probe job: %w", err)
		}

		return VersionProbeResult{Pending: true}, nil
	}

	if c, ok := getJobCondition(&existingJob, batchv1.JobFailed); ok && c.Status == corev1.ConditionTrue {
		log.Warn("version probe job failed")

		// Recreate the failed job with the same image only if spec changed
		if controllerutil.GetSpecHashFromObject(&job) != controllerutil.GetSpecHashFromObject(&existingJob) {
			log.Debug("spec changed, deleting failed version probe job for recreation")

			if delErr := rm.Delete(
				ctx,
				&existingJob,
				v1.EventActionVersionCheck,
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			); delErr != nil {
				return VersionProbeResult{}, fmt.Errorf("delete failed version probe job: %w", delErr)
			}

			return VersionProbeResult{Pending: true}, nil
		}

		return VersionProbeResult{Err: errors.New(c.Message)}, nil
	}

	if c, ok := getJobCondition(&existingJob, batchv1.JobComplete); !ok || c.Status != corev1.ConditionTrue {
		log.Debug("version probe has not completed yet")
		return VersionProbeResult{Pending: true}, nil
	}

	version, err := readVersionFromJob(ctx, log, cli, &existingJob)
	if err != nil {
		log.Warn("failed to read version from completed job, retrying", "error", err)
		return VersionProbeResult{Err: err}, nil
	}

	return VersionProbeResult{Version: version}, nil
}

// GetVersionSyncCondition evaluates the VersionInSync condition based on the probe result and replica versions.
// Returns current condition and optional EventSpec that should be recorded if condition Status changed.
func GetVersionSyncCondition(
	probe VersionProbeResult,
	replicaVersions map[string]string,
	isUpdating bool,
) (metav1.Condition, []EventSpec) {
	newCond := func(status metav1.ConditionStatus, reason v1.ConditionReason, message string) metav1.Condition {
		return metav1.Condition{
			Type:    v1.ConditionTypeVersionInSync,
			Status:  status,
			Reason:  reason,
			Message: message,
		}
	}

	if probe.Err != nil {
		message := fmt.Sprintf("Version probe failed: %v", probe.Err)

		return newCond(metav1.ConditionUnknown, v1.ConditionReasonVersionProbeFailed, message), []EventSpec{{
			Type:    corev1.EventTypeWarning,
			Reason:  v1.EventReasonVersionProbeFailed,
			Action:  v1.EventActionVersionCheck,
			Message: message,
		}}
	}

	if probe.Pending {
		return newCond(metav1.ConditionUnknown, v1.ConditionReasonVersionPending, "Version probe has not completed yet"), nil
	}

	var mismatched []string
	for id, version := range replicaVersions {
		if version != "" && version != probe.Version {
			mismatched = append(mismatched, fmt.Sprintf("%s: %s", id, version))
		}
	}

	if len(mismatched) == 0 {
		return newCond(metav1.ConditionTrue, v1.ConditionReasonVersionMatch, ""), nil
	}

	slices.Sort(mismatched)
	cond := newCond(metav1.ConditionFalse, v1.ConditionReasonVersionMismatch,
		fmt.Sprintf("Replica version doesn't match version probe %s: %s", probe.Version, strings.Join(mismatched, ", ")))

	if isUpdating {
		return cond, nil
	}

	return cond, []EventSpec{{
		Type:    corev1.EventTypeWarning,
		Reason:  v1.EventReasonVersionDiverge,
		Action:  v1.EventActionVersionCheck,
		Message: cond.Message,
	}}
}

func (rm *ResourceManager) cleanupVersionProbeJobs(ctx context.Context, log controllerutil.Logger, job *batchv1.Job) {
	cli := rm.ctrl.GetClient()

	var jobs batchv1.JobList

	if err := cli.List(ctx, &jobs, client.InNamespace(rm.owner.GetNamespace()), client.MatchingLabels(map[string]string{
		controllerutil.LabelAppKey:  rm.specificName,
		controllerutil.LabelRoleKey: controllerutil.LabelVersionProbe,
	})); err != nil {
		log.Warn("failed to list obsolete version probe jobs", "error", err)
		return
	}

	for _, j := range jobs.Items {
		if j.Name != job.Name {
			log.Debug("deleting obsolete version probe job", "job", j.Name)

			if err := rm.Delete(ctx, &j, v1.EventActionVersionCheck, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				log.Warn("failed to delete obsolete version probe job", "job", j.Name, "error", err)
			}
		}
	}
}

func (rm *ResourceManager) buildVersionProbeJob(cfg VersionProbeConfig) (batchv1.Job, error) {
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   rm.owner.GetNamespace(),
			Labels:      maps.Clone(cfg.Labels),
			Annotations: maps.Clone(cfg.Annotations),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: new(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      maps.Clone(cfg.Labels),
					Annotations: maps.Clone(cfg.Annotations),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ImagePullSecrets:   cfg.PodTemplate.ImagePullSecrets,
					SecurityContext:    cfg.PodTemplate.SecurityContext,
					NodeSelector:       cfg.PodTemplate.NodeSelector,
					Tolerations:        cfg.PodTemplate.Tolerations,
					ServiceAccountName: cfg.PodTemplate.ServiceAccountName,
					SchedulerName:      cfg.PodTemplate.SchedulerName,
					Containers: []corev1.Container{
						{
							Name:                     v1.VersionProbeContainerName,
							Image:                    cfg.ContainerTemplate.Image.String(),
							ImagePullPolicy:          cfg.ContainerTemplate.ImagePullPolicy,
							SecurityContext:          cfg.ContainerTemplate.SecurityContext,
							TerminationMessagePolicy: corev1.TerminationMessageReadFile,
							TerminationMessagePath:   corev1.TerminationMessagePathDefault,
							Command:                  []string{"sh", "-c", fmt.Sprintf("%s --version > %s 2>&1", cfg.Binary, corev1.TerminationMessagePathDefault)},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(DefaultProbeCPURequest),
									corev1.ResourceMemory: resource.MustParse(DefaultProbeMemoryRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(DefaultProbeCPULimit),
									corev1.ResourceMemory: resource.MustParse(DefaultProbeMemoryLimit),
								},
							},
						},
					},
				},
			},
		},
	}

	// Apply user-provided version probe overrides.
	if cfg.VersionProbe != nil {
		var err error
		if job, err = patchResource(&job, cfg.VersionProbe, jobSchema); err != nil {
			return batchv1.Job{}, fmt.Errorf("patch version probe job: %w", err)
		}
	}

	if err := ctrl.SetControllerReference(rm.owner, &job, rm.ctrl.GetScheme()); err != nil {
		return batchv1.Job{}, fmt.Errorf("set version probe job controller reference: %w", err)
	}

	// Recreate successful probe only on Image or PullPolicy changes
	type imageKey struct {
		Image      string
		PullPolicy corev1.PullPolicy
	}

	imageHash, err := controllerutil.DeepHashObject(imageKey{
		Image:      cfg.ContainerTemplate.Image.String(),
		PullPolicy: cfg.ContainerTemplate.ImagePullPolicy,
	})
	if err != nil {
		return batchv1.Job{}, fmt.Errorf("hash version probe job image: %w", err)
	}

	job.Name = fmt.Sprintf("%s-version-probe-%s", rm.specificName, imageHash[:8])

	// Set reserved labels after overrides to ensure they are not modified by user overrides.
	job.Labels = controllerutil.MergeMaps(job.Labels, map[string]string{
		controllerutil.LabelAppKey:  rm.specificName,
		controllerutil.LabelRoleKey: controllerutil.LabelVersionProbe,
	})

	specHash, err := controllerutil.DeepHashObject(job.Spec)
	if err != nil {
		return batchv1.Job{}, fmt.Errorf("hash version probe job spec: %w", err)
	}

	controllerutil.AddSpecHashToObject(&job, specHash)

	return job, nil
}

func getJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) (batchv1.JobCondition, bool) {
	for _, c := range job.Status.Conditions {
		if c.Type == conditionType {
			return c, true
		}
	}

	return batchv1.JobCondition{}, false
}

func readVersionFromJob(ctx context.Context, log controllerutil.Logger, cli client.Client, job *batchv1.Job) (string, error) {
	// Find the pod created by this Job.
	var podList corev1.PodList
	if err := cli.List(ctx, &podList,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{
			batchv1.ControllerUidLabel: string(job.UID),
			batchv1.JobNameLabel:       job.Name,
		},
	); err != nil {
		return "", fmt.Errorf("list pods for version probe job: %w", err)
	}

	if len(podList.Items) == 0 {
		return "", fmt.Errorf("no pods found for version probe job %s", job.Name)
	}

	if len(podList.Items) > 1 {
		log.Warn("more than one pods found for version probe job")
	}

	for _, pod := range podList.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == v1.VersionProbeContainerName && cs.State.Terminated != nil {
				version, err := controllerutil.ParseVersion(cs.State.Terminated.Message)
				if err != nil {
					return "", fmt.Errorf("parse version probe from job container output: %w", err)
				}

				return version, nil
			}
		}
	}

	return "", errors.New("no termination message found in version probe job pods")
}
