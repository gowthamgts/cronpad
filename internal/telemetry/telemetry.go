// Package telemetry owns optional exporters and their shutdown lifecycle.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	loggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	loghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	metricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	metrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	tracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	tracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	prom "go.opentelemetry.io/otel/exporters/prometheus"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/gowthamgts/cronpad/internal/config"
)

type Runtime struct {
	Logs     slog.Handler
	Metrics  http.Handler
	shutdown []func(context.Context) error
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(r.shutdown) - 1; i >= 0; i-- {
		errs = append(errs, r.shutdown[i](ctx))
	}
	return errors.Join(errs...)
}

// Bootstrap leaves OTel globals untouched and opens no sockets when unconfigured.
func Bootstrap(ctx context.Context, c config.ConfigTelemetry) (r *Runtime, err error) {
	r = &Runtime{}
	defer func() {
		if err != nil {
			_ = r.Shutdown(ctx)
		}
	}()
	if c.OTLPEndpoint == "" && c.PrometheusAddr == "" {
		return r, nil
	}
	res := resource.NewWithAttributes("", attribute.String("service.name", c.ServiceName))
	var readers []sdkmetric.Option
	if c.OTLPEndpoint != "" {
		var te sdktrace.SpanExporter
		var me sdkmetric.Exporter
		if c.OTLPProtocol == "http" {
			te, err = tracehttp.New(ctx, tracehttp.WithEndpointURL(c.OTLPEndpoint))
			if err == nil {
				me, err = metrichttp.New(ctx, metrichttp.WithEndpointURL(c.OTLPEndpoint))
			}
		} else {
			te, err = tracegrpc.New(ctx, tracegrpc.WithEndpointURL(c.OTLPEndpoint))
			if err == nil {
				me, err = metricgrpc.New(ctx, metricgrpc.WithEndpointURL(c.OTLPEndpoint))
			}
		}
		if err != nil {
			if te != nil {
				_ = te.Shutdown(ctx)
			}
			return r, fmt.Errorf("OTLP exporters: %w", err)
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(te), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.TracesSampleRatio))))
		otel.SetTracerProvider(tp)
		r.shutdown = append(r.shutdown, tp.Shutdown)
		readers = append(readers, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)))
		if c.OTLPLogs {
			var le sdklog.Exporter
			if c.OTLPProtocol == "http" {
				le, err = loghttp.New(ctx, loghttp.WithEndpointURL(c.OTLPEndpoint))
			} else {
				le, err = loggrpc.New(ctx, loggrpc.WithEndpointURL(c.OTLPEndpoint))
			}
			if err != nil {
				_ = me.Shutdown(ctx)
				return r, fmt.Errorf("OTLP logs: %w", err)
			}
			lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(le)))
			r.Logs = otelslog.NewHandler(c.ServiceName, otelslog.WithLoggerProvider(lp))
			r.shutdown = append(r.shutdown, lp.Shutdown)
		}
	}
	if c.PrometheusAddr != "" {
		registry := prometheus.NewRegistry()
		pe, e := prom.New(prom.WithRegisterer(registry))
		if e != nil {
			return r, fmt.Errorf("prometheus exporter: %w", e)
		}
		readers = append(readers, sdkmetric.WithReader(pe))
		r.Metrics = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	}
	readers = append(readers, sdkmetric.WithResource(res))
	mp := sdkmetric.NewMeterProvider(readers...)
	otel.SetMeterProvider(mp)
	r.shutdown = append(r.shutdown, mp.Shutdown)
	return r, nil
}
