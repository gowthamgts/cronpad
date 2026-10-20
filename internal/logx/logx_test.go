package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/trace"
)

func tracedContext() context.Context {
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}}))
}
func sinkLog(ctx context.Context, l *slog.Logger) {
	l.InfoContext(ctx, "request delivered", Sink, "webhook")
}
func TestContextDeepAndRedaction(t *testing.T) {
	var out bytes.Buffer
	l := Setup(Config{Level: "info", Format: "json", Service: "cronpad", Version: "test"}, &out, nil)
	ctx := With(tracedContext(), JobID, "job_1", RunID, "run_1", Occ, "job_1|123|0|1|s", "authorization", "Bearer abc", "nested", slog.GroupValue(slog.String("password", "abc")), "innocent", Secret("hidden"))
	sinkLog(ctx, l)
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{JobID, RunID, Occ, TraceID, SpanID, Service, Version} {
		if record[key] == nil {
			t.Fatalf("missing %s", key)
		}
	}
	for _, s := range []string{"Bearer abc", "hidden", `"password":"abc"`} {
		if strings.Contains(out.String(), s) {
			t.Fatalf("leak: %s", out.String())
		}
	}
	if record["authorization"] != "[REDACTED]" {
		t.Fatal(record)
	}
}
func TestCopyOnWrite(t *testing.T) {
	ctx := With(context.Background(), JobID, "parent")
	sibling := With(ctx, RunID, "sibling")
	attrs := Attrs(ctx)
	attrs[0] = slog.String(JobID, "mutated")
	if Attrs(ctx)[0].Value.String() != "parent" {
		t.Fatal("Attrs mutated parent")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			child := With(ctx, RunID, "child")
			if len(Attrs(child)) != 2 {
				t.Error("child attrs")
			}
		})
	}
	wg.Wait()
	if len(Attrs(ctx)) != 1 || Attrs(sibling)[1].Value.String() != "sibling" {
		t.Fatal("parent/sibling mutated")
	}
}
func TestFormatsSameKeys(t *testing.T) {
	var outputs [2]bytes.Buffer
	for i, format := range []string{"json", "logfmt"} {
		l := Setup(Config{Format: format, Level: "info", Service: "cronpad", Version: "test"}, &outputs[i], nil)
		sinkLog(With(tracedContext(), JobID, "job_1", RunID, "run_1", Occ, "occ_1"), l)
	}
	var record map[string]any
	if err := json.Unmarshal(outputs[0].Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	var jsonKeys, textKeys []string
	for key := range record {
		jsonKeys = append(jsonKeys, key)
	}
	for _, m := range regexp.MustCompile(`(?:^| )([a-z_]+)=`).FindAllStringSubmatch(outputs[1].String(), -1) {
		textKeys = append(textKeys, m[1])
	}
	sort.Strings(jsonKeys)
	sort.Strings(textKeys)
	if !reflect.DeepEqual(jsonKeys, textKeys) {
		t.Fatalf("json %v logfmt %v", jsonKeys, textKeys)
	}
}
func TestHandlerWithGroupAttrsAndLevel(t *testing.T) {
	var out bytes.Buffer
	l := Setup(Config{Format: "json", Level: "warn"}, &out, nil).WithGroup("worker").With(Attempt, 1)
	l.InfoContext(context.Background(), "hidden")
	if out.Len() != 0 {
		t.Fatal("level ignored")
	}
	l.WarnContext(With(context.Background(), JobID, "job_1"), "warning")
	if !strings.Contains(out.String(), `"worker":{"attempt":1,"job_id":"job_1"}`) {
		t.Fatal(out.String())
	}
}
func BenchmarkWorkerLogging(b *testing.B) {
	l := Setup(Config{Format: "json", Level: "info", Service: "cronpad", Version: "test"}, io.Discard, nil)
	ctx := With(tracedContext(), JobID, "job_1", RunID, "run_1", Occ, "job_1|123|0|1|s", Attempt, 0, WorkspaceID, "ws_1")
	clk := clockwork.NewFakeClock()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := slog.NewRecord(clk.Now(), slog.LevelInfo, "run finished", 0)
		r.Add(Sink, "webhook", Outcome, "success", DurationMS, 104)
		if err := l.Handler().Handle(ctx, r); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBridgeRedactsAllSources(t *testing.T) {
	var out bytes.Buffer
	bridge := slog.NewJSONHandler(&out, nil)
	l := Setup(Config{Format: "json", Level: "info"}, io.Discard, bridge).With("access_token", "static")
	ctx := With(context.Background(), "password", "ctx-value")
	l.InfoContext(ctx, "test bridge", "nested", slog.GroupValue(slog.String("signature", "nested-value")), "secret_type", Secret("secret-value"))
	for _, value := range []string{"static", "ctx-value", "nested-value", "secret-value"} {
		if strings.Contains(out.String(), value) {
			t.Fatalf("bridge leaked %s: %s", value, out.String())
		}
	}
}
