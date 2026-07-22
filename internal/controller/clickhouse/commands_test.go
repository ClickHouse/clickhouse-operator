package clickhouse

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/zapr"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	testReplicas             int32 = 3
	keeperHostname                 = "test-keeper"
	clickhouseHostnameFormat       = "test-clickhouse-0-%d-0"
	testPassword                   = "test-password"
	keeperImage                    = "clickhouse/clickhouse-keeper:26.6.2.81"
	clickhouseImage                = "clickhouse/clickhouse-server:26.6.2.81"
	testConfigRevision             = "test-revision-v1"
)

func generateKeeperConfig() *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`listen_host: 0.0.0.0
keeper_server:
  tcp_port: %d
  server_id: 1
  log_storage_path: /var/lib/clickhouse/coordination/log
  snapshot_storage_path: /var/lib/clickhouse/coordination/snapshots
  raft_configuration:
    server:
      id: 1
      hostname: %s
      port: %d
`, keeper.PortNative, keeperHostname, keeper.PortInterserver))
}

func generateCHConfig(replica int32) *strings.Reader {
	replicas := make([]string, 0, testReplicas)
	for i := range testReplicas {
		replicas = append(replicas, fmt.Sprintf(`{"host": "%s", "port": %d}`,
			fmt.Sprintf(clickhouseHostnameFormat, i), PortNative))
	}

	return strings.NewReader(fmt.Sprintf(`macros:
  shard: 0
  replica: %d
zookeeper:
  node:
    host: %s
    port: %d
remote_servers:
  default:
    shard:
      replica: [%s]
named_collections:
  __operator:
    config_revision: %s
display_secrets_in_show_and_select: true
custom_settings_prefixes: custom_
`, replica, keeperHostname, keeper.PortNative, strings.Join(replicas, ","), testConfigRevision))
}

func generateUsersConfig() *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`users:
  default:
    no_password
  operator:
    password: %s
    profile: __operator
    quota: default
    grants:
      query: GRANT ALL ON *.*
profiles:
  __operator:
    profile: default
    custom_operator_reload_revision: "'%s'"
`, testPassword, testConfigRevision))
}

