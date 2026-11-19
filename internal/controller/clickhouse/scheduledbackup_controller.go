package clickhouse

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	gcmp "github.com/google/go-cmp/cmp"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	// scheduledBackupNameTimeLayout is the suffix added to the schedule name to name the created backups.
	scheduledBackupNameTimeLayout = "20060102150405"
	// maxMissedScheduleWindow bounds how far back missed runs are looked for.
	maxMissedScheduleWindow = 7 * 24 * time.Hour
)

// ScheduledBackupController reconciles ClickHouseScheduledBackup objects.
// It creates ClickHouseBackup resources on a schedule and prunes the old ones.
type ScheduledBackupController struct {
	client.Client

	Scheme   *runtime.Scheme
	Logger   util.Logger
	Recorder events.EventRecorder
	// Now returns the current time, replaceable in tests.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousescheduledbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousescheduledbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousescheduledbackups/finalizers,verbs=update

// Reconcile creates the due backup of the schedule and removes the backups that exceed the history limits.
func (c *ScheduledBackupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	scheduled := &v1.ClickHouseScheduledBackup{}
	if err := c.Get(ctx, req.NamespacedName, scheduled); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get ClickHouseScheduledBackup %s: %w", req.String(), err)
	}

	if !scheduled.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	log := c.Logger.WithContext(ctx, scheduled)
	status := scheduled.Status.DeepCopy()

	backups, err := c.ownedBackups(ctx, scheduled)
	if err != nil {
		return ctrl.Result{}, err
	}

	backups, err = c.prune(ctx, log, scheduled, backups)
	if err != nil {
		return ctrl.Result{}, err
	}

	recordLastSuccessful(scheduled, backups)

	result, err := c.schedule(ctx, log, scheduled, backups)
	if err != nil {
		return ctrl.Result{}, err
	}

	if gcmp.Equal(status, &scheduled.Status) {
		return result, nil
	}

	if err := c.Status().Update(ctx, scheduled); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return result, nil
}

// schedule creates the backup of the most recent missed run and returns when the next run is due.
func (c *ScheduledBackupController) schedule(
	ctx context.Context,
	log util.Logger,
	scheduled *v1.ClickHouseScheduledBackup,
	backups []v1.ClickHouseBackup,
) (ctrl.Result, error) {
	schedule, err := parseBackupSchedule(scheduled.Spec)
	if err != nil {
		if c.setScheduled(scheduled, metav1.ConditionFalse, v1.ScheduledBackupConditionReasonInvalidSchedule, err.Error()) {
			c.Recorder.Eventf(scheduled, nil, corev1.EventTypeWarning, v1.EventReasonInvalidSchedule, v1.EventActionScheduling,
				"Invalid schedule: %s", err.Error())
		}

		return ctrl.Result{}, nil
	}

	if scheduled.Spec.Suspend != nil && *scheduled.Spec.Suspend {
		c.setScheduled(scheduled, metav1.ConditionFalse, v1.ScheduledBackupConditionReasonSuspended, "")

		return ctrl.Result{}, nil
	}

	c.setScheduled(scheduled, metav1.ConditionTrue, v1.ScheduledBackupConditionReasonActive, "")

	now := c.Now()

	last := scheduled.CreationTimestamp.Time
	if scheduled.Status.LastScheduleTime != nil {
		last = scheduled.Status.LastScheduleTime.Time
	}

	due, next := missedBackupSchedule(schedule, last, now)
	if due.IsZero() {
		return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
	}

	scheduled.Status.LastScheduleTime = &metav1.Time{Time: due}

	if active := activeBackup(backups); active != "" {
		log.Info("skipping scheduled backup, previous backup is not finished", "backup", active)
		c.Recorder.Eventf(scheduled, nil, corev1.EventTypeWarning, v1.EventReasonBackupSkipped, v1.EventActionScheduling,
			"Skipped the run of %s: backup %s is not finished", due.UTC().Format(time.RFC3339), active)

		return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
	}

	backup, err := c.newBackup(scheduled, backups, due)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := c.Create(ctx, backup); err != nil && !k8serrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create ClickHouseBackup %s: %w", backup.Name, err)
	}

	log.Info("created scheduled backup", "backup", backup.Name, "incremental", backup.IsIncremental())
	c.Recorder.Eventf(scheduled, backup, corev1.EventTypeNormal, v1.EventReasonBackupScheduled, v1.EventActionScheduling,
		"Created backup %s", backup.Name)

	scheduled.Status.LastBackupName = backup.Name

	return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
}

