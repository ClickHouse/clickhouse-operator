package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// operatorOwnerClaim is the JSON payload of the clickhouse.com/operator-owner-claim annotation,
// written by an operator instance claiming ownership of a cluster it was made the
// desired-operator-owner of. Field names are shared with data-plane-management, which reads
// this annotation off the CR; see platform/constants/k8sClickHouseClusterMeta.go in
// data-plane-application.
type operatorOwnerClaim struct {
	Owner     string    `json:"owner"`
	ClaimedAt time.Time `json:"claimedAt"`
}

func readOperatorOwnerClaim(cluster *v1.ClickHouseCluster) (operatorOwnerClaim, bool) {
	raw, ok := cluster.Annotations[ctrlutil.AnnotationOperatorOwnerClaim]
	if !ok {
		return operatorOwnerClaim{}, false
	}

	var claim operatorOwnerClaim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		return operatorOwnerClaim{}, false
	}

	return claim, true
}

// patchClusterMetadata applies mutate to r.Cluster and, if it changed the labels or annotations,
// sends only that diff as a merge patch. A merge patch (rather than a whole-object update) is
// required here: the admin API and the other operator instance in a handoff both write to this
// CR's labels/annotations outside of this reconcile, and a whole-object update would race them.
func (r *clickhouseReconciler) patchClusterMetadata(ctx context.Context, mutate func(cluster *v1.ClickHouseCluster)) error {
	before := r.Cluster.DeepCopy()
	mutate(r.Cluster)

	if maps.Equal(before.Labels, r.Cluster.Labels) && maps.Equal(before.Annotations, r.Cluster.Annotations) {
		return nil
	}

	if err := r.GetClient().Patch(ctx, r.Cluster, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("patch ClickHouseCluster %s metadata: %w", r.Cluster.Name, err)
	}

	return nil
}

// reconcileOperatorOwnershipGate runs before the Services step (and everything else that
// touches the cluster) to decide whether this operator instance is allowed to reconcile the
// cluster's Services, and to claim ownership if this operator was made the desired owner.
//
// Before the operator-owner label is ever set on a cluster, every operator instance reconciles
// it as before; this is what lets this gate roll out ahead of the handoff migration itself.
func (r *clickhouseReconciler) reconcileOperatorOwnershipGate(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	owner := r.Cluster.Labels[ctrlutil.LabelOperatorOwner]
	desired := r.Cluster.Labels[ctrlutil.LabelDesiredOperatorOwner]

	r.isOperatorOwner = owner == "" || owner == r.OperatorOwnerID

	// Only the desired owner claims, and only while it is not already the owner.
	if desired != r.OperatorOwnerID || owner == r.OperatorOwnerID {
		return chctrl.StepContinue(), nil
	}

	if claim, ok := readOperatorOwnerClaim(r.Cluster); ok && claim.Owner == r.OperatorOwnerID {
		// Already claimed by us; keep the original claimedAt instead of refreshing it every reconcile.
		return chctrl.StepContinue(), nil
	}

	claim := operatorOwnerClaim{Owner: r.OperatorOwnerID, ClaimedAt: time.Now().UTC().Truncate(time.Second)}

	raw, err := json.Marshal(claim)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("marshal operator owner claim: %w", err)
	}

	if err := r.patchClusterMetadata(ctx, func(cluster *v1.ClickHouseCluster) {
		if cluster.Annotations == nil {
			cluster.Annotations = map[string]string{}
		}

		cluster.Annotations[ctrlutil.AnnotationOperatorOwnerClaim] = string(raw)
	}); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("claim operator ownership: %w", err)
	}

	log.Info("claimed operator ownership", "operatorOwnerID", r.OperatorOwnerID)
	r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonOperatorOwnerClaimed, v1.EventActionReconciling,
		"Operator %q claimed ownership, waiting for the current owner %q to hand it off", r.OperatorOwnerID, owner)

	return chctrl.StepContinue(), nil
}

// reconcileOperatorOwnershipHandoff runs after every other step, including Services. When this
// operator instance is the current owner, a different instance is the desired owner, and that
// instance has claimed ownership (clickhouse.com/operator-owner-claim), this finishes the
// handoff by moving the operator-owner label to the desired owner and clearing the claim.
//
// The claim is only ever acted on by the current owner, never by the claimant itself, so the
// two operator instances reconciling the same cluster during a handoff never race to write
// operator-owner.
func (r *clickhouseReconciler) reconcileOperatorOwnershipHandoff(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	owner := r.Cluster.Labels[ctrlutil.LabelOperatorOwner]
	desired := r.Cluster.Labels[ctrlutil.LabelDesiredOperatorOwner]

	if owner != r.OperatorOwnerID || desired == "" || desired == owner {
		return chctrl.StepContinue(), nil
	}

	claim, ok := readOperatorOwnerClaim(r.Cluster)
	if !ok || claim.Owner != desired {
		return chctrl.StepContinue(), nil
	}

	if err := r.patchClusterMetadata(ctx, func(cluster *v1.ClickHouseCluster) {
		if cluster.Labels == nil {
			cluster.Labels = map[string]string{}
		}

		cluster.Labels[ctrlutil.LabelOperatorOwner] = desired
		delete(cluster.Annotations, ctrlutil.AnnotationOperatorOwnerClaim)
	}); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("hand off operator ownership to %q: %w", desired, err)
	}

	log.Info("handed off operator ownership", "from", owner, "to", desired)
	r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonOperatorOwnerHandedOff, v1.EventActionReconciling,
		"Operator %q handed ownership off to %q", owner, desired)

	return chctrl.StepContinue(), nil
}
