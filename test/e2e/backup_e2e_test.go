package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	s3Image  = "adobe/s3mock:5.2.3"
	s3Name   = "s3"
	s3Port   = 9090
	s3Bucket = "backups"
)

var _ = Describe("Backup controller", Label("clickhouse", "backup"), func() {
	var (
		ns      string
		cr      v1.ClickHouseCluster
		storage v1.BackupStorage
	)

	waitBackup := func(ctx context.Context, backup *v1.ClickHouseBackup) {
		GinkgoHelper()

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(backup), backup)).To(Succeed())
			g.Expect(backup.Status.Phase).To(Equal(v1.BackupPhaseCompleted), "conditions: %v", backup.Status.Conditions)
		}, 3*time.Minute).WithPolling(pollingInterval).Should(Succeed())
	}

	BeforeEach(func(ctx context.Context) {
		ns = testutil.EnsureTestNamespace(ctx, env)

		By("starting S3 storage")

		labels := map[string]string{controllerutil.LabelAppKey: s3Name}
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			Namespace: ns, Name: s3Name, Labels: labels,
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  s3Name,
				Image: s3Image,
				Env:   []corev1.EnvVar{{Name: "COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS", Value: s3Bucket}},
				Ports: []corev1.ContainerPort{{ContainerPort: s3Port}},
				ReadinessProbe: &corev1.Probe{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(s3Port)}},
			}}},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Service{
			Namespace: ns, Name: s3Name,
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Ports:    []corev1.ServicePort{{Port: s3Port, TargetPort: intstr.FromInt32(s3Port)}},
			},
		})).To(Succeed())

		Expect(k8sClient.Create(ctx, &corev1.Secret{
			Namespace: ns, Name: s3Name,
			StringData: map[string]string{"access-key-id": "test", "secret-access-key": "test"},
		})).To(Succeed())

		storage = v1.BackupStorage{S3: &v1.S3BackupStorage{
			Endpoint: fmt.Sprintf("http://%s.%s.svc:%d/%s", s3Name, ns, s3Port, s3Bucket),
			Credentials: &v1.S3Credentials{
				AccessKeyID:     v1.SecretKeySelector{Name: s3Name, Key: "access-key-id"},
				SecretAccessKey: v1.SecretKeySelector{Name: s3Name, Key: "secret-access-key"},
			},
		}}

		By("creating the cluster")

		name := fmt.Sprintf("backup-%d", rand.Uint32()) //nolint:gosec
		keeper := testutil.NewKeeperCluster(ns, name).WithStorage(defaultStorage).Cluster()
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})
		env.WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute)

		cr = testutil.NewClickHouseCluster(ns, name).
			WithReplicas(2).
			WithStorage(defaultStorage).
			WithKeeper(keeper.Name).
			Cluster()
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})
		env.WaitClickHouseUpdatedAndReady(ctx, &cr, 3*time.Minute)
	})

	It("should back up, restore and schedule backups of a cluster", func(ctx context.Context) {
		chClient, err := testutil.NewClickHouseClient(ctx, podDialer, &cr)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(chClient.Close)

		Expect(chClient.Exec(ctx,
			"CREATE TABLE default.backup_test (id UInt64) ENGINE=ReplicatedMergeTree ORDER BY id")).To(Succeed())
		Expect(chClient.Exec(ctx, "INSERT INTO default.backup_test SELECT number FROM numbers(1000)")).To(Succeed())

		By("taking a full backup")

		full := &v1.ClickHouseBackup{
			Namespace: ns, Name: "full",
			Spec: v1.ClickHouseBackupSpec{
				ClusterRef: corev1.LocalObjectReference{Name: cr.Name},
				Storage:    storage,
			},
		}
		Expect(k8sClient.Create(ctx, full)).To(Succeed())
		waitBackup(ctx, full)
		Expect(full.Status.Location).To(Equal(storage.S3.Endpoint + "/full"))
		Expect(full.Status.NumFiles).To(BeNumerically(">", 0))

		By("taking an incremental backup")

		Expect(chClient.Exec(ctx, "INSERT INTO default.backup_test SELECT number + 1000 FROM numbers(500)")).To(Succeed())

		incremental := &v1.ClickHouseBackup{
			Namespace: ns, Name: "incremental",
			Spec: v1.ClickHouseBackupSpec{
				ClusterRef:    corev1.LocalObjectReference{Name: cr.Name},
				Storage:       storage,
				BaseBackupRef: &corev1.LocalObjectReference{Name: full.Name},
			},
		}
		Expect(k8sClient.Create(ctx, incremental)).To(Succeed())
		waitBackup(ctx, incremental)
		Expect(incremental.Status.BaseLocation).To(Equal(full.Status.Location))

		By("restoring the dropped table")

		Expect(chClient.Exec(ctx, "DROP TABLE default.backup_test SYNC")).To(Succeed())

		restore := &v1.ClickHouseRestore{
			Namespace: ns, Name: "restore",
			Spec: v1.ClickHouseRestoreSpec{
				ClusterRef: corev1.LocalObjectReference{Name: cr.Name},
				BackupRef:  &corev1.LocalObjectReference{Name: incremental.Name},
			},
		}
		Expect(k8sClient.Create(ctx, restore)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(restore), restore)).To(Succeed())
			g.Expect(restore.Status.Phase).To(Equal(v1.BackupPhaseCompleted), "conditions: %v", restore.Status.Conditions)
		}, 3*time.Minute).WithPolling(pollingInterval).Should(Succeed())

		for id := range cr.ReplicaIDs() {
			Eventually(func(g Gomega) {
				var count uint64
				g.Expect(chClient.QueryRowReplica(ctx, id, "SELECT count() FROM default.backup_test", &count)).To(Succeed())
				g.Expect(count).To(BeEquivalentTo(1500))
			}, time.Minute).WithPolling(pollingInterval).Should(Succeed())
		}

		By("reporting a backup that cannot be written")

		failed := &v1.ClickHouseBackup{
			Namespace: ns, Name: "missing-bucket",
			Spec: v1.ClickHouseBackupSpec{
				ClusterRef: corev1.LocalObjectReference{Name: cr.Name},
				Storage: v1.BackupStorage{S3: &v1.S3BackupStorage{
					Endpoint:    fmt.Sprintf("http://%s.%s.svc:%d/missing-bucket", s3Name, ns, s3Port),
					Credentials: storage.S3.Credentials,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, failed)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(failed), failed)).To(Succeed())
			g.Expect(failed.Status.Phase).To(Equal(v1.BackupPhaseFailed))
		}, 2*time.Minute).WithPolling(pollingInterval).Should(Succeed())

		By("creating backups on schedule")

		scheduled := &v1.ClickHouseScheduledBackup{
			Namespace: ns, Name: "minutely",
			Spec: v1.ClickHouseScheduledBackupSpec{
				Schedule:           "* * * * *",
				FullBackupInterval: new(int32(2)),
				Template: v1.ClickHouseBackupTemplate{Spec: v1.ClickHouseBackupSpec{
					ClusterRef: corev1.LocalObjectReference{Name: cr.Name},
					Storage:    storage,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, scheduled)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, scheduled)).To(Succeed())
		})

		Eventually(func(g Gomega) {
			var backups v1.ClickHouseBackupList
			g.Expect(k8sClient.List(ctx, &backups, client.InNamespace(ns),
				client.MatchingLabels{controllerutil.LabelScheduledBackup: scheduled.Name})).To(Succeed())

			var completedFull, completedIncremental int

			for _, backup := range backups.Items {
				if backup.Status.Phase != v1.BackupPhaseCompleted {
					continue
				}

				if backup.IsIncremental() {
					completedIncremental++
				} else {
					completedFull++
				}
			}

			g.Expect(completedFull).To(BeNumerically(">=", 1))
			g.Expect(completedIncremental).To(BeNumerically(">=", 1))

			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(scheduled), scheduled)).To(Succeed())
			g.Expect(scheduled.Status.LastSuccessfulBackupName).NotTo(BeEmpty())
		}, 4*time.Minute).WithPolling(pollingInterval).Should(Succeed())
	})
})