func (c *ScheduledBackupController) newBackup(
	scheduled *v1.ClickHouseScheduledBackup,
	backups []v1.ClickHouseBackup,
	due time.Time,
) (*v1.ClickHouseBackup, error) {
	backup := &v1.ClickHouseBackup{
		Name:      fmt.Sprintf("%s-%s", scheduled.Name, due.UTC().Format(scheduledBackupNameTimeLayout)),
		Namespace: scheduled.Namespace,
		Labels:    map[string]string{util.LabelScheduledBackup: scheduled.Name},
		Spec:      *scheduled.Spec.Template.Spec.DeepCopy(),
	}

	if base := incrementalBase(backups, scheduled.Spec.FullBackupInterval); base != "" {
		backup.Spec.BaseBackupRef = &corev1.LocalObjectReference{Name: base}
	}

	if err := controllerutil.SetControllerReference(scheduled, backup, c.Scheme); err != nil {
		return nil, fmt.Errorf("set owner of ClickHouseBackup %s: %w", backup.Name, err)
	}

	return backup, nil
}

// ownedBackups lists the backups created by the schedule, ordered from the oldest to the newest.
func (c *ScheduledBackupController) ownedBackups(
	ctx context.Context,
	scheduled *v1.ClickHouseScheduledBackup,
) ([]v1.ClickHouseBackup, error) {
	var list v1.ClickHouseBackupList
	if err := c.List(ctx, &list, client.InNamespace(scheduled.Namespace),
		client.MatchingLabels{util.LabelScheduledBackup: scheduled.Name}); err != nil {
		return nil, fmt.Errorf("list ClickHouseBackups of %s: %w", scheduled.Name, err)
	}

	backups := slices.DeleteFunc(list.Items, func(backup v1.ClickHouseBackup) bool {
		return !metav1.IsControlledBy(&backup, scheduled) || !backup.DeletionTimestamp.IsZero()
	})

	slices.SortFunc(backups, func(a, b v1.ClickHouseBackup) int {
		return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})

	return backups, nil
}

// prune deletes the backup resources that exceed the history limits and returns the remaining ones.
func (c *ScheduledBackupController) prune(
	ctx context.Context,
	log util.Logger,
	scheduled *v1.ClickHouseScheduledBackup,
	backups []v1.ClickHouseBackup,
) ([]v1.ClickHouseBackup, error) {
	expired := expiredBackups(backups, scheduled.Spec.SuccessfulBackupsHistoryLimit, scheduled.Spec.FailedBackupsHistoryLimit)
	if len(expired) == 0 {
		return backups, nil
	}

	for i := range backups {
		if _, ok := expired[backups[i].Name]; !ok {
			continue
		}

		log.Info("deleting backup resource that exceeds the history limit", "backup", backups[i].Name)

		if err := c.Delete(ctx, &backups[i]); err != nil && !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("delete ClickHouseBackup %s: %w", backups[i].Name, err)
		}
	}

	return slices.DeleteFunc(backups, func(backup v1.ClickHouseBackup) bool {
		_, ok := expired[backup.Name]
		return ok
	}), nil
}

// setScheduled sets the Scheduled condition and reports whether its status changed.
func (*ScheduledBackupController) setScheduled(
	scheduled *v1.ClickHouseScheduledBackup,
	status metav1.ConditionStatus,
	reason v1.ConditionReason,
	message string,
) bool {
	return chctrl.SetStatusCondition(&scheduled.Status.Conditions, metav1.Condition{
		Type:               v1.ScheduledBackupConditionTypeScheduled,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: scheduled.Generation,
	})
}

