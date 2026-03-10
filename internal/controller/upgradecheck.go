package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// UpdateUpgradeCondition checks for available upgrades and sets the VersionUpgraded condition.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) UpdateUpgradeCondition(
	ctx context.Context,
	log controllerutil.Logger,
	probe VersionProbeResult,
	upgradeChannel string,
) error {
	if r.GetVersionChecker() == nil {
		meta.RemoveStatusCondition(r.Cluster.Conditions(), string(v1.ConditionTypeVersionUpgraded))
		return nil
	}

	if probe.Pending {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeVersionUpgraded, metav1.ConditionUnknown, v1.ConditionReasonVersionPending, "Version probe has not completed yet"))
		return nil
	}

	if probe.Err != nil {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeVersionUpgraded, metav1.ConditionUnknown, v1.ConditionReasonVersionProbeFailed, "Version probe failed"))
		return nil //nolint:nilerr
	}

	result, err := r.GetVersionChecker().CheckUpdates(probe.Version, upgradeChannel)
	if err != nil {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeVersionUpgraded, metav1.ConditionUnknown, v1.ConditionReasonUpgradeCheckFailed, fmt.Sprintf("Upgrade check failed: %v", err)))
		return nil
	}

	var (
		reason  v1.ConditionReason
		message string
	)

	switch {
	case !result.OnChannel:
		reason = v1.ConditionReasonWrongReleaseChannel
		message = "Current version is not in the selected upgrade channel"
	case result.MinorUpdate != nil:
		reason = v1.ConditionReasonMinorUpdateAvailable
		message = "Minor upgrade available: " + result.MinorUpdate.Version()
	case len(result.MajorUpdates) > 0:
		versions := make([]string, len(result.MajorUpdates))
		for i, v := range result.MajorUpdates {
			versions[i] = v.Version()
		}

		reason = v1.ConditionReasonMajorUpdateAvailable
		message = "Major upgrades available: " + strings.Join(versions, ", ")

	case result.Outdated:
		reason = v1.ConditionReasonVersionOutdated
		message = "Current version " + probe.Version + " is out of support"
	default:
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeVersionUpgraded, metav1.ConditionTrue, v1.ConditionReasonUpToDate, ""))
		return nil
	}

	cond := r.NewCondition(v1.ConditionTypeVersionUpgraded, metav1.ConditionFalse, reason, message)
	if _, err = r.UpsertConditionAndSendEvent(ctx, log, cond, corev1.EventTypeWarning, v1.EventReasonUpgradeAvailable, v1.EventActionVersionCheck, cond.Message); err != nil {
		return fmt.Errorf("update VersionUpgraded condition: %w", err)
	}

	return nil
}
