package controller

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	// ObserverProbeTimeout bounds a single cluster probe within an observation tick.
	ObserverProbeTimeout = 10 * time.Second

	observerParallelism   = 8
	observerChannelBuffer = 1024
)

// Observer periodically probes externally observable cluster state and requests reconciliation when it changes.
type Observer[C client.Object] struct {
	name     string
	log      controllerutil.Logger
	interval time.Duration
	list     func(ctx context.Context) ([]C, error)
	probe    func(ctx context.Context, cluster C) string

	events chan event.GenericEvent
	seen   map[types.NamespacedName]string
}

// NewObserver creates an Observer that probes the listed clusters every interval.
func NewObserver[C client.Object](
	name string,
	log controllerutil.Logger,
	interval time.Duration,
	list func(ctx context.Context) ([]C, error),
	probe func(ctx context.Context, cluster C) string,
) *Observer[C] {
	return &Observer[C]{
		name:     name,
		log:      log.Named(name),
		interval: interval,
		list:     list,
		probe:    probe,
		events:   make(chan event.GenericEvent, observerChannelBuffer),
		seen:     map[types.NamespacedName]string{},
	}
}

// Source returns the watch source emitting an event for every observed state change.
func (o *Observer[C]) Source() source.Source {
	return source.Channel(o.events, &handler.EnqueueRequestForObject{})
}

// Start runs the observation loop until the context is canceled. It implements manager.Runnable.
func (o *Observer[C]) Start(ctx context.Context) error {
	o.log.Info("starting cluster observer", "interval", o.interval)

	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.log.Info("cluster observer stopped")
			return nil
		case <-ticker.C:
			o.observe(ctx)
		}
	}
}

func (o *Observer[C]) observe(ctx context.Context) {
	clusters, err := o.list(ctx)
	if err != nil {
		o.log.Error(err, "list clusters")
		return
	}

	candidates := make([]C, 0, len(clusters))
	for _, cluster := range clusters {
		if !cluster.GetDeletionTimestamp().IsZero() || controllerutil.PauseAnnotated(cluster) {
			continue
		}

		candidates = append(candidates, cluster)
	}

	digests := make([]string, len(candidates))

	var wg sync.WaitGroup

	sem := make(chan struct{}, observerParallelism)
	for i, cluster := range candidates {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			probeCtx, cancel := context.WithTimeout(ctx, ObserverProbeTimeout)
			defer cancel()

			digests[i] = o.probe(probeCtx, cluster)
		})
	}

	wg.Wait()

	alive := make(map[types.NamespacedName]bool, len(candidates))
	for i, cluster := range candidates {
		key := types.NamespacedName{Namespace: cluster.GetNamespace(), Name: cluster.GetName()}
		alive[key] = true

		previous, known := o.seen[key]

		// The informer initial list already enqueued every cluster; trigger only on observed changes.
		if !known || previous == digests[i] {
			o.seen[key] = digests[i]
			continue
		}

		o.log.Info("observed state changed, requesting reconciliation", "cluster", key.String())

		select {
		case o.events <- event.GenericEvent{Object: cluster}:
			o.seen[key] = digests[i]
		default:
			// Keep the previous digest so the dropped trigger is retried on the next tick.
			o.log.Warn("observer event channel is full, dropping trigger", "cluster", key.String())
		}
	}

	for key := range o.seen {
		if !alive[key] {
			delete(o.seen, key)
		}
	}
}
