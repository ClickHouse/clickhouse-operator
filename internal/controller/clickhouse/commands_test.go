package clickhouse

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/go-logr/zapr"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
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
	testUsername                   = "operator"
	keeperImage                    = "clickhouse/clickhouse-keeper:26.2"
	clickhouseImage                = "clickhouse/clickhouse-server:26.2"
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
display_secrets_in_show_and_select: true
`, replica, keeperHostname, keeper.PortNative, strings.Join(replicas, ",")))
}

func generateUsersConfig() *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`users:
  default:
    no_password
  operator:
    password: %s
    profile: default
    quota: default
    grants:
      query: GRANT ALL ON *.*
`, testPassword))
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
				WaitingFor:   wait.ForListeningPort(nat.Port(strconv.FormatInt(keeper.PortNative, 10) + "/tcp")),
			},
			Started: true,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx context.Context) {
			By("terminating ClickHouse Keeper")

			_ = ctr.Terminate(ctx, testcontainers.StopTimeout(time.Second))
		})

		chPort := strconv.FormatInt(PortNative, 10) + "/tcp"
		conns := map[v1.ClickHouseReplicaID]clickhouse.Conn{}

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
					ExposedPorts: []string{chPort},
					WaitingFor:   wait.ForListeningPort(nat.Port(chPort)),
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
			port, err := ctr.MappedPort(ctx, nat.Port(chPort))
			Expect(err).NotTo(HaveOccurred())

			conn, err := clickhouse.Open(&clickhouse.Options{
				Addr: []string{net.JoinHostPort(host, port.Port())},
				Auth: clickhouse.Auth{Username: testUsername, Password: testPassword},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(conn.Ping(ctx)).To(Succeed())

			conns[v1.ClickHouseReplicaID{ShardID: 0, Index: i}] = conn
		}

		logger := zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
		logf.SetLogger(zapr.NewLogger(logger))

		cmd = &commander{
			log: controllerutil.NewLogger(logger),
			cluster: &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: v1.ClickHouseClusterSpec{
					Shards:   new(int32(1)),
					Replicas: new(testReplicas),
				},
			},
			auth:  clickhouse.Auth{Username: testUsername, Password: testPassword},
			conns: conns,
		}
		DeferCleanup(func() {
			cmd.Close()
		})
	})

	It("should get every node version", func(ctx context.Context) {
		for id := range cmd.cluster.ReplicaIDs() {
			v0, err := cmd.Version(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(v0).To(MatchRegexp(`^\d+\.\d+\.\d+\.\d+$`))
		}
	})

	Describe("EnsureDefaultDatabaseEngine", func() {
		for i := range testReplicas {
			It(fmt.Sprintf("converts default database from Atomic to Replicated on %d", i), func(ctx context.Context) {
				id := v1.ClickHouseReplicaID{ShardID: 0, Index: i}

				By("running EnsureDefaultDatabaseEngine")

				err := cmd.EnsureDefaultDatabaseEngine(ctx, cmd.log, id)
				Expect(err).NotTo(HaveOccurred())

				By("verifying default database is now Replicated")

				dbs, err := cmd.Databases(ctx, id)
				Expect(err).NotTo(HaveOccurred())
				Expect(dbs).To(HaveKey("default"))
				Expect(dbs["default"].IsReplicated).To(BeTrue())
				Expect(dbs["default"].UUID).To(Equal(uuid.NewSHA1(uuid.Nil, []byte(cmd.cluster.SpecificName())).String()))
			})
		}
	})

	It("replicates databases to all replicas", func(ctx context.Context) {
		By("creating replicated database on replica 0")

		id0 := v1.ClickHouseReplicaID{ShardID: 0, Index: 0}
		dbUUID := uuid.New().String()
		q := `CREATE DATABASE testdb UUID '%s' ENGINE=Replicated('/clickhouse/databases/testdb', '{shard}', '{replica}')`
		err := cmd.conns[id0].Exec(ctx, fmt.Sprintf(q, dbUUID))
		Expect(err).NotTo(HaveOccurred())

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

		conn := cmd.conns[v1.ClickHouseReplicaID{ShardID: 0, Index: 0}]
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
		for id, conn := range cmd.conns {
			By(fmt.Sprintf("verifying data is replicated to replica %d", id.Index))

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
