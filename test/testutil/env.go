package testutil

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	// PollInterval is the default polling interval for Eventually-style waits.
	PollInterval     = 100 * time.Millisecond
	cacheSyncTimeout = 2 * time.Minute
)

// Env bundles the suite-level handles shared by the test helpers.
type Env struct {
	Client client.Client
	Config *rest.Config
	Dialer controllerutil.DialContextFunc

	cacheOnce sync.Once
	cache     cache.Cache
	cacheStop context.CancelFunc
	cacheDone chan struct{}
}

// Cache lazily starts a suite-lifetime informer cache and returns it.
// Register StopCache as a suite cleanup after the goroutine-leak assertion.
func (e *Env) Cache() cache.Cache {
	GinkgoHelper()

	e.cacheOnce.Do(func() {
		c, err := cache.New(e.Config, cache.Options{
			Scheme:                      e.Client.Scheme(),
			ReaderFailOnMissingInformer: true,
		})
		Expect(err).NotTo(HaveOccurred())

		ctx, stop := context.WithCancel(context.Background())
		e.cacheStop = stop
		e.cacheDone = make(chan struct{})

		go func() {
			defer close(e.cacheDone)
			defer GinkgoRecover()

			Expect(c.Start(ctx)).To(Succeed())
		}()

		syncCtx, cancel := context.WithTimeout(ctx, cacheSyncTimeout)
		defer cancel()

		Expect(c.WaitForCacheSync(syncCtx)).To(BeTrue())

		e.cache = c
	})

	// A failed first initialization poisons the Once: fail loudly instead of nil-panicking downstream.
	Expect(e.cache).NotTo(BeNil(), "informer cache failed to initialize")

	return e.cache
}

// StopCache stops the informer cache and waits for its goroutines to exit.
func (e *Env) StopCache() {
	GinkgoHelper()

	if e.cacheStop == nil {
		return
	}

	e.cacheStop()

	select {
	case <-e.cacheDone:
	case <-time.After(cacheSyncTimeout):
		Fail("informer cache did not stop within " + cacheSyncTimeout.String())
	}
}
