package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

// GetUpgradeCondition checks for available upgrades and returns the VersionUpgraded condition to set.
// Returns current condition and optional EventSpec that should be recorded if condition Status changed.
func GetUpgradeCondition(
	checker upgrade.Checker,
	probe VersionProbeResult,
	upgradeChannel string,
) (metav1.Condition, []EventSpec) {
	newCond := func(status metav1.ConditionStatus, reason v1.ConditionReason, message string) metav1.Condition {
		return metav1.Condition{
			Type:    v1.ConditionTypeVersionUpgraded,
			Status:  status,
			Reason:  reason,
			Message: message,
		}
	}

	if probe.Pending {
		return newCond(metav1.ConditionUnknown, v1.ConditionReasonVersionPending, "Version probe has not completed yet"), nil
	}

	if probe.Err != nil {
		return newCond(metav1.ConditionUnknown, v1.ConditionReasonVersionProbeFailed, "Version probe failed"), nil
	}

	result, err := checker.CheckUpdates(probe.Version, upgradeChannel)
	if err != nil {
		return newCond(metav1.ConditionUnknown, v1.ConditionReasonUpgradeCheckFailed, fmt.Sprintf("Upgrade check failed: %v", err)), nil
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
		return newCond(metav1.ConditionTrue, v1.ConditionReasonUpToDate, ""), nil
	}

	cond := newCond(metav1.ConditionFalse, reason, message)

	return cond, []EventSpec{{
		Type:    corev1.EventTypeWarning,
		Reason:  v1.EventReasonUpgradeAvailable,
		Action:  v1.EventActionVersionCheck,
		Message: message,
	}}
}