var _ = Describe("commander", Ordered, Label("integration"), func() {
	var (
		chContainers []testcontainers.Container
		testNetwork  *testcontainers.DockerNetwork
		cmd          *commander
	)

	BeforeAll(func(ctx context.Context) {
		var err error

		By("creating Docker network")

		testNetwork, err = network.New(ctx)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func(ctx context.Context) {
			By("removing Docker network")

			_ = testNetwork.Remove(ctx)
		})

		By("starting ClickHouse Keeper")

		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: keeperImage,
				ConfigModifier: func(c *container.Config) {
					c.Hostname = keeperHostname
				},
				Env: map[string]string{
					"KEEPER_CONFIG": "/etc/clickhouse-keeper/keeper_config.yaml",
				},
				Networks: []string{testNetwork.Name},
				Files: []testcontainers.ContainerFile{
					{
						Reader:            generateKeeperConfig(),
						ContainerFilePath: "/etc/clickhouse-keeper/keeper_config.yaml",
						FileMode:          0o644,
					},
				},
				ExposedPorts: []string{strconv.FormatInt(keeper.PortNative, 10) + "/tcp"},
				WaitingFor:   wait.ForListeningPort(strconv.FormatInt(keeper.PortNative, 10) + "/tcp"),
			},
			Started: true,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx context.Context) {
			By("terminating ClickHouse Keeper")

			_ = ctr.Terminate(ctx, testcontainers.StopTimeout(time.Second))
		})

		chPort := strconv.FormatInt(PortNative, 10) + "/tcp"
		chHTTPPort := strconv.FormatInt(PortHTTP, 10) + "/tcp"
		hostTargets := map[string]string{}
		cluster := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				Shards:   new(int32(1)),
				Replicas: new(testReplicas),
			},
		}

		for i := range testReplicas {
			By(fmt.Sprintf("starting ClickHouse node %d", i))
			hostname := fmt.Sprintf(clickhouseHostnameFormat, i)
			ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: testcontainers.ContainerRequest{
					Image: clickhouseImage,
					ConfigModifier: func(c *container.Config) {
						c.Hostname = hostname
					},
					Networks: []string{testNetwork.Name},
					Files: []testcontainers.ContainerFile{
						{
							Reader:            generateCHConfig(i),
							ContainerFilePath: "/etc/clickhouse-server/config.d/test.yaml",
							FileMode:          0o644,
						},
						{
							Reader:            generateUsersConfig(),
							ContainerFilePath: "/etc/clickhouse-server/users.d/test.yaml",
							FileMode:          0o644,
						},
					},
					ExposedPorts: []string{chPort, chHTTPPort},
					WaitingFor:   wait.ForHTTP("/").WithPort(chHTTPPort).WithStartupTimeout(2 * time.Minute),
				},
				Started: true,
			})
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func(ctx context.Context) {
				By("terminating ClickHouse node: " + hostname)

				_ = ctr.Terminate(ctx, testcontainers.StopTimeout(time.Second))
			})

			chContainers = append(chContainers, ctr)

			host, err := ctr.Host(ctx)
			Expect(err).NotTo(HaveOccurred())
			port, err := ctr.MappedPort(ctx, chPort)
			Expect(err).NotTo(HaveOccurred())

			hostTargets[cluster.HostnameByID(v1.ClickHouseReplicaID{Index: i})] = net.JoinHostPort(host, port.Port())
		}

		zapLogger := zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
		logf.SetLogger(zapr.NewLogger(zapLogger))
		logger := controllerutil.NewLogger(zapLogger)

		dialer := func(ctx context.Context, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("split addr %q: %w", addr, err)
			}

			target, ok := hostTargets[host]
			if !ok {
				return nil, fmt.Errorf("no test container for host %q", host)
			}

			return (&net.Dialer{}).DialContext(ctx, "tcp", target)
		}

		secret := &corev1.Secret{Data: map[string][]byte{SecretKeyManagementPassword: []byte(testPassword)}}
		cache := newConnCache()
		cmd = newCommander(logger, &cluster, secret, dialer, cache)

		DeferCleanup(func() {
			cache.Close(logger)
		})
	})

	It("should probe version and config revision in a single query", func(ctx context.Context) {
		for id := range cmd.cluster.ReplicaIDs() {
			Eventually(func(g Gomega) {
				probe, err := cmd.Probe(ctx, id)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(probe.Version).To(MatchRegexp(`^\d+\.\d+\.\d+\.\d+$`))
				g.Expect(probe.ReloadConfigRevision).To(Equal(testConfigRevision))
				g.Expect(probe.UsersReloadConfigRevision).To(Equal(testConfigRevision))
			}, "1m", "100ms").Should(Succeed())
		}
	})

	It("preserves a non-empty Atomic default instead of dropping it", func(ctx context.Context) {
		id0 := v1.ClickHouseReplicaID{ShardID: 0, Index: 0}

		By("seeding a table into the Atomic default database")

		conn, err := cmd.getConn(id0)
		Expect(err).NotTo(HaveOccurred())
		Expect(conn.Exec(ctx, `CREATE TABLE default.seed (id UInt64) ENGINE=MergeTree ORDER BY id`)).To(Succeed())
		Expect(conn.Exec(ctx, `INSERT INTO default.seed SELECT number FROM numbers(10)`)).To(Succeed())

		By("running EnsureDefaultDatabaseEngine")

		// A non-empty Atomic `default` must not be dropped; the operator should
		// report failure (SchemaInSync=false) rather than destroy data.
		Expect(cmd.EnsureDefaultDatabaseEngine(ctx, cmd.log, slices.Collect(cmd.cluster.ReplicaIDs()))).To(BeFalse())

		By("verifying default database is still Atomic and the seeded table survived")

		var engine string
		Expect(conn.QueryRow(ctx, `SELECT engine FROM system.databases WHERE name='default'`).Scan(&engine)).To(Succeed())
		Expect(engine).To(Equal("Atomic"))

		var count uint64
		Expect(conn.QueryRow(ctx, `SELECT count() FROM default.seed`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(uint64(10)))

		By("dropping the seed table so the default database is empty again")

		Expect(conn.Exec(ctx, `DROP TABLE default.seed SYNC`)).To(Succeed())
	})

	It("converts default database from Atomic to Replicated", func(ctx context.Context) {
		By("running EnsureDefaultDatabaseEngine")

		Expect(cmd.EnsureDefaultDatabaseEngine(ctx, cmd.log, slices.Collect(cmd.cluster.ReplicaIDs()))).To(BeTrue())

		By("verifying default database is now Replicated")

		for id := range cmd.cluster.ReplicaIDs() {
			dbs, err := cmd.Databases(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(dbs).To(HaveKey("default"))
			Expect(dbs["default"].IsReplicated).To(BeTrue())
			Expect(dbs["default"].UUID).To(Equal(uuid.NewSHA1(uuid.Nil, []byte(cmd.cluster.SpecificName())).String()))
		}
	})

	It("replicates databases to all replicas", func(ctx context.Context) {
		By("creating replicated database on replica 0")

		id0 := v1.ClickHouseReplicaID{ShardID: 0, Index: 0}
		dbUUID := uuid.New().String()
		q := `CREATE DATABASE testdb UUID '%s' ENGINE=Replicated('/clickhouse/databases/testdb', '{shard}', '{replica}')`
		conn, err := cmd.getConn(id0)
		Expect(err).NotTo(HaveOccurred())
		Expect(conn.Exec(ctx, fmt.Sprintf(q, dbUUID))).To(Succeed())

		dbs, err := cmd.Databases(ctx, id0)
		Expect(err).NotTo(HaveOccurred())
		Expect(dbs).To(HaveKey("default"))
		Expect(dbs).To(HaveKeyWithValue("testdb", databaseDescriptor{
			Name:         "testdb",
			EngineFull:   "Replicated('/clickhouse/databases/testdb', '{shard}', '{replica}')",
			UUID:         dbUUID,
			IsReplicated: true,
		}))

		// Create on other replicas with same UUID/engine
		for id := range cmd.cluster.ReplicaIDs() {
			Expect(cmd.CreateDatabases(ctx, cmd.log, id, dbs)).To(Succeed())
			newDBs, err := cmd.Databases(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(newDBs).To(Equal(dbs))
		}
	})

	It("should sync all replicas in shard", func(ctx context.Context) {
		By("creating test tables")

		conn, err := cmd.getConn(v1.ClickHouseReplicaID{ShardID: 0, Index: 0})
		Expect(err).NotTo(HaveOccurred())
		Expect(conn.Exec(ctx, `CREATE TABLE testdb.test (id UInt64) ENGINE=ReplicatedMergeTree ORDER BY id`)).To(Succeed())
		Expect(conn.Exec(ctx, `INSERT INTO testdb.test SELECT number FROM numbers(10)`)).To(Succeed())

		By("syncing shard")
		Eventually(func() error {
			err := cmd.SyncShard(ctx, cmd.log, 0)
			if err != nil {
				By(fmt.Sprintf("retrying sync shard error: %v", err))
			}

			return err
		}, "5s", "100ms").To(Succeed())

		var count uint64
		for id := range cmd.cluster.ReplicaIDs() {
			By(fmt.Sprintf("verifying data is replicated to replica %d", id.Index))

			conn, err := cmd.getConn(id)
			Expect(err).NotTo(HaveOccurred())

			row := conn.QueryRow(ctx, `SELECT count() FROM testdb.test`)
			Expect(row.Err()).ToNot(HaveOccurred())
			Expect(row.Scan(&count)).To(Succeed())
			Expect(count).To(Equal(uint64(10)))
		}
	})

	Describe("CleanupDatabaseReplicas", Ordered, func() {
		It("should do nothing if nothing changed", func(ctx context.Context) {
			Expect(cmd.CleanupDatabaseReplicas(ctx, cmd.log, map[v1.ClickHouseReplicaID]struct{}{})).To(Succeed())
		})

		It("should do nothing if still active", func(ctx context.Context) {
			*cmd.cluster.Spec.Replicas = testReplicas - 1
			err := cmd.CleanupDatabaseReplicas(ctx, cmd.log, map[v1.ClickHouseReplicaID]struct{}{})
			Expect(err.Error()).To(ContainSubstring("not cleaned up"))
		})

		It("should do nothing if not in sync", func(ctx context.Context) {
			Expect(chContainers[len(chContainers)-1].Stop(ctx, new(time.Nanosecond))).To(Succeed())
			Eventually(func() uint64 {
				_, conn, err := cmd.getAnyConn(ctx)
				Expect(err).ToNot(HaveOccurred())

				row := conn.QueryRow(ctx, `SELECT COUNT(DISTINCT host_name) FROM system.clusters WHERE is_active`)
				Expect(row.Err()).ToNot(HaveOccurred())

				var activeCount uint64
				Expect(row.Scan(&activeCount)).To(Succeed())

				return activeCount
			}, "1m", "1s").To(Equal(uint64(testReplicas - 1)))

			err := cmd.CleanupDatabaseReplicas(ctx, cmd.log, map[v1.ClickHouseReplicaID]struct{}{
				{Index: testReplicas - 1}: {},
			})
			Expect(err.Error()).To(ContainSubstring("not cleaned up"))
		})

		It("should cleanup outdated replica", func(ctx context.Context) {
			Expect(cmd.CleanupDatabaseReplicas(ctx, cmd.log, map[v1.ClickHouseReplicaID]struct{}{})).To(Succeed())
			_, conn, err := cmd.getAnyConn(ctx)
			Expect(err).ToNot(HaveOccurred())

			row := conn.QueryRow(ctx, `SELECT MAX(total_replicas) FROM system.database_replicas`)
			Expect(row.Err()).ToNot(HaveOccurred())

			var count uint32
			Expect(row.Scan(&count)).To(Succeed())
			Expect(count).To(Equal(uint32(testReplicas - 1)))
		})
	})
})
