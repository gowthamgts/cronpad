package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/secrets"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func setup(t *testing.T, url string) (*testutil.Fixture, *Evaluator, *Notifier, sqlitegen.Job) {
	t.Helper()
	f := testutil.New(t)
	job := f.Job(t, "alert", f.Clock.Now())
	vault, err := secrets.New(base64.StdEncoding.EncodeToString(make([]byte, 32)), f.Store)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Wrap("whsec_"+base64.StdEncoding.EncodeToString([]byte("independent-signature-key")), "alert:ch_test")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(ChannelConfig{URL: url, Format: "generic"})
	if err = f.Store.Control().SaveAlertChannel(context.Background(), sqlitegen.SaveAlertChannelParams{ID: "ch_test", WorkspaceID: job.WorkspaceID, Kind: "webhook", Name: "test", Config: string(cfg), SigningSecret: secret, CreatedAt: f.Clock.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	rule(t, f, `{"all":true}`, `["run.failed","job.recovered","job.auto_disabled"]`, `{}`, 60000)
	e := &Evaluator{Store: f.Store, Writer: f.Writer, Clock: f.Clock, Config: f.Config, PublicURL: "https://cronpad.test"}
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Engine.Events = e
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		return sink.ExecResult{Outcome: sink.Fatal, Error: "boom"}
	})
	f.StartWriter(t)
	n := &Notifier{Store: f.Store, Writer: f.Writer, Clock: f.Clock, Vault: vault, Policy: netguard.Policy{AllowPrivateNetworks: true}, SigningKey: []byte("verification-key"), PublicURL: "https://cronpad.test"}
	return f, e, n, job
}
func rule(t *testing.T, f *testutil.Fixture, selector, events, conditions string, cooldown int64) {
	t.Helper()
	if err := f.Store.Control().SaveAlertRule(context.Background(), sqlitegen.SaveAlertRuleParams{ID: "ar_test", WorkspaceID: "ws_test", Name: "rule", JobSelector: selector, Events: events, Conditions: conditions, ChannelIds: `["ch_test"]`, CooldownMs: cooldown, Enabled: 1, CreatedAt: f.Clock.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}
func process(t *testing.T, f *testutil.Fixture, job sqlitegen.Job) {
	t.Helper()
	f.Run(t, func(ctx context.Context) error { return f.Engine.Process(ctx, structWork(job, f.Clock.Now())) })
}
func outbox(t *testing.T, f *testutil.Fixture) []historygen.AlertOutbox {
	t.Helper()
	rows, err := f.Store.HistoryReader().PendingAlerts(context.Background(), f.Clock.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func TestCooldownSuppressionAndRecovery(t *testing.T) {
	received := make(chan Payload, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		raw, _ := io.ReadAll(r.Body)
		key := []byte("independent-signature-key")
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(r.Header.Get("webhook-id") + "." + r.Header.Get("webhook-timestamp") + "."))
		_, _ = mac.Write(raw)
		want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(want), []byte(r.Header.Get("webhook-signature"))) {
			t.Error("independent signature check failed")
		}
		var p Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Error(err)
		}
		received <- p
		w.WriteHeader(200)
	}))
	defer server.Close()
	f, _, n, job := setup(t, server.URL)
	process(t, f, job)
	if len(outbox(t, f)) != 1 {
		t.Fatal("alert missing from result transaction")
	}
	f.Run(t, n.Poll)
	first := <-received
	if first.URL != "https://cronpad.test/runs/"+first.Run.ID {
		t.Fatalf("alert run link: %s", first.URL)
	}
	if first.SuppressedCount != 0 {
		t.Fatal(first)
	}
	for range 12 {
		f.Clock.Advance(time.Second)
		process(t, f, job)
	}
	var suppressed int
	if err := f.DBs.History.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM alert_outbox WHERE status='suppressed'").Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if suppressed != 12 {
		t.Fatal(suppressed)
	}
	f.Clock.Advance(time.Minute)
	process(t, f, job)
	f.Run(t, n.Poll)
	next := <-received
	if next.SuppressedCount != 12 || !strings.Contains(message(next), "+12 more failures suppressed") {
		t.Fatalf("%+v", next)
	}
	t.Logf("suppressed rows=%d; next payload suppressed_count=%d; message=%q", suppressed, next.SuppressedCount, message(next))
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult { return sink.ExecResult{Outcome: sink.Success} })
	f.Clock.Advance(time.Second)
	process(t, f, job)
	f.Run(t, n.Poll)
	if p := <-received; p.Event != events.JobRecovered {
		t.Fatal(p.Event)
	}
	f.Clock.Advance(time.Second)
	process(t, f, job)
	if len(outbox(t, f)) != 0 {
		t.Fatal("recovery repeated")
	}
}
func TestSelectorsAndConditions(t *testing.T) {
	for _, tc := range []struct {
		name, selector, ev, conditions string
		consecutive, attempt           int
		want                           bool
	}{
		{"all", `{"all":true}`, events.RunFailed, `{}`, 1, 0, true},
		{"job_match", `{"job_ids":["job_alert"]}`, events.RunFailed, `{}`, 1, 0, true},
		{"job_miss", `{"job_ids":["other"]}`, events.RunFailed, `{}`, 1, 0, false},
		{"tag_any", `{"tags":["other","critical"]}`, events.RunFailed, `{}`, 1, 0, true},
		{"tag_miss", `{"tags":["other"]}`, events.RunFailed, `{}`, 1, 0, false},
		{"consecutive_below", `{"all":true}`, events.RunFailed, `{"min_consecutive":3}`, 2, 0, false},
		{"consecutive_at", `{"all":true}`, events.RunFailed, `{"min_consecutive":3}`, 3, 0, true},
		{"attempt_below", `{"all":true}`, events.RunDeferred, `{"min_attempt":2}`, 0, 0, false},
		{"attempt_at", `{"all":true}`, events.RunDeferred, `{"min_attempt":2}`, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e, _, job := setup(t, "https://example.com")
			job.Tags = `["critical"]`
			rule(t, f, tc.selector, fmt.Sprintf(`["%s"]`, tc.ev), tc.conditions, 0)
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.Run(t, func(ctx context.Context) error {
				return f.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
					return e.evaluate(ctx, tx, events.Event{Type: tc.ev, WorkspaceID: job.WorkspaceID, JobID: job.ID, At: f.Clock.Now(), Attempt: tc.attempt}, job, historygen.Run{ID: "run_test"}, tc.consecutive)
				})
			})
			if got := len(outbox(t, f)) > 0; got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestOutboxBackoffFailsAfterEightAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	f, _, n, job := setup(t, server.URL)
	process(t, f, job)
	for attempt := int64(1); attempt <= 8; attempt++ {
		f.Run(t, n.Poll)
		var got int64
		var status string
		var next int64
		if err := f.DBs.History.Reader.QueryRowContext(t.Context(), "SELECT attempts,status,next_attempt_at FROM alert_outbox").Scan(&got, &status, &next); err != nil {
			t.Fatal(err)
		}
		if got != attempt {
			t.Fatal(got)
		}
		if attempt < 8 {
			if status != "pending" {
				t.Fatal(status)
			}
			f.Run(t, n.Poll)
			if calls.Load() != int32(attempt) {
				t.Fatal("retried before backoff")
			}
			f.Clock.Advance(time.UnixMilli(next).Sub(f.Clock.Now()))
		} else if status != "failed" {
			t.Fatal(status)
		}
	}
	if calls.Load() != 8 {
		t.Fatal(calls.Load())
	}
}
func TestSlackAndDiscordBodyShapes(t *testing.T) {
	p := Payload{Event: events.RunFailed, SuppressedCount: 12}
	p.Job.Name = "critical"
	for _, format := range []string{"slack", "discord"} {
		b, err := Body(p, format)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err = json.Unmarshal(b, &obj); err != nil {
			t.Fatal(err)
		}
		field := "text"
		if format == "discord" {
			field = "content"
			if obj["allowed_mentions"] == nil {
				t.Fatal("mentions not disabled")
			}
		}
		if !strings.Contains(obj[field].(string), "+12 more failures suppressed") {
			t.Fatal(string(b))
		}
	}
}
func TestOutboxRollsBackWithRunState(t *testing.T) {
	f, e, _, job := setup(t, "https://example.com")
	err := f.Store.WithTx(context.Background(), store.History, func(tx store.Tx) error {
		if err := e.evaluate(context.Background(), tx, events.Event{Type: events.RunFailed, WorkspaceID: job.WorkspaceID, At: f.Clock.Now()}, job, historygen.Run{ID: "rollback"}, 1); err != nil {
			return err
		}
		return sql.ErrNoRows
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if len(outbox(t, f)) != 0 {
		t.Fatal("outbox survived rollback")
	}
}
func TestAutoDisabledOwnerEmailNotify(t *testing.T) {
	for _, notify := range []bool{false, true} {
		for _, explicitRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("notify=%t/rule=%t", notify, explicitRule), func(t *testing.T) {
				f, e, _, job := setup(t, "https://example.com")
				if explicitRule {
					rule(t, f, `{"all":true}`, `["job.auto_disabled"]`, `{}`, 0)
				} else if _, err := f.Store.Control().DeleteAlertRule(context.Background(), sqlitegen.DeleteAlertRuleParams{ID: "ar_test", WorkspaceID: job.WorkspaceID}); err != nil {
					t.Fatal(err)
				}
				e.Config.Alerts.Email.Enabled = true
				e.Config.AutoDisable.Notify = notify
				e.Config.AutoDisable.ConsecutiveFailures = 1
				f.Engine.Config.AutoDisable.ConsecutiveFailures = 1
				if err := e.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
				process(t, f, job)
				owners, channels := 0, 0
				for _, row := range outbox(t, f) {
					if row.ChannelID == "owners" {
						owners++
					} else {
						channels++
					}
				}
				if (owners == 1) != notify || owners > 1 || (channels == 1) != explicitRule || channels > 1 {
					t.Fatalf("owner emails=%d, explicit channel alerts=%d", owners, channels)
				}
			})
		}
	}
}
func structWork(j sqlitegen.Job, at time.Time) worker.Work {
	return worker.Work{Occurrence: queue.Occurrence{JobID: j.ID, ScheduledAt: at, Version: j.Version, Trigger: "m"}, ClaimedAt: at}
}

