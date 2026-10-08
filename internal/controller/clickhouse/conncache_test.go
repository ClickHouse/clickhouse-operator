package clickhouse

import (
	"github.com/ClickHouse/clickhouse-go/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("connCache", func() {
	log := controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	key := types.NamespacedName{Namespace: "default", Name: "cached"}

	It("should expose entries read-only through GetCurrent", func() {
		cache := newConnCache()

		_, ok := cache.TryGet(key, "hash-a")
		Expect(ok).To(BeFalse(), "GetCurrent must not create entries")

		entry := cache.Get(key, "hash-a", log)
		current, ok := cache.TryGet(key, "hash-a")
		Expect(ok).To(BeTrue())
		Expect(current).To(BeIdenticalTo(entry))

		_, ok = cache.TryGet(key, "hash-b")
		Expect(ok).To(BeFalse(), "rotated credentials must not match")

		current, ok = cache.TryGet(key, "hash-a")
		Expect(ok).To(BeTrue())
		Expect(current).To(BeIdenticalTo(entry), "GetCurrent must not rotate the entry")
	})

	It("should refuse dialing through a closed entry", func() {
		cache := newConnCache()
		stale := cache.Get(key, "hash-a", log)

		By("rotating the credentials while the stale entry is still held")

		fresh := cache.Get(key, "hash-b", log)
		Expect(fresh).NotTo(BeIdenticalTo(stale))

		_, err := stale.Conn(v1.ClickHouseReplicaID{}, func() (clickhouse.Conn, error) {
			Fail("a closed entry must not dial")
			return nil, nil
		})
		Expect(err).To(MatchError("connection cache entry is closed"))
	})
})
