package telemetry

import (
	"context"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestUnconfiguredNoOp(t *testing.T) {
	before := otel.GetTracerProvider()
	r, err := Bootstrap(context.Background(), config.ConfigTelemetry{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Logs != nil || r.Metrics != nil || otel.GetTracerProvider() != before {
		t.Fatal("disabled telemetry changed runtime")
	}
	if err = r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestPrometheusBootstrap(t *testing.T) {
	r, err := Bootstrap(context.Background(), config.ConfigTelemetry{ServiceName: "test", PrometheusAddr: ":9090"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Shutdown(context.Background()) }()
	w := httptest.NewRecorder()
	r.Metrics.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