func TestNonRunEventBusConsumerAndHostedDegradedRestriction(t *testing.T) {
	for _, kind := range []string{events.JobCompleted, events.LeaseExpired, events.Degraded} {
		t.Run(kind, func(t *testing.T) {
			f, e, _, job := setup(t, "https://example.com")
			rule(t, f, `{"all":true}`, fmt.Sprintf(`["%s"]`, kind), `{}`, 0)
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			workspace := job.WorkspaceID
			if kind == events.Degraded {
				workspace = ""
			}
			f.Run(t, func(ctx context.Context) error {
				e.Publish(ctx, events.Event{Type: kind, WorkspaceID: workspace, JobID: job.ID, At: f.Clock.Now(), Error: "test"})
				return f.Writer.Do(ctx, store.History, func(context.Context, store.Tx) error { return nil })
			})
			if rows := outbox(t, f); len(rows) != 1 || rows[0].EventType != kind {
				t.Fatalf("%+v", rows)
			}
			if kind == events.Degraded {
				e.Config.Mode = "hosted"
				f.Clock.Advance(time.Second)
				f.Run(t, func(ctx context.Context) error {
					e.Publish(ctx, events.Event{Type: kind, WorkspaceID: workspace, JobID: job.ID, At: f.Clock.Now()})
					return f.Writer.Do(ctx, store.History, func(context.Context, store.Tx) error { return nil })
				})
				if len(outbox(t, f)) != 1 {
					t.Fatal("hosted degraded event escaped restriction")
				}
			}
		})
	}
}
func TestTimeoutSLAAndDeferredPersistWithRun(t *testing.T) {
	f, e, _, job := setup(t, "https://example.com")
	rule(t, f, `{"all":true}`, `["run.timeout","run.sla_breached","run.deferred"]`, `{}`, 0)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error {
		return f.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			return e.RecordRun(ctx, tx, job, historygen.Run{ID: "run_timeout", WorkspaceID: job.WorkspaceID, JobID: job.ID, Status: "deferred", Attempts: 2, SlaBreached: 1}, sink.Timeout, true)
		})
	})
	rows := outbox(t, f)
	if len(rows) != 3 {
		t.Fatalf("want timeout, SLA, deferred; got %+v", rows)
	}
}

