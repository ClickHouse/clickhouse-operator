package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("Observer", func() {
	var (
		clusters []*v1.KeeperCluster
		digests  map[string]string
		observer *Observer[*v1.KeeperCluster]
	)

	cluster := func(name string) *v1.KeeperCluster {
		return &v1.KeeperCluster{Name: name, Namespace: "default"}
	}

	drain := func() []string {
		var triggered []string
		for {
			select {
			case e := <-observer.events:
				triggered = append(triggered, e.Object.GetName())
			default:
				return triggered
			}
		}
	}

	BeforeEach(func() {
		clusters = nil
		digests = map[string]string{}
		observer = NewObserver("test",
			controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))),
			time.Minute,
			func(context.Context) ([]*v1.KeeperCluster, error) { return clusters, nil },
			func(_ context.Context, c *v1.KeeperCluster) string { return digests[c.Name] },
		)
	})

	It("should trigger only on digest changes", func(ctx context.Context) {
		clusters = []*v1.KeeperCluster{cluster("a"), cluster("b")}
		digests["a"], digests["b"] = "1", "1"

		By("first observation establishes the baseline without triggering")
		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())

		By("unchanged digests do not trigger")
		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())

		By("a changed digest triggers exactly the changed cluster")

		digests["a"] = "2"

		observer.observe(ctx)
		Expect(drain()).To(ConsistOf("a"))

		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())
	})

	It("should skip paused and deleting clusters", func(ctx context.Context) {
		paused := cluster("paused")
		paused.Annotations = map[string]string{controllerutil.AnnotationPauseReconciliation: "true"}
		deleting := cluster("deleting")
		deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		clusters = []*v1.KeeperCluster{paused, deleting}
		digests["paused"], digests["deleting"] = "1", "1"

		observer.observe(ctx)
		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())
		Expect(observer.seen).To(BeEmpty())
	})

	It("should re-baseline clusters that vanish and return", func(ctx context.Context) {
		clusters = []*v1.KeeperCluster{cluster("a")}
		digests["a"] = "1"

		observer.observe(ctx)

		By("cluster disappears")

		clusters = nil

		observer.observe(ctx)
		Expect(observer.seen).To(BeEmpty())

		By("cluster returns with a different digest: baseline again, no trigger")

		clusters = []*v1.KeeperCluster{cluster("a")}
		digests["a"] = "2"

		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())
	})

	It("should treat probe failure classes as state changes", func(ctx context.Context) {
		clusters = []*v1.KeeperCluster{cluster("a")}
		digests["a"] = "healthy"

		observer.observe(ctx)

		digests["a"] = "unreachable"

		observer.observe(ctx)
		Expect(drain()).To(ConsistOf("a"))

		observer.observe(ctx)
		Expect(drain()).To(BeEmpty())
	})

	It("should keep a full event channel from blocking observation", func(ctx context.Context) {
		var e event.GenericEvent

		clusters = []*v1.KeeperCluster{cluster("a")}
		digests["a"] = "1"

		observer.observe(ctx)

		for i := range observerChannelBuffer + 8 {
			digests["a"] = time.Duration(i).String()

			observer.observe(ctx)
		}

		Expect(observer.events).To(HaveLen(observerChannelBuffer))
		e = <-observer.events
		Expect(e.Object.GetName()).To(Equal("a"))

		By("retrying the dropped trigger once the channel has room, without a new change")
		observer.observe(ctx)
		Expect(observer.events).To(HaveLen(observerChannelBuffer))
	})
})
