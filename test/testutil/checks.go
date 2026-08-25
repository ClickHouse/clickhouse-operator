package testutil

import (
	"context"

	"github.com/ClickHouse/clickhouse-go/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// ClickHouseRWChecks writes a new data batch and reads back all batches written so far.
func (e *Env) ClickHouseRWChecks(
	ctx context.Context, cr *v1.ClickHouseCluster, checksDone *int, auth ...clickhouse.Auth,
) {
	ExpectWithOffset(1, e.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	Expect(len(auth)).To(Or(Equal(0), Equal(1)))
	chClient, err := NewClickHouseClient(ctx, e.Dialer, cr, auth...)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	defer chClient.Close()

	if *checksDone == 0 {
		By("creating test database")
		Expect(chClient.CreateDatabase(ctx)).To(Succeed())
		By("checking default database replicated")
		Expect(chClient.CheckDefaultDatabasesReplicated(ctx)).To(Succeed())
	}

	runRWChecks(
		func(order int) error { return chClient.CheckWrite(ctx, order) },
		func(order int) error { return chClient.CheckRead(ctx, order) },
		checksDone,
	)
}

// KeeperRWChecks writes a new data batch and reads back all batches written so far.
func (e *Env) KeeperRWChecks(ctx context.Context, cr *v1.KeeperCluster, checksDone *int) {
	ExpectWithOffset(1, e.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")

	cli, err := NewKeeperClient(ctx, e.Dialer, cr)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	defer cli.Close()

	runRWChecks(cli.CheckWrite, cli.CheckRead, checksDone)
}

func runRWChecks(write, read func(order int) error, checksDone *int) {
	By("writing new test data")
	ExpectWithOffset(2, write(*checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")

	for i := range *checksDone {
		ExpectWithOffset(2, read(i)).To(Succeed(), "check read %d failed", i)
	}
}
