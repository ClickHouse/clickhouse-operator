package clickhouse

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("schema sync steps", func() {
	var (
		joining = v1.ClickHouseReplicaID{ShardID: 0, Index: 1}
		removed = v1.ClickHouseReplicaID{ShardID: 0, Index: 2}
		log     = ctrlutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter)))
	)

	replica := func(initialized bool) replicaState {
		status := corev1.ConditionFalse
		if initialized {
			status = corev1.ConditionTrue
		}

		return replicaState{Pod: &corev1.Pod{
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:   v1.ReplicaInitializedCondition,
				Status: status,
			}}},
		}}
	}

	reconciler := func(syncEnabled bool, states map[v1.ClickHouseReplicaID]replicaState, sync schemaSyncResult) *clickhouseReconciler {
		cluster := &v1.ClickHouseCluster{Spec: v1.ClickHouseClusterSpec{
			Shards:   new(int32(1)),
			Replicas: new(int32(2)),
			Settings: v1.ClickHouseSettings{EnableDatabaseSync: new(syncEnabled)},
		}}

		return &clickhouseReconciler{
			statusManager:  chctrl.NewStatusManager(nil, cluster),
			Cluster:        cluster,
			ReplicaState:   states,
			schemaSync:     sync,
			unsyncedShards: map[int32]bool{},
		}
	}

	type admissionCase struct {
		id                  v1.ClickHouseReplicaID
		syncDisabled        bool
		initialized         bool
		defaultDBReplicated *bool // nil: the default database step failed
		databasesSynced     bool
		admitted            bool
	}

	DescribeTable("decides which replicas may serve client traffic",
		func(c admissionCase) {
			sync := schemaSyncResult{
				defaultDBReplicated: map[v1.ClickHouseReplicaID]bool{},
				databasesSynced:     map[v1.ClickHouseReplicaID]bool{c.id: c.databasesSynced},
			}
			if c.defaultDBReplicated != nil {
				sync.defaultDBReplicated[c.id] = *c.defaultDBReplicated
			}

			r := reconciler(!c.syncDisabled, map[v1.ClickHouseReplicaID]replicaState{c.id: replica(c.initialized)}, sync)
			Expect(r.trafficAdmission()).To(HaveKeyWithValue(c.id, c.admitted))
		},
		Entry("withdraws traffic from a replica scheduled for deletion",
			admissionCase{id: removed, initialized: true}),
		Entry("keeps traffic on an initialized replica",
			admissionCase{id: joining, initialized: true, admitted: true}),
		Entry("admits every desired replica when sync is disabled",
			admissionCase{id: joining, syncDisabled: true, admitted: true}),
		Entry("admits a replica whose populated default database stays non-Replicated",
			admissionCase{id: joining, defaultDBReplicated: new(false), databasesSynced: true, admitted: true}),
		Entry("holds a replica whose default database step failed",
			admissionCase{id: joining, databasesSynced: true}),
		Entry("holds a replica whose databases are not synced",
			admissionCase{id: joining, defaultDBReplicated: new(true)}),
	)

	It("reports SchemaSyncDisabled even when the schema sync steps did not run", func(ctx context.Context) {
		r := reconciler(false, map[v1.ClickHouseReplicaID]replicaState{}, schemaSyncResult{})

		_, err := r.reconcileSyncCondition(ctx, log)
		Expect(err).NotTo(HaveOccurred())

		cond := meta.FindStatusCondition(r.Cluster.Status.Conditions, v1.ClickHouseConditionTypeSchemaInSync)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(v1.ClickHouseConditionSchemaSyncDisabled))
	})

	It("reports SchemaInSync without syncing databases to replicas already serving traffic", func(ctx context.Context) {
		serving := v1.ClickHouseReplicaID{ShardID: 0, Index: 0}
		r := reconciler(true, map[v1.ClickHouseReplicaID]replicaState{serving: replica(true)}, schemaSyncResult{
			defaultDBReplicated:    map[v1.ClickHouseReplicaID]bool{serving: true},
			databasesSynced:        map[v1.ClickHouseReplicaID]bool{},
			staleReplicasCleanedUp: true,
		})
		r.reachableReplicas = []v1.ClickHouseReplicaID{serving}

		_, err := r.reconcileSyncCondition(ctx, log)
		Expect(err).NotTo(HaveOccurred())

		cond := meta.FindStatusCondition(r.Cluster.Status.Conditions, v1.ClickHouseConditionTypeSchemaInSync)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(v1.ClickHouseConditionReplicasInSync))
	})
})