//nolint:ireturn // the cron parser returns the interface
func parseBackupSchedule(spec v1.ClickHouseScheduledBackupSpec) (cron.Schedule, error) {
	expression := spec.Schedule
	if spec.TimeZone != nil && *spec.TimeZone != "" {
		expression = fmt.Sprintf("CRON_TZ=%s %s", *spec.TimeZone, spec.Schedule)
	}

	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return nil, fmt.Errorf("parse schedule %q: %w", spec.Schedule, err)
	}

	return schedule, nil
}

// missedBackupSchedule returns the most recent run between last and now, or zero time if there is none,
// and the first run after now.
func missedBackupSchedule(schedule cron.Schedule, last, now time.Time) (due, next time.Time) {
	if earliest := now.Add(-maxMissedScheduleWindow); last.Before(earliest) {
		last = earliest
	}

	next = schedule.Next(last)
	for !next.After(now) {
		due = next
		next = schedule.Next(next)
	}

	return due, next
}

// activeBackup returns the name of a backup that is not finished yet.
func activeBackup(backups []v1.ClickHouseBackup) string {
	for i := range backups {
		if !backups[i].Status.Phase.Finished() {
			return backups[i].Name
		}
	}

	return ""
}

// incrementalBase returns the name of the full backup the next backup is based on,
// or an empty string if the next backup is a full one.
func incrementalBase(backups []v1.ClickHouseBackup, interval *int32) string {
	if interval == nil || *interval <= 1 {
		return ""
	}

	incrementals := int32(0)

	for i := len(backups) - 1; i >= 0; i-- {
		backup := &backups[i]

		if backup.IsIncremental() {
			incrementals++
			continue
		}

		if backup.Status.Phase != v1.BackupPhaseCompleted {
			continue
		}

		if incrementals >= *interval-1 {
			return ""
		}

		return backup.Name
	}

	return ""
}

// expiredBackups selects the finished backups that exceed the history limits.
// The most recent full backup and the base backups of the kept backups are never selected.
func expiredBackups(backups []v1.ClickHouseBackup, successfulLimit, failedLimit *int32) map[string]struct{} {
	var completed, failed []*v1.ClickHouseBackup

	protected := map[string]struct{}{}

	for i := range backups {
		backup := &backups[i]

		switch backup.Status.Phase {
		case v1.BackupPhaseCompleted:
			completed = append(completed, backup)
		case v1.BackupPhaseFailed:
			failed = append(failed, backup)
		case v1.BackupPhasePending, v1.BackupPhaseRunning:
			fallthrough
		default:
			protectBase(protected, backup)
		}
	}

	expired := map[string]struct{}{}

	if failedLimit != nil && len(failed) > int(*failedLimit) {
		for _, backup := range failed[:len(failed)-int(*failedLimit)] {
			expired[backup.Name] = struct{}{}
		}
	}

	if successfulLimit == nil || len(completed) <= int(*successfulLimit) {
		return expired
	}

	kept := completed[len(completed)-int(*successfulLimit):]
	for _, backup := range kept {
		protectBase(protected, backup)
	}

	for _, c := range slices.Backward(completed) {
		if !c.IsIncremental() {
			protected[c.Name] = struct{}{}
			break
		}
	}

	for _, backup := range completed[:len(completed)-int(*successfulLimit)] {
		if _, ok := protected[backup.Name]; !ok {
			expired[backup.Name] = struct{}{}
		}
	}

	return expired
}

func protectBase(protected map[string]struct{}, backup *v1.ClickHouseBackup) {
	if backup.IsIncremental() {
		protected[backup.Spec.BaseBackupRef.Name] = struct{}{}
	}
}

// recordLastSuccessful stores the most recent completed backup in the schedule status.
func recordLastSuccessful(scheduled *v1.ClickHouseScheduledBackup, backups []v1.ClickHouseBackup) {
	for i := len(backups) - 1; i >= 0; i-- {
		backup := &backups[i]
		if backup.Status.Phase != v1.BackupPhaseCompleted || backup.Status.CompletionTime == nil {
			continue
		}

		last := scheduled.Status.LastSuccessfulTime
		if last == nil || last.Before(backup.Status.CompletionTime) {
			scheduled.Status.LastSuccessfulTime = backup.Status.CompletionTime
			scheduled.Status.LastSuccessfulBackupName = backup.Name
		}

		return
	}
}
