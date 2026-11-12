package clickhouse

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// fakeBackupExecutor records the submitted statements and reports the configured operation states.
type fakeBackupExecutor struct {
	mu sync.Mutex

	unreachable bool
	hasSetting  bool
	startErr    error
	stateErr    error
	queries     []string
	replicas    []v1.ClickHouseReplicaID
	states      map[string]backupOperationState
}

func (f *fakeBackupExecutor) Ping(context.Context, v1.ClickHouseReplicaID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.unreachable {
		return errors.New("unreachable")
	}

	return nil
}

func (f *fakeBackupExecutor) HasSetting(context.Context, v1.ClickHouseReplicaID, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hasSetting, nil
}

func (f *fakeBackupExecutor) StartBackupOperation(_ context.Context, id v1.ClickHouseReplicaID, query string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.startErr != nil {
		return f.startErr
	}

	f.queries = append(f.queries, query)
	f.replicas = append(f.replicas, id)

	return nil
}

func (f *fakeBackupExecutor) BackupOperationState(
	_ context.Context,
	_ v1.ClickHouseReplicaID,
	operationID string,
) (backupOperationState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.stateErr != nil {
		return backupOperationState{}, false, f.stateErr
	}

	state, ok := f.states[operationID]

	return state, ok, nil
}

func (f *fakeBackupExecutor) setState(operationID string, state backupOperationState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.states[operationID] = state
}

func (f *fakeBackupExecutor) lastQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.queries) == 0 {
		return ""
	}

	return f.queries[len(f.queries)-1]
}

