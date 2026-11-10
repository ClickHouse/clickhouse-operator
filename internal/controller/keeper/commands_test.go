package keeper

import (
	"bufio"
	"context"
	"io"
	"net"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("queryKeeper", func() {
	It("should parse leader followers", func(ctx context.Context) {
		client, server := net.Pipe()

		go func() {
			defer GinkgoRecover()
			defer func() { _ = server.Close() }()

			command, err := bufio.NewReader(server).ReadString('\n')
			Expect(err).NotTo(HaveOccurred())
			Expect(command).To(HavePrefix(FLWCommand))

			_, err = io.WriteString(server, "zk_version\tv26.10.2.103-stable\n"+
				"zk_server_state\tleader\nzk_followers\t2\nzk_synced_followers\t1\n")
			Expect(err).NotTo(HaveOccurred())
		}()

		logger := util.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		status, err := queryKeeper(ctx, logger, client)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.ServerState).To(Equal(ModeLeader))
		Expect(status.Followers).To(Equal(2))
		Expect(status.SyncedFollowers).To(Equal(1))
	})
})
