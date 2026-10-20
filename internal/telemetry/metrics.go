package telemetry

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricNames is the complete registry, including instruments used by later consumers.
var MetricNames = []string{"cronpad.run.start_lag", "cronpad.run.queue_wait", "cronpad.run.sink_duration", "cronpad.run.e2e", "cronpad.dispatcher.tick_duration", "cronpad.janitor.duration", "cronpad.results.flush_duration", "cronpad.results.batch_size", "cronpad.runs", "cronpad.runs.sla_breached", "cronpad.attempts", "cronpad.dispatcher.claimed", "cronpad.dispatcher.backpressure", "cronpad.dispatcher.concurrency_deferred", "cronpad.leases.expired", "cronpad.leases.lost", "cronpad.janitor.deleted", "cronpad.reconciler.repaired", "cronpad.reconciler.backlogged", "cronpad.duplicates_suppressed", "cronpad.jobs.auto_disabled", "cronpad.alerts.sent", "cronpad.queue.errors", "cronpad.db.errors", "cronpad.queue.due_size", "cronpad.queue.inflight_size", "cronpad.queue.oldest_due_age", "cronpad.dispatcher.lock_held", "cronpad.jobs"}
var LagBuckets = []float64{1, 5, 10, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

type gaugePoint struct {
	value float64
	attrs []attribute.KeyValue
}

// Instruments registers exactly once per runtime and is shared by all components.
type Instruments struct {
	counters   map[string]metric.Int64Counter
	histograms map[string]metric.Float64Histogram
	gauges     map[string]metric.Float64ObservableGauge
	mu         sync.Mutex
	points     map[string]map[string]gaugePoint
}

func NewInstruments(m metric.Meter) (*Instruments, error) {
	i := &Instruments{counters: map[string]metric.Int64Counter{}, histograms: map[string]metric.Float64Histogram{}, gauges: map[string]metric.Float64ObservableGauge{}, points: map[string]map[string]gaugePoint{}}
	for _, name := range []string{"cronpad.runs", "cronpad.runs.sla_breached", "cronpad.attempts", "cronpad.dispatcher.claimed", "cronpad.dispatcher.backpressure", "cronpad.dispatcher.concurrency_deferred", "cronpad.leases.expired", "cronpad.leases.lost", "cronpad.janitor.deleted", "cronpad.reconciler.repaired", "cronpad.reconciler.backlogged", "cronpad.duplicates_suppressed", "cronpad.jobs.auto_disabled", "cronpad.alerts.sent", "cronpad.queue.errors", "cronpad.db.errors"} {
		v, e := m.Int64Counter(name)
		if e != nil {
			return nil, fmt.Errorf("counter %s: %w", name, e)
		}
		i.counters[name] = v
	}
	for _, name := range []string{"cronpad.run.start_lag", "cronpad.run.queue_wait", "cronpad.run.sink_duration", "cronpad.run.e2e", "cronpad.dispatcher.tick_duration", "cronpad.janitor.duration", "cronpad.results.flush_duration", "cronpad.results.batch_size"} {
		unit := "ms"
		buckets := LagBuckets
		if name == "cronpad.results.batch_size" {
			unit = "{command}"
			buckets = []float64{1, 10, 50, 100, 250, 500}
		}
		v, e := m.Float64Histogram(name, metric.WithUnit(unit), metric.WithExplicitBucketBoundaries(buckets...))
		if e != nil {
			return nil, fmt.Errorf("histogram %s: %w", name, e)
		}
		i.histograms[name] = v
	}
	observables := []metric.Observable{}
	for _, name := range []string{"cronpad.queue.due_size", "cronpad.queue.inflight_size", "cronpad.queue.oldest_due_age", "cronpad.dispatcher.lock_held", "cronpad.jobs"} {
		opts := []metric.Float64ObservableGaugeOption{}
		if name == "cronpad.queue.oldest_due_age" {
			opts = append(opts, metric.WithUnit("ms"))
		}
		v, e := m.Float64ObservableGauge(name, opts...)
		if e != nil {
			return nil, fmt.Errorf("gauge %s: %w", name, e)
		}
		i.gauges[name] = v
		observables = append(observables, v)
	}
	_, e := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		i.mu.Lock()
		defer i.mu.Unlock()
		for name, g := range i.gauges {
			points := i.points[name]
			if len(points) == 0 && name != "cronpad.jobs" {
				o.ObserveFloat64(g, 0)
			}
			for _, p := range points {
				o.ObserveFloat64(g, p.value, metric.WithAttributes(p.attrs...))
			}
		}
		return nil
	}, observables...)
	if e != nil {
		return nil, fmt.Errorf("gauge callback: %w", e)
	}
	return i, nil
}
func (i *Instruments) Add(ctx context.Context, name string, n int64, attrs ...attribute.KeyValue) {
	i.counters[name].Add(ctx, n, metric.WithAttributes(attrs...))
}
func (i *Instruments) Record(ctx context.Context, name string, v float64, attrs ...attribute.KeyValue) {
	i.histograms[name].Record(ctx, v, metric.WithAttributes(attrs...))
}
func (i *Instruments) Gauge(name string, v float64, attrs ...attribute.KeyValue) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.points[name] == nil {
		i.points[name] = map[string]gaugePoint{}
	}
	set := attribute.NewSet(attrs...)
	i.points[name][set.Encoded(attribute.DefaultEncoder())] = gaugePoint{v, append([]attribute.KeyValue(nil), attrs...)}
}
