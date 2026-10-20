package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestBaseLogContract(t *testing.T) {
	for _, format := range []string{"json", "logfmt"} {
		t.Run(format, func(t *testing.T) {
			for _, traced := range []bool{false, true} {
				var local, bridge bytes.Buffer
				logger := Setup(Config{Format: format}, &local, slog.NewJSONHandler(&bridge, nil))
				ctx := t.Context()
				sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
				if traced {
					ctx = trace.ContextWithSpanContext(ctx, sc)
				}
				logger.InfoContext(ctx, "application event")
				var record map[string]any
				if err := json.Unmarshal(bridge.Bytes(), &record); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{Service: "cronpad", Version: "unknown", Level: "INFO", MSg: "application event"} {
					if record[key] != want {
						t.Fatalf("bridge %s = %v, want %s", key, record[key], want)
					}
				}
				if record[Time] == nil {
					t.Fatal("missing timestamp", record)
				}
				if traced {
					if record[TraceID] != sc.TraceID().String() || record[SpanID] != sc.SpanID().String() {
						t.Fatal("trace context lost", record)
					}
				} else if record[TraceID] != nil || record[SpanID] != nil {
					t.Fatal("fabricated trace identifiers", record)
				}
				for _, key := range []string{JobID, RunID, WorkspaceID, RequestID} {
					if record[key] != nil {
						t.Fatal("fabricated operation identifiers", record)
					}
				}
				if format == "json" {
					if !bytes.Equal(local.Bytes(), bridge.Bytes()) {
						t.Fatalf("local/bridge mismatch: %s != %s", &local, &bridge)
					}
				} else {
					for _, field := range []string{"time=", "level=INFO", `msg="application event"`, "service=cronpad", "version=unknown"} {
						if !strings.Contains(local.String(), field) {
							t.Fatalf("missing %s: %s", field, &local)
						}
					}
					for _, key := range []string{TraceID, SpanID} {
						if strings.Contains(local.String(), key+"=") != traced {
							t.Fatalf("incorrect trace fields: %s", &local)
						}
					}
				}
			}
		})
	}
}
