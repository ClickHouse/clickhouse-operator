package clickhouse

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("UpdateStage", func() {
	var rev chctrl.RevisionState

	BeforeEach(func() {
		rev = chctrl.RevisionState{
			StatefulSetRevision:   "sts-rev",
			ConfigurationRevision: "cfg-rev",
			RestartConfigRevision: "restart-rev",
			ReloadConfigRevision:  "reload-rev",
		}
	})

	settledReplica := func() replicaState {
		sts := &appsv1.StatefulSet{
			Name:       "test-0-0",
			Generation: 1,
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{ctrlutil.AnnotationConfigHash: rev.RestartConfigRevision},
					},
				},
			},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      1,
				CurrentRevision:    "rev-1",
				UpdateRevision:     "rev-1",
			},
		}
		ctrlutil.AddSpecHashToObject(sts, rev.StatefulSetRevision)

		cfg := &corev1.ConfigMap{Name: "test-0-0-configmap"}
		ctrlutil.AddObjectConfigHash(cfg, rev.ConfigurationRevision)

		pod := &corev1.Pod{
			Name: "test-0-0-0",
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}}},
		}

		return replicaState{
			Version:                   "26.5.1.1",
			ReloadConfigRevision:      rev.ReloadConfigRevision,
			UsersReloadConfigRevision: rev.ReloadConfigRevision,
			STS:                       sts, CFG: cfg,
			Pod: pod,
		}
	}

	It("should report UpToDate for a settled healthy replica", func() {
		Expect(settledReplica().UpdateStage(rev)).To(Equal(chctrl.StageUpToDate))
	})

	It("should treat a reload failure as NotReadyUpToDate, not Error", func() {
		replica := settledReplica()
		replica.ReloadConfigRevision = ""
		replica.UsersReloadConfigRevision = ""
		replica.ReloadError = errors.New("dial tcp: connect: connection refused")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageNotReadyUpToDate))
	})

	It("should keep a pending template change at HasDiff when the probe fails", func() {
		replica := settledReplica()
		replica.replicaProbe = replicaProbe{}
		replica.ReloadError = errors.New("dial tcp: i/o timeout")
		ctrlutil.AddSpecHashToObject(replica.STS, "next-sts-rev")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageHasDiff))
	})

	It("should report Updating while waiting for a reload result", func() {
		replica := settledReplica()
		replica.replicaProbe = replicaProbe{}

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageUpdating))
	})

	It("should wait for an existing StatefulSet update before a new diff", func() {
		replica := settledReplica()
		replica.STS.Status.ObservedGeneration = 0
		ctrlutil.AddSpecHashToObject(replica.STS, "next-sts-rev")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageUpdating))
	})
})

var _ = Describe("reconcileActiveReplicaStatus", func() {
	var (
		cc  *ClusterController
		r   *clickhouseReconciler
		log ctrlutil.Logger
		id  v1.ClickHouseReplicaID
		sts *appsv1.StatefulSet
	)

	BeforeEach(func(ctx context.Context) {
		scheme := k8sruntime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(v1.AddToScheme(scheme)).To(Succeed())

		cc = &ClusterController{
			Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
			Scheme:   scheme,
			Recorder: events.NewFakeRecorder(32),
		}
		cluster := &v1.ClickHouseCluster{
			Namespace: "default",
			Name:      "test",
			Spec:      v1.ClickHouseClusterSpec{Replicas: new(int32(1)), Shards: new(int32(1))},
		}
		r = &clickhouseReconciler{
			Controller:      cc,
			statusManager:   chctrl.NewStatusManager(cc, cluster),
			ResourceManager: chctrl.NewResourceManager(cc, cluster),
			Cluster:         cluster,
			ReplicaState:    map[v1.ClickHouseReplicaID]replicaState{},
		}
		log = ctrlutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		id = v1.ClickHouseReplicaID{ShardID: 0, Index: 0}
		sts = &appsv1.StatefulSet{
			Namespace: cluster.Namespace,
			Name:      cluster.StatefulSetNameByReplicaID(id),
			Labels:    ctrlutil.MergeMaps(id.Labels(), map[string]string{ctrlutil.LabelAppKey: cluster.SpecificName()}),
		}
		Expect(cc.Create(ctx, sts)).To(Succeed())
	})

	It("should ignore a replica pod that is being deleted", func(ctx context.Context) {
		pod := &corev1.Pod{Namespace: sts.Namespace, Name: sts.Name + "-0", Labels: sts.Labels, Finalizers: []string{"test/keep"}}
		Expect(cc.Create(ctx, pod)).To(Succeed())

		_, err := r.reconcileActiveReplicaStatus(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(r.ReplicaState[id].Pod.Name).To(Equal(pod.Name))

		Expect(cc.Delete(ctx, pod)).To(Succeed())

		_, err = r.reconcileActiveReplicaStatus(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(r.ReplicaState[id].Pod).To(BeNil())
	})

	It("should not be ready while scaled to zero with a replica StatefulSet left", func(ctx context.Context) {
		r.Cluster.Spec.Replicas = new(int32(0))

		_, err := r.reconcileActiveReplicaStatus(ctx, log)
		Expect(err).ToNot(HaveOccurred())

		ready := meta.FindStatusCondition(r.Cluster.Status.Conditions, v1.ConditionTypeReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal(v1.ConditionReasonScalingDown))
	})
})