func TestDeliveryLeasePreventsConcurrentSendAndRecoversCrash(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	f, _, n, job := setup(t, server.URL)
	process(t, f, job)
	rows := outbox(t, f)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	err := f.Store.WithTx(context.Background(), store.History, func(tx store.Tx) error {
		count, err := tx.History.ClaimAlert(context.Background(), historygen.ClaimAlertParams{ID: rows[0].ID, DeliveryToken: "crashed", DeliveryLeaseUntil: f.Clock.Now().Add(30 * time.Second).UnixMilli(), Now: f.Clock.Now().UnixMilli()})
		if count != 1 {
			t.Error("failed to claim")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Run(t, n.Poll)
	if calls.Load() != 0 {
		t.Fatal("live delivery lease ignored")
	}
	f.Clock.Advance(31 * time.Second)
	f.Run(t, n.Poll)
	if calls.Load() != 1 {
		t.Fatal("expired lease not recovered")
	}
	f.Run(t, n.Poll)
	if calls.Load() != 1 {
		t.Fatal("sent row redelivered")
	}
}

func TestSLAAlertPersistedBeforeSinkCompletes(t *testing.T) {
	f, e, _, job := setup(t, "https://example.com")
	rule(t, f, `{"all":true}`, `["run.sla_breached"]`, `{}`, 0)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		close(entered)
		<-release
		return sink.ExecResult{Outcome: sink.Success}
	})
	at := f.Clock.Now().Add(-time.Hour)
	go func() { done <- f.Engine.Process(context.Background(), structWork(job, at)) }()
	f.Until(t, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	rows := outbox(t, f)
	close(release)
	f.Until(t, func() bool {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
			return true
		default:
			return false
		}
	})
	if len(rows) != 1 || rows[0].EventType != events.RunSLABreached {
		t.Fatalf("SLA missing before sink completed: %+v", rows)
	}
	if len(outbox(t, f)) != 1 {
		t.Fatal("SLA repeated at finish")
	}
}

func TestQuotaEventsReachRulesWithoutSMTP(t *testing.T) {
	f, e, _, job := setup(t, "https://example.org")
	rule(t, f, `{"all":true}`, `["workspace.quota_warning","workspace.quota_exceeded"]`, `{}`, 0)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1}, Store: f.Store, Clock: f.Clock}
	process(t, f, job)
	rows := outbox(t, f)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.ChannelID != "ch_test" {
			t.Fatal(row)
		}
	}
}

func TestAlertDashboardLinks(t *testing.T) {
	e := &Evaluator{PublicURL: "https://app.cronpad.com"}
	for _, tc := range []struct{ jobID, runID, want string }{
		{"job_1", "run_1", "https://app.cronpad.com/runs/run_1"},
		{"job_1", "", "https://app.cronpad.com/jobs/job_1"},
		{"", "", "https://app.cronpad.com/alerts"},
	} {
		p := e.payload(events.Event{}, sqlitegen.Job{ID: tc.jobID}, historygen.Run{ID: tc.runID})
		if p.URL != tc.want {
			t.Fatalf("dashboard link %s, want %s", p.URL, tc.want)
		}
	}
}
