package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

// Assert fields on each actual record, not on the concatenated output: one
// correctly enriched message must not hide another that lost its context.
func TestExecutionLogContract(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			for _, outcome := range []string{"success", "fatal", "panic"} {
				t.Run(outcome, func(t *testing.T) {
					f := testutil.New(t)
					f.StartWriter(t)
					job := f.Job(t, "log-contract", f.Clock.Now())
					o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
					var output bytes.Buffer
					f.Engine.Log = logx.Setup(logx.Config{Service: "cronpad", Version: "test"}, &output, nil)
					f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, _ sink.ExecRequest) sink.ExecResult {
						f.Engine.Log.InfoContext(ctx, "nested execution operation")
						if outcome == "panic" {
							panic("contract panic")
						}
						if outcome == "fatal" {
							return sink.ExecResult{Outcome: sink.Fatal, Code: 400, Error: "contract failure"}
						}
						return sink.ExecResult{Outcome: sink.Success, Code: 200}
					})
					sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
					f.Run(t, func(ctx context.Context) error {
						return f.Engine.Process(trace.ContextWithSpanContext(ctx, sc), worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
					})
					decoder := json.NewDecoder(&output)
					finished, nested, panicked := 0, 0, 0
					for decoder.More() {
						var record map[string]any
						if err := decoder.Decode(&record); err != nil {
							t.Fatal(err)
						}
						for key, want := range map[string]any{
							logx.Service: "cronpad", logx.Version: "test", logx.JobID: job.ID,
							logx.WorkspaceID: job.WorkspaceID, logx.Occ: o.Format(), logx.Sink: "webhook",
							logx.Attempt: float64(0), logx.Trigger: "s",
							logx.TraceID: sc.TraceID().String(), logx.SpanID: sc.SpanID().String(),
						} {
							if record[key] != want {
								t.Fatalf("%s = %v, want %v: %v", key, record[key], want, record)
							}
						}
						for _, key := range []string{logx.Time, logx.Level, logx.MSg, logx.RunID, logx.ScheduledAt} {
							if record[key] == nil || record[key] == "" {
								t.Fatalf("missing %s: %v", key, record)
							}
						}
						switch record[logx.MSg] {
						case "nested execution operation":
							nested++
						case "sink panicked":
							panicked++
						case "run finished":
							finished++
							for _, key := range []string{logx.StatusCode, logx.DurationMS, logx.StartLagMS, logx.SLABreached, logx.Error} {
								if _, ok := record[key]; !ok {
									t.Fatalf("missing completion field %s: %v", key, record)
								}
							}
							wantOutcome, wantStatus := string(sink.Success), float64(200)
							switch outcome {
							case "fatal":
								wantOutcome, wantStatus = string(sink.Fatal), 400
							case "panic":
								wantOutcome, wantStatus = string(sink.Fatal), 0
							}
							if record[logx.Outcome] != wantOutcome || record[logx.StatusCode] != wantStatus {
								t.Fatalf("incorrect outcome/status: %v", record)
							}
							if outcome == "success" && record[logx.Error] != "" {
								t.Fatalf("unexpected success error: %v", record)
							}
							if outcome != "success" && record[logx.Error] == "" {
								t.Fatalf("missing failure detail: %v", record)
							}
						}
					}
					if finished != 1 || nested != 1 || (outcome == "panic" && panicked != 1) {
						t.Fatalf("finished=%d nested=%d panicked=%d", finished, nested, panicked)
					}
				})
			}
		})
	}
}
