package clickhouse

import (
	"context"
	"errors"
	"net"

	"github.com/ClickHouse/clickhouse-go/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

type fakeConn struct {
	clickhouse.Conn

	closed bool
}

func (c *fakeConn) Close() error {
	c.closed = true

	return nil
}

var _ = Describe("replica connections", func() {
	var (
		id      v1.ClickHouseReplicaID
		cluster v1.ClickHouseCluster
	)

	BeforeEach(func() {
		id = v1.ClickHouseReplicaID{ShardID: 0, Index: 1}
		cluster = v1.ClickHouseCluster{Namespace: "default", Name: "test"}
	})

	It("should redial a replica that moved to another address", func() {
		entry := &connCacheEntry{conns: map[v1.ClickHouseReplicaID]clickhouse.Conn{}}

		var dialed []*fakeConn

		dial := func() (clickhouse.Conn, error) {
			conn := &fakeConn{}
			dialed = append(dialed, conn)

			return conn, nil
		}

		first, err := entry.Conn(id, "10.0.0.1:9001", dial)
		Expect(err).NotTo(HaveOccurred())

		again, err := entry.Conn(id, "10.0.0.1:9001", dial)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(BeIdenticalTo(first))

		moved, err := entry.Conn(id, "10.0.0.2:9001", dial)
		Expect(err).NotTo(HaveOccurred())
		Expect(moved).NotTo(BeIdenticalTo(first))
		Expect(dialed).To(HaveLen(2))
		Expect(dialed[0].closed).To(BeTrue())
	})

	It("should not dial a replica the resolver cannot address", func(ctx context.Context) {
		logger := controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
		dialer := func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("unexpected dial")
		}
		resolver := func(v1.ClickHouseReplicaID) (string, error) {
			return "", errors.New("replica pod has no IP")
		}

		cmd := newCommander(logger, &cluster, &corev1.Secret{}, dialer, resolver, nil)

		_, err := cmd.Probe(ctx, id)
		Expect(err).To(MatchError(ContainSubstring("replica pod has no IP")))
	})

	It("should observe only pooled replica connections", func(ctx context.Context) {
		logger := controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
		dialer := func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("unexpected dial")
		}
		cache := newConnCache()
		secret := &corev1.Secret{}
		newCommander(logger, &cluster, secret, dialer, nil, cache)

		cmd, ok := newObservingCommander(logger, &cluster, secret, dialer, cache)
		Expect(ok).To(BeTrue())

		_, err := cmd.Probe(ctx, id)
		Expect(err).To(MatchError(ContainSubstring("replica has no pooled connection")))
	})

	It("should resolve replicas by the loaded Pod IP", func() {
		r := &clickhouseReconciler{ReplicaState: map[v1.ClickHouseReplicaID]replicaState{}}

		_, err := r.replicaAddress(id)
		Expect(err).To(MatchError("replica pod is not loaded"))

		pod := &corev1.Pod{}
		r.ReplicaState[id] = replicaState{Pod: pod}

		_, err = r.replicaAddress(id)
		Expect(err).To(MatchError("replica pod has no IP"))

		pod.Status.PodIP = "10.0.0.7"

		Expect(r.replicaAddress(id)).To(Equal("10.0.0.7:9001"))
	})
})
