package keeper

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("queryKeeper", func() {
	It("should parse leader synced followers", func(ctx context.Context) {
		logger := util.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
		conn := fakeConn{data: []byte("zk_server_state\tleader\nzk_followers\t2\nzk_synced_followers\t1\n")}

		status, err := queryKeeper(ctx, logger, conn)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.SyncedFollowers).To(Equal(1))
	})
})
