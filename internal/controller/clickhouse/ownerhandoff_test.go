package clickhouse

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("operator ownership handoff", func() {
	const (
		selfID  = "1.16500.1"
		otherID = "1.16400.1"
	)

	var (
		log      = ctrlutil.NewLogger(zap.NewNop())
		recorder *events.FakeRecorder
	)

	newReconciler := func(labels, annotations map[string]string) (*clickhouseReconciler, *v1.ClickHouseCluster) {
		testScheme := k8sruntime.NewScheme()
		Expect(clientgoscheme.AddToScheme(testScheme)).To(Succeed())
		Expect(v1.AddToScheme(testScheme)).To(Succeed())

		cluster := &v1.ClickHouseCluster{
			Name:        "test-cluster",
			Namespace:   "default",
			Labels:      labels,
			Annotations: annotations,
		}

		recorder = events.NewFakeRecorder(16)
		controller := &ClusterController{
			Client:   fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster).Build(),
			Scheme:   testScheme,
			Recorder: recorder,
		}

		return &clickhouseReconciler{
			Controller: controller,
			Settings:   chctrl.Settings{OperatorOwnerID: selfID},
			Cluster:    cluster,
		}, cluster
	}

	Describe("reconcileOperatorOwnershipGate", func() {
		It("allows reconciliation when no operator-owner label is set, regardless of desired owner", func(ctx context.Context) {
			r, _ := newReconciler(nil, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.isOperatorOwner).To(BeTrue())
		})

		It("allows reconciliation when this operator is the owner", func(ctx context.Context) {
			r, _ := newReconciler(map[string]string{ctrlutil.LabelOperatorOwner: selfID}, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.isOperatorOwner).To(BeTrue())
		})

		It("blocks reconciliation when a different operator owns the cluster", func(ctx context.Context) {
			r, _ := newReconciler(map[string]string{ctrlutil.LabelOperatorOwner: otherID}, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.isOperatorOwner).To(BeFalse())
		})

		It("claims ownership when this operator is the desired owner but not yet the owner", func(ctx context.Context) {
			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        otherID,
				ctrlutil.LabelDesiredOperatorOwner: selfID,
			}, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())

			raw, ok := persisted.Annotations[ctrlutil.AnnotationOperatorOwnerClaim]
			Expect(ok).To(BeTrue())

			var claim operatorOwnerClaim
			Expect(json.Unmarshal([]byte(raw), &claim)).To(Succeed())
			Expect(claim.Owner).To(Equal(selfID))
			Expect(claim.ClaimedAt).NotTo(BeZero())

			Expect(recorder.Events).To(Receive(ContainSubstring("claimed ownership")))
		})

		It("does not refresh an existing claim from this operator", func(ctx context.Context) {
			claimedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			existing, err := json.Marshal(operatorOwnerClaim{Owner: selfID, ClaimedAt: claimedAt})
			Expect(err).NotTo(HaveOccurred())

			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        otherID,
				ctrlutil.LabelDesiredOperatorOwner: selfID,
			}, map[string]string{ctrlutil.AnnotationOperatorOwnerClaim: string(existing)})

			_, err = r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.ResourceVersion).To(Equal(cluster.ResourceVersion))
		})

		It("does not claim ownership when this operator is not the desired owner", func(ctx context.Context) {
			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        otherID,
				ctrlutil.LabelDesiredOperatorOwner: otherID,
			}, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.Annotations).NotTo(HaveKey(ctrlutil.AnnotationOperatorOwnerClaim))
		})

		It("does not claim ownership when this operator is already the owner", func(ctx context.Context) {
			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        selfID,
				ctrlutil.LabelDesiredOperatorOwner: selfID,
			}, nil)

			_, err := r.reconcileOperatorOwnershipGate(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.Annotations).NotTo(HaveKey(ctrlutil.AnnotationOperatorOwnerClaim))
		})
	})

	Describe("reconcileOperatorOwnershipHandoff", func() {
		It("moves operator-owner to the desired owner once it has claimed it", func(ctx context.Context) {
			claim, err := json.Marshal(operatorOwnerClaim{Owner: otherID, ClaimedAt: time.Now().UTC()})
			Expect(err).NotTo(HaveOccurred())

			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        selfID,
				ctrlutil.LabelDesiredOperatorOwner: otherID,
			}, map[string]string{ctrlutil.AnnotationOperatorOwnerClaim: string(claim)})

			_, err = r.reconcileOperatorOwnershipHandoff(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.Labels[ctrlutil.LabelOperatorOwner]).To(Equal(otherID))
			Expect(persisted.Annotations).NotTo(HaveKey(ctrlutil.AnnotationOperatorOwnerClaim))

			Expect(recorder.Events).To(Receive(ContainSubstring("handed ownership off")))
		})

		It("does not hand off without a matching claim", func(ctx context.Context) {
			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        selfID,
				ctrlutil.LabelDesiredOperatorOwner: otherID,
			}, nil)

			_, err := r.reconcileOperatorOwnershipHandoff(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.Labels[ctrlutil.LabelOperatorOwner]).To(Equal(selfID))
		})

		It("does not hand off when this operator is not the current owner", func(ctx context.Context) {
			claim, err := json.Marshal(operatorOwnerClaim{Owner: otherID, ClaimedAt: time.Now().UTC()})
			Expect(err).NotTo(HaveOccurred())

			r, cluster := newReconciler(map[string]string{
				ctrlutil.LabelOperatorOwner:        otherID,
				ctrlutil.LabelDesiredOperatorOwner: otherID,
			}, map[string]string{ctrlutil.AnnotationOperatorOwnerClaim: string(claim)})

			_, err = r.reconcileOperatorOwnershipHandoff(ctx, log)
			Expect(err).NotTo(HaveOccurred())

			var persisted v1.ClickHouseCluster
			Expect(r.GetClient().Get(ctx, client.ObjectKeyFromObject(cluster), &persisted)).To(Succeed())
			Expect(persisted.Labels[ctrlutil.LabelOperatorOwner]).To(Equal(otherID))
		})
	})

	Describe("reconcileService gating", func() {
		It("skips Service reconciliation when this operator is not the owner", func(ctx context.Context) {
			r := &clickhouseReconciler{
				Cluster:         &v1.ClickHouseCluster{Name: "test-cluster", Namespace: "default"},
				isOperatorOwner: false,
			}

			result, err := r.reconcileService(ctx, log)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(chctrl.StepContinue()))
		})
	})
})
