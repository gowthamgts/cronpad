package telemetry

import (
	"context"
	"slices"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricNamesRegistered(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	i, err := NewInstruments(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	for name := range i.counters {
		i.Add(context.Background(), name, 1)
	}
	for name := range i.histograms {
		i.Record(context.Background(), name, 1)
	}
	i.Gauge("cronpad.jobs", 1, attribute.String("state", "active"))
	var data metricdata.ResourceMetrics
	if err = reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			registered[metric.Name] = true
		}
	}
	for _, name := range MetricNames {
		if !registered[name] {
			t.Errorf("missing registered metric %s", name)
		}
	}
	for name := range registered {
		if !slices.Contains(MetricNames, name) {
			t.Error("unlisted registered metric", name)
		}
	}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "cronpad.run.start_lag" {
				hist := m.Data.(metricdata.Histogram[float64])
				if !slices.Equal(hist.DataPoints[0].Bounds, LagBuckets) {
					t.Fatal(hist.DataPoints[0].Bounds)
				}
			}
		}
	}
}