var _ = When("reconciling backup resources", Ordered, func() {
	const (
		namespace       = "default"
		clusterName     = "backup-cluster"
		credentialsName = "backup-storage-access"
		endpoint        = "https://bucket.s3.amazonaws.com/backups"
	)

	var (
		suite      testutil.TestSuit
		recorder   *events.FakeRecorder
		executor   *fakeBackupExecutor
		controller *BackupController
		storage    v1.BackupStorage
	)

	reconcileBackup := func(ctx context.Context, backup *v1.ClickHouseBackup) ctrl.Result {
		GinkgoHelper()

		result, err := backupReconciler{controller}.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(backup)})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, client.ObjectKeyFromObject(backup), backup)).To(Succeed())

		return result
	}

	reconcileRestore := func(ctx context.Context, restore *v1.ClickHouseRestore) ctrl.Result {
		GinkgoHelper()

		result, err := restoreReconciler{controller}.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(restore)})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, client.ObjectKeyFromObject(restore), restore)).To(Succeed())

		return result
	}

	newBackup := func(ctx context.Context, name string, mutate ...func(*v1.ClickHouseBackup)) *v1.ClickHouseBackup {
		GinkgoHelper()

		backup := &v1.ClickHouseBackup{
			Name: name, Namespace: namespace,
			Spec: v1.ClickHouseBackupSpec{
				ClusterRef: corev1.LocalObjectReference{Name: clusterName},
				Storage:    storage,
			},
		}
		for _, fn := range mutate {
			fn(backup)
		}

		Expect(suite.Client.Create(ctx, backup)).To(Succeed())

		return backup
	}

	completeBackup := func(ctx context.Context, backup *v1.ClickHouseBackup) {
		GinkgoHelper()

		reconcileBackup(ctx, backup)
		Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))

		executor.setState(backup.Status.ID, backupOperationState{Status: backupStatusCreated})
		reconcileBackup(ctx, backup)
		Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseCompleted))
	}

	completedCondition := func(conditions []metav1.Condition) metav1.Condition {
		GinkgoHelper()

		cond := meta.FindStatusCondition(conditions, v1.BackupConditionTypeCompleted)
		Expect(cond).NotTo(BeNil())

		return *cond
	}

	BeforeAll(func(ctx context.Context) {
		suite = testutil.SetupEnvironment(v1.AddToScheme)

		cluster := &v1.ClickHouseCluster{
			Name: clusterName, Namespace: namespace,
			Spec: v1.ClickHouseClusterSpec{
				Replicas:         new(int32(2)),
				Shards:           new(int32(1)),
				KeeperClusterRef: &v1.KeeperClusterReference{Name: "keeper"},
			},
		}
		Expect(suite.Client.Create(ctx, cluster)).To(Succeed())

		Expect(suite.Client.Create(ctx, &corev1.Secret{
			Name: cluster.SecretName(), Namespace: namespace,
			Data: map[string][]byte{SecretKeyManagementPassword: []byte("password")},
		})).To(Succeed())

		Expect(suite.Client.Create(ctx, &corev1.Secret{
			Name: credentialsName, Namespace: namespace,
			Data: map[string][]byte{"id": []byte("access-key"), "secret": []byte("secret-key")},
		})).To(Succeed())

		storage = v1.BackupStorage{S3: &v1.S3BackupStorage{
			Endpoint: endpoint,
			Credentials: &v1.S3Credentials{
				AccessKeyID:     v1.SecretKeySelector{Name: credentialsName, Key: "id"},
				SecretAccessKey: v1.SecretKeySelector{Name: credentialsName, Key: "secret"},
			},
		}}
	})

	AfterAll(func() {
		By("tearing down the test environment")

		Expect(suite.TestEnv.Stop()).To(Succeed())
	})

	BeforeEach(func() {
		recorder = events.NewFakeRecorder(64)
		executor = &fakeBackupExecutor{states: map[string]backupOperationState{}}
		controller = &BackupController{
			Client:   suite.Client,
			Logger:   suite.Log.Named("backup"),
			Recorder: recorder,
			NewExecutor: func(controllerutil.Logger, *v1.ClickHouseCluster, *corev1.Secret) backupExecutor {
				return executor
			},
		}
	})

	Describe("ClickHouseBackup", func() {
		It("should run a backup to completion", func(ctx context.Context) {
			backup := newBackup(ctx, "full")

			By("submitting the statement to the first replica")

			result := reconcileBackup(ctx, backup)
			Expect(result.RequeueAfter).To(Equal(backupPollInterval))
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))
			Expect(backup.Status.ID).To(Equal(string(backup.UID)))
			Expect(backup.Status.Replica).To(Equal(&v1.BackupReplica{ShardID: 0, Index: 0}))
			Expect(backup.Status.Location).To(Equal(endpoint + "/full"))
			Expect(backup.Status.StartTime).NotTo(BeNil())
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonInProgress))
			Expect(executor.lastQuery()).To(Equal(
				"BACKUP ALL EXCEPT DATABASES system ON CLUSTER 'default' " +
					"TO S3('https://bucket.s3.amazonaws.com/backups/full', 'access-key', 'secret-key') " +
					"SETTINGS id = '" + string(backup.UID) + "' ASYNC"))

			By("waiting while the backup is created")

			executor.setState(backup.Status.ID, backupOperationState{Status: backupStatusCreating})
			result = reconcileBackup(ctx, backup)
			Expect(result.RequeueAfter).To(Equal(backupPollInterval))
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))

			By("recording the result")

			executor.setState(backup.Status.ID, backupOperationState{
				Status: backupStatusCreated, NumFiles: 12, TotalSize: 2048, CompressedSize: 1024,
			})
			result = reconcileBackup(ctx, backup)
			Expect(result.IsZero()).To(BeTrue())
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseCompleted))
			Expect(backup.Status.CompletionTime).NotTo(BeNil())
			Expect(backup.Status.NumFiles).To(BeEquivalentTo(12))
			Expect(backup.Status.TotalSize).To(BeEquivalentTo(2048))
			Expect(backup.Status.CompressedSize).To(BeEquivalentTo(1024))
			Expect(completedCondition(backup.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			testutil.AssertEvents(recorder.Events, map[string]int{
				v1.EventReasonBackupStarted:   1,
				v1.EventReasonBackupCompleted: 1,
			})

			By("leaving the finished backup untouched")

			executor.setState(backup.Status.ID, backupOperationState{Status: "BACKUP_FAILED"})
			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseCompleted))
			Expect(executor.queries).To(HaveLen(1))
		})

		It("should wait for the cluster", func(ctx context.Context) {
			backup := newBackup(ctx, "no-cluster", func(b *v1.ClickHouseBackup) {
				b.Spec.ClusterRef.Name = "missing"
			})

			result := reconcileBackup(ctx, backup)
			Expect(result.RequeueAfter).To(Equal(backupWaitInterval))
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhasePending))
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonClusterNotFound))
			Expect(executor.queries).To(BeEmpty())
		})

		It("should wait for a reachable replica", func(ctx context.Context) {
			backup := newBackup(ctx, "unreachable")
			executor.unreachable = true

			result := reconcileBackup(ctx, backup)
			Expect(result.RequeueAfter).To(Equal(backupWaitInterval))
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhasePending))
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonClusterNotReady))

			executor.unreachable = false

			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))
		})

		It("should wait for the storage credentials", func(ctx context.Context) {
			backup := newBackup(ctx, "no-credentials", func(b *v1.ClickHouseBackup) {
				b.Spec.Storage.S3 = b.Spec.Storage.S3.DeepCopy()
				b.Spec.Storage.S3.Credentials.SecretAccessKey.Key = "missing"
			})

			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhasePending))
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonCredentialsInvalid))
			Expect(executor.queries).To(BeEmpty())
		})

		It("should use the server credentials when none are configured", func(ctx context.Context) {
			executor.hasSetting = true
			backup := newBackup(ctx, "server-credentials", func(b *v1.ClickHouseBackup) {
				b.Spec.Storage = v1.BackupStorage{S3: &v1.S3BackupStorage{Endpoint: endpoint + "/"}}
				b.Spec.Scope = &v1.BackupScope{Databases: []string{"analytics"}}
				b.Spec.Settings = map[string]string{"compression_method": "zstd"}
			})

			reconcileBackup(ctx, backup)
			Expect(executor.lastQuery()).To(Equal(
				"BACKUP DATABASE `analytics` ON CLUSTER 'default' " +
					"TO S3('https://bucket.s3.amazonaws.com/backups/server-credentials') " +
					"SETTINGS id = '" + string(backup.UID) + "', compression_method = 'zstd', " +
					"s3_allow_server_credentials_in_user_queries = 1 ASYNC"))
		})

		It("should fail when ClickHouse reports a failure", func(ctx context.Context) {
			backup := newBackup(ctx, "failed")
			reconcileBackup(ctx, backup)

			executor.setState(backup.Status.ID, backupOperationState{
				Status: "BACKUP_FAILED",
				Error:  "Code: 499. DB::Exception: The specified bucket does not exist. (S3_ERROR), Stack trace (when copying):\n0. DB",
			})
			result := reconcileBackup(ctx, backup)
			Expect(result.IsZero()).To(BeTrue())
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseFailed))

			cond := completedCondition(backup.Status.Conditions)
			Expect(cond.Reason).To(Equal(v1.BackupConditionReasonFailed))
			Expect(cond.Message).To(Equal(
				"BACKUP_FAILED: Code: 499. DB::Exception: The specified bucket does not exist. (S3_ERROR)"))
			testutil.AssertEvents(recorder.Events, map[string]int{
				v1.EventReasonBackupStarted: 1,
				v1.EventReasonBackupFailed:  1,
			})
		})

		It("should fail when the replica forgets the operation", func(ctx context.Context) {
			backup := newBackup(ctx, "state-lost")
			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))

			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseFailed))
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonStateLost))
		})

		It("should keep polling while the replica is unreachable", func(ctx context.Context) {
			backup := newBackup(ctx, "replica-restart")
			reconcileBackup(ctx, backup)

			executor.stateErr = errors.New("connection refused")
			result := reconcileBackup(ctx, backup)
			Expect(result.RequeueAfter).To(Equal(backupPollInterval))
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))
		})

		It("should fail when the cluster is deleted while the backup is running", func(ctx context.Context) {
			cluster := &v1.ClickHouseCluster{
				Name: "short-lived", Namespace: namespace,
				Spec: v1.ClickHouseClusterSpec{
					Replicas:         new(int32(1)),
					KeeperClusterRef: &v1.KeeperClusterReference{Name: "keeper"},
				},
			}
			Expect(suite.Client.Create(ctx, cluster)).To(Succeed())
			Expect(suite.Client.Create(ctx, &corev1.Secret{
				Name: cluster.SecretName(), Namespace: namespace,
				Data: map[string][]byte{SecretKeyManagementPassword: []byte("password")},
			})).To(Succeed())

			backup := newBackup(ctx, "orphaned", func(b *v1.ClickHouseBackup) {
				b.Spec.ClusterRef.Name = cluster.Name
			})
			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))

			Expect(suite.Client.Delete(ctx, cluster)).To(Succeed())
			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseFailed))
			Expect(completedCondition(backup.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonStateLost))
		})

		It("should fail without leaking credentials when the statement is rejected", func(ctx context.Context) {
			backup := newBackup(ctx, "rejected")
			executor.startErr = &clickhouse.Exception{
				Code:    598,
				Message: "Backup S3('https://bucket.s3.amazonaws.com/backups/rejected', 'access-key', 'secret-key') already exists",
			}

			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseFailed))
			Expect(backup.Status.ID).To(BeEmpty())

			cond := completedCondition(backup.Status.Conditions)
			Expect(cond.Reason).To(Equal(v1.BackupConditionReasonFailed))
			Expect(cond.Message).To(ContainSubstring("already exists"))
			Expect(cond.Message).NotTo(ContainSubstring("secret-key"))
		})

		It("should take an incremental backup once the base is completed", func(ctx context.Context) {
			base := newBackup(ctx, "base")
			incremental := newBackup(ctx, "incremental", func(b *v1.ClickHouseBackup) {
				b.Spec.BaseBackupRef = &corev1.LocalObjectReference{Name: base.Name}
			})

			By("waiting for the base backup")

			reconcileBackup(ctx, incremental)
			Expect(incremental.Status.Phase).To(Equal(v1.BackupPhasePending))
			Expect(completedCondition(incremental.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonBackupNotCompleted))

			completeBackup(ctx, base)

			By("referencing the base backup location")

			reconcileBackup(ctx, incremental)
			Expect(incremental.Status.Phase).To(Equal(v1.BackupPhaseRunning))
			Expect(incremental.Status.BaseLocation).To(Equal(endpoint + "/base"))
			Expect(executor.lastQuery()).To(ContainSubstring(
				"base_backup = S3('https://bucket.s3.amazonaws.com/backups/base', 'access-key', 'secret-key')"))
		})

		It("should fail an incremental backup whose base failed", func(ctx context.Context) {
			base := newBackup(ctx, "failed-base")
			reconcileBackup(ctx, base)
			executor.setState(base.Status.ID, backupOperationState{Status: "BACKUP_FAILED", Error: "boom"})
			reconcileBackup(ctx, base)

			incremental := newBackup(ctx, "orphan-incremental", func(b *v1.ClickHouseBackup) {
				b.Spec.BaseBackupRef = &corev1.LocalObjectReference{Name: base.Name}
			})
			reconcileBackup(ctx, incremental)
			Expect(incremental.Status.Phase).To(Equal(v1.BackupPhaseFailed))
			Expect(completedCondition(incremental.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonBackupFailed))
		})

		It("should reject invalid specs", func(ctx context.Context) {
			By("changing the spec of a created backup")

			backup := newBackup(ctx, "immutable")
			backup.Spec.ClusterRef.Name = "other"
			Expect(suite.Client.Update(ctx, backup)).To(MatchError(ContainSubstring("spec is immutable")))

			By("setting a managed setting")

			managed := &v1.ClickHouseBackup{
				Name: "managed-setting", Namespace: namespace,
				Spec: v1.ClickHouseBackupSpec{
					ClusterRef: corev1.LocalObjectReference{Name: clusterName},
					Storage:    storage,
					Settings:   map[string]string{"base_backup": "S3('x')"},
				},
			}
			Expect(suite.Client.Create(ctx, managed)).To(MatchError(ContainSubstring("managed by the operator")))

			By("using an invalid setting name")

			managed.Spec.Settings = map[string]string{"a = 1, b": "1"}
			Expect(suite.Client.Create(ctx, managed)).To(MatchError(ContainSubstring("valid identifiers")))

			By("omitting the storage type")

			managed.Spec.Settings = nil
			managed.Spec.Storage = v1.BackupStorage{}
			Expect(suite.Client.Create(ctx, managed)).To(MatchError(ContainSubstring("s3 storage must be set")))
		})
	})

	Describe("ClickHouseRestore", func() {
		newRestore := func(ctx context.Context, name string, mutate func(*v1.ClickHouseRestore)) *v1.ClickHouseRestore {
			GinkgoHelper()

			restore := &v1.ClickHouseRestore{
				Name: name, Namespace: namespace,
				Spec: v1.ClickHouseRestoreSpec{ClusterRef: corev1.LocalObjectReference{Name: clusterName}},
			}
			mutate(restore)
			Expect(suite.Client.Create(ctx, restore)).To(Succeed())

			return restore
		}

		It("should restore a completed backup", func(ctx context.Context) {
			backup := newBackup(ctx, "to-restore")
			restore := newRestore(ctx, "from-backup", func(r *v1.ClickHouseRestore) {
				r.Spec.BackupRef = &corev1.LocalObjectReference{Name: backup.Name}
				r.Spec.Settings = map[string]string{"allow_non_empty_tables": "true"}
			})

			By("waiting for the backup")

			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhasePending))
			Expect(completedCondition(restore.Status.Conditions).Reason).To(Equal(v1.BackupConditionReasonBackupNotCompleted))

			completeBackup(ctx, backup)
			testutil.AssertEvents(recorder.Events, map[string]int{
				v1.EventReasonBackupStarted:   1,
				v1.EventReasonBackupCompleted: 1,
			})

			By("submitting the statement")

			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseRunning))
			Expect(restore.Status.Location).To(Equal(endpoint + "/to-restore"))
			Expect(executor.lastQuery()).To(Equal(
				"RESTORE ALL EXCEPT DATABASES system ON CLUSTER 'default' " +
					"FROM S3('https://bucket.s3.amazonaws.com/backups/to-restore', 'access-key', 'secret-key') " +
					"SETTINGS id = '" + string(restore.UID) + "', allow_non_empty_tables = true, " +
					"use_same_s3_credentials_for_base_backup = 1 ASYNC"))

			By("recording the result")

			executor.setState(restore.Status.ID, backupOperationState{Status: backupStatusRestoring})
			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseRunning))

			executor.setState(restore.Status.ID, backupOperationState{Status: backupStatusRestored})
			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseCompleted))
			testutil.AssertEvents(recorder.Events, map[string]int{
				v1.EventReasonRestoreStarted:   1,
				v1.EventReasonRestoreCompleted: 1,
			})
		})

		It("should restore a backup located in the storage", func(ctx context.Context) {
			restore := newRestore(ctx, "from-source", func(r *v1.ClickHouseRestore) {
				r.Spec.Source = &v1.RestoreSource{Storage: storage, Name: "other-cluster-backup"}
				r.Spec.Scope = &v1.BackupScope{Tables: []v1.BackupTable{{Database: "default", Table: "events"}}}
			})

			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseRunning))
			Expect(executor.lastQuery()).To(HavePrefix(
				"RESTORE TABLE `default`.`events` ON CLUSTER 'default' " +
					"FROM S3('https://bucket.s3.amazonaws.com/backups/other-cluster-backup', 'access-key', 'secret-key')"))

			executor.setState(restore.Status.ID, backupOperationState{Status: "RESTORE_FAILED", Error: "table is not empty"})
			reconcileRestore(ctx, restore)
			Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseFailed))
			Expect(completedCondition(restore.Status.Conditions).Message).To(Equal("RESTORE_FAILED: table is not empty"))
		})

		It("should require exactly one backup source", func(ctx context.Context) {
			restore := &v1.ClickHouseRestore{
				Name: "no-source", Namespace: namespace,
				Spec: v1.ClickHouseRestoreSpec{ClusterRef: corev1.LocalObjectReference{Name: clusterName}},
			}
			Expect(suite.Client.Create(ctx, restore)).To(MatchError(ContainSubstring("exactly one of backupRef or source")))

			restore.Spec.BackupRef = &corev1.LocalObjectReference{Name: "backup"}
			restore.Spec.Source = &v1.RestoreSource{Storage: storage, Name: "backup"}
			Expect(suite.Client.Create(ctx, restore)).To(MatchError(ContainSubstring("exactly one of backupRef or source")))
		})
	})

	Describe("ClickHouseScheduledBackup", func() {
		var (
			scheduler *ScheduledBackupController
			now       time.Time
		)

		reconcileScheduled := func(ctx context.Context, scheduled *v1.ClickHouseScheduledBackup) ctrl.Result {
			GinkgoHelper()

			result, err := scheduler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scheduled)})
			Expect(err).NotTo(HaveOccurred())
			Expect(suite.Client.Get(ctx, client.ObjectKeyFromObject(scheduled), scheduled)).To(Succeed())

			return result
		}

		newScheduled := func(
			ctx context.Context,
			name string,
			mutate ...func(*v1.ClickHouseScheduledBackup),
		) *v1.ClickHouseScheduledBackup {
			GinkgoHelper()

			scheduled := &v1.ClickHouseScheduledBackup{
				Name: name, Namespace: namespace,
				Spec: v1.ClickHouseScheduledBackupSpec{
					Schedule: "0 * * * *",
					Template: v1.ClickHouseBackupTemplate{Spec: v1.ClickHouseBackupSpec{
						ClusterRef: corev1.LocalObjectReference{Name: clusterName},
						Storage:    storage,
					}},
				},
			}
			for _, fn := range mutate {
				fn(scheduled)
			}

			Expect(suite.Client.Create(ctx, scheduled)).To(Succeed())

			// Start from the beginning of the hour after the creation to make the schedule deterministic.
			now = scheduled.CreationTimestamp.Truncate(time.Hour).Add(time.Hour)

			return scheduled
		}

		ownedBackups := func(ctx context.Context, scheduled *v1.ClickHouseScheduledBackup) []v1.ClickHouseBackup {
			GinkgoHelper()

			backups, err := scheduler.ownedBackups(ctx, scheduled)
			Expect(err).NotTo(HaveOccurred())

			return backups
		}

		// runBackup lets the schedule create the backup of the next hour and finishes it.
		runBackup := func(ctx context.Context, scheduled *v1.ClickHouseScheduledBackup, succeed bool) *v1.ClickHouseBackup {
			GinkgoHelper()

			reconcileScheduled(ctx, scheduled)

			backup := &v1.ClickHouseBackup{}
			Expect(suite.Client.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      scheduled.Status.LastBackupName,
			}, backup)).To(Succeed())

			reconcileBackup(ctx, backup)
			Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseRunning))

			status := backupStatusCreated
			if !succeed {
				status = "BACKUP_FAILED"
			}

			executor.setState(backup.Status.ID, backupOperationState{Status: status})
			reconcileBackup(ctx, backup)

			now = now.Add(time.Hour)

			return backup
		}

		BeforeEach(func() {
			scheduler = &ScheduledBackupController{
				Client:   suite.Client,
				Scheme:   clientgoscheme.Scheme,
				Logger:   suite.Log.Named("scheduled-backup"),
				Recorder: recorder,
				Now:      func() time.Time { return now },
			}
		})

		It("should create backups on schedule", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "hourly")

			By("waiting for the first run")

			now = scheduled.CreationTimestamp.Time
			result := reconcileScheduled(ctx, scheduled)
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(result.RequeueAfter).To(BeNumerically("<=", time.Hour))
			Expect(ownedBackups(ctx, scheduled)).To(BeEmpty())
			Expect(meta.IsStatusConditionTrue(scheduled.Status.Conditions, v1.ScheduledBackupConditionTypeScheduled)).To(BeTrue())

			By("creating the backup of the due run")

			now = scheduled.CreationTimestamp.Truncate(time.Hour).Add(time.Hour + time.Minute)
			due := now.Truncate(time.Hour)
			result = reconcileScheduled(ctx, scheduled)
			Expect(result.RequeueAfter).To(Equal(59 * time.Minute))
			Expect(scheduled.Status.LastScheduleTime.Time).To(BeTemporally("==", due))
			Expect(scheduled.Status.LastBackupName).To(Equal("hourly-" + due.UTC().Format(scheduledBackupNameTimeLayout)))

			backups := ownedBackups(ctx, scheduled)
			Expect(backups).To(HaveLen(1))
			Expect(backups[0].Name).To(Equal(scheduled.Status.LastBackupName))
			Expect(backups[0].Spec.ClusterRef.Name).To(Equal(clusterName))
			Expect(backups[0].Spec.BaseBackupRef).To(BeNil())
			Expect(metav1.IsControlledBy(&backups[0], scheduled)).To(BeTrue())

			By("not creating the same run twice")

			reconcileScheduled(ctx, scheduled)
			Expect(ownedBackups(ctx, scheduled)).To(HaveLen(1))

			By("skipping a run while the previous backup is not finished")

			now = now.Add(time.Hour)

			reconcileScheduled(ctx, scheduled)
			Expect(ownedBackups(ctx, scheduled)).To(HaveLen(1))
			Expect(scheduled.Status.LastScheduleTime.Time).To(BeTemporally("==", due.Add(time.Hour)))

			By("recording the last successful backup")

			completeBackup(ctx, &backups[0])
			reconcileScheduled(ctx, scheduled)
			Expect(scheduled.Status.LastSuccessfulBackupName).To(Equal(backups[0].Name))
			Expect(scheduled.Status.LastSuccessfulTime).NotTo(BeNil())
		})

		It("should only run the most recent missed run", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "missed")

			now = now.Add(5*time.Hour + time.Minute)

			reconcileScheduled(ctx, scheduled)
			Expect(ownedBackups(ctx, scheduled)).To(HaveLen(1))
			Expect(scheduled.Status.LastScheduleTime.Time).To(BeTemporally("==", now.Truncate(time.Hour)))
		})

		It("should not create backups while suspended", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "suspended", func(s *v1.ClickHouseScheduledBackup) {
				s.Spec.Suspend = new(true)
			})

			result := reconcileScheduled(ctx, scheduled)
			Expect(result.IsZero()).To(BeTrue())
			Expect(ownedBackups(ctx, scheduled)).To(BeEmpty())

			cond := meta.FindStatusCondition(scheduled.Status.Conditions, v1.ScheduledBackupConditionTypeScheduled)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(v1.ScheduledBackupConditionReasonSuspended))
		})

		It("should report an invalid schedule", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "invalid", func(s *v1.ClickHouseScheduledBackup) {
				s.Spec.Schedule = "every day"
			})

			result := reconcileScheduled(ctx, scheduled)
			Expect(result.IsZero()).To(BeTrue())

			cond := meta.FindStatusCondition(scheduled.Status.Conditions, v1.ScheduledBackupConditionTypeScheduled)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(v1.ScheduledBackupConditionReasonInvalidSchedule))
			testutil.AssertEvents(recorder.Events, map[string]int{v1.EventReasonInvalidSchedule: 1})
		})

		It("should alternate full and incremental backups", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "incremental", func(s *v1.ClickHouseScheduledBackup) {
				s.Spec.FullBackupInterval = new(int32(3))
			})

			first := runBackup(ctx, scheduled, true)
			Expect(first.Spec.BaseBackupRef).To(BeNil())

			for range 2 {
				incremental := runBackup(ctx, scheduled, true)
				Expect(incremental.Spec.BaseBackupRef).To(Equal(&corev1.LocalObjectReference{Name: first.Name}))
			}

			By("retrying a failed full backup")

			failed := runBackup(ctx, scheduled, false)
			Expect(failed.Spec.BaseBackupRef).To(BeNil())
			Expect(failed.Status.Phase).To(Equal(v1.BackupPhaseFailed))

			second := runBackup(ctx, scheduled, true)
			Expect(second.Spec.BaseBackupRef).To(BeNil())

			incremental := runBackup(ctx, scheduled, true)
			Expect(incremental.Spec.BaseBackupRef).To(Equal(&corev1.LocalObjectReference{Name: second.Name}))
		})

		It("should prune the backups that exceed the history limits", func(ctx context.Context) {
			scheduled := newScheduled(ctx, "pruned", func(s *v1.ClickHouseScheduledBackup) {
				s.Spec.FullBackupInterval = new(int32(3))
				s.Spec.SuccessfulBackupsHistoryLimit = new(int32(2))
				s.Spec.FailedBackupsHistoryLimit = new(int32(1))
			})

			names := func() []string {
				GinkgoHelper()

				backups := ownedBackups(ctx, scheduled)

				result := make([]string, 0, len(backups))
				for _, backup := range backups {
					result = append(result, backup.Name)
				}

				return result
			}

			full := runBackup(ctx, scheduled, true)
			incremental1 := runBackup(ctx, scheduled, true)
			incremental2 := runBackup(ctx, scheduled, true)

			By("keeping the base of the kept incremental backups")

			reconcileScheduled(ctx, scheduled)
			Expect(names()).To(ContainElements(full.Name, incremental1.Name, incremental2.Name))
			Expect(names()).To(HaveLen(4))

			By("keeping a limited number of failed backups")

			nextFull := &v1.ClickHouseBackup{}
			Expect(suite.Client.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: scheduled.Status.LastBackupName,
			}, nextFull)).To(Succeed())
			reconcileBackup(ctx, nextFull)
			executor.setState(nextFull.Status.ID, backupOperationState{Status: "BACKUP_FAILED"})
			reconcileBackup(ctx, nextFull)

			now = now.Add(time.Hour)

			failed := runBackup(ctx, scheduled, false)
			reconcileScheduled(ctx, scheduled)
			Expect(names()).NotTo(ContainElement(nextFull.Name))
			Expect(names()).To(ContainElement(failed.Name))

			By("dropping the old chain once a newer one fills the history")

			Expect(suite.Client.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: scheduled.Status.LastBackupName,
			}, nextFull)).To(Succeed())
			completeBackup(ctx, nextFull)

			now = now.Add(time.Hour)

			newIncremental := runBackup(ctx, scheduled, true)
			Expect(newIncremental.Spec.BaseBackupRef).To(Equal(&corev1.LocalObjectReference{Name: nextFull.Name}))

			reconcileScheduled(ctx, scheduled)
			Expect(names()).NotTo(ContainElements(full.Name, incremental1.Name, incremental2.Name))
			Expect(names()).To(ContainElements(nextFull.Name, newIncremental.Name))
		})

		It("should reject invalid specs", func(ctx context.Context) {
			scheduled := &v1.ClickHouseScheduledBackup{
				Name: "with-base", Namespace: namespace,
				Spec: v1.ClickHouseScheduledBackupSpec{
					Schedule: "0 * * * *",
					Template: v1.ClickHouseBackupTemplate{Spec: v1.ClickHouseBackupSpec{
						ClusterRef:    corev1.LocalObjectReference{Name: clusterName},
						Storage:       storage,
						BaseBackupRef: &corev1.LocalObjectReference{Name: "base"},
					}},
				},
			}
			Expect(suite.Client.Create(ctx, scheduled)).To(MatchError(ContainSubstring("baseBackupRef is managed by the schedule")))

			scheduled.Spec.Template.Spec.BaseBackupRef = nil
			scheduled.Name = "a-name-that-leaves-no-room-for-the-timestamp-suffix"
			Expect(suite.Client.Create(ctx, scheduled)).To(MatchError(ContainSubstring("no more than 48 characters")))
		})
	})
})
