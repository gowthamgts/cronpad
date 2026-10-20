package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"
	"github.com/gowthamgts/cronpad/internal/store"
)

func newJobs(t *testing.T) (*Jobs, http.Handler) {
	t.Helper()
	ctx := context.Background()
	c, e := config.Load(ctx, "", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.Auth.DevLogin = true
	c.Sinks.Webhook.AllowPrivateNetworks = true
	clk := clockwork.NewFakeClockAt(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	dbs, e := store.Open(ctx, filepath.Join(dir, "control.db"), filepath.Join(dir, "history.db"), time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = dbs.Close() })
	s := store.New(dbs)
	q := queue.NewMemory(clk)
	scheduler := &QueueScheduler{Store: s, Queue: q, Clock: clk, WakeCh: make(chan struct{}, 1)}
	j := &Jobs{Store: s, Queue: q, Scheduler: scheduler, Clock: clk, Config: c, Webhook: webhook.New(netguard.Policy{AllowPrivateNetworks: true}, clk, "test", 1024, time.Second)}
	r := chi.NewRouter()
	j.testAuth = DevAuth(s, clk, true)
	j.Mount(r)
	return j, r
}
func jobRequest(t *testing.T, h http.Handler, method, path, user, body string, status int) map[string]any {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBufferString(body))
	if user != "" {
		r.Header.Set("X-Dev-User", user)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	var out map[string]any
	if status != 204 {
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
	}
	return out
}

const validJob = `{"name":"report","schedule":{"expr":"every day at 9am","timezone":"Asia/Kolkata"},"sink":{"kind":"webhook","config":{"url":"https://example.org"}},"retry":{"max_attempts":5},"tags":["reports"]}`

func TestScheduleValidationInheritsContext(t *testing.T) {
	jobs, _ := newJobs(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	in := jobSchedule{Expr: "@every 1m", Timezone: "UTC"}
	if err := jobs.validate.VarWithValueCtx(ctx, in.Expr, in, "required,schedule"); err == nil {
		t.Fatal("schedule validation ignored caller cancellation")
	}
}

func TestJobsLifecycle(t *testing.T) {
	j, h := newJobs(t)
	out := jobRequest(t, h, "POST", "/api/v1/jobs", "alice", validJob, 201)
	jobID := out["id"].(string)
	path := "/api/v1/jobs/" + jobID
	if out["signing_secret"] == nil || out["next_run_at"] == nil || out["version"] != float64(1) {
		t.Fatal(out)
	}
	if out["retry"].(map[string]any)["initial_interval"] != "10s" {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "GET", path, "alice", "", 200)
	if len(out["next_runs"].([]any)) != 5 || out["signing_secret"] != nil {
		t.Fatal(out)
	}
	// Every job-specific endpoint must hide a job in a different workspace.
	for _, tc := range []struct{ method, suffix string }{{"GET", ""}, {"PATCH", ""}, {"DELETE", ""}, {"POST", "/pause"}, {"POST", "/resume"}, {"POST", "/enable"}, {"POST", "/trigger"}, {"POST", "/test"}, {"POST", "/rotate-signing-secret"}, {"GET", "/runs"}} {
		t.Run("cross_workspace_"+tc.method+tc.suffix, func(t *testing.T) { jobRequest(t, h, tc.method, path+tc.suffix, "bob", "{}", 404) })
	}
	out = jobRequest(t, h, "GET", "/api/v1/jobs?tag=reports&q=REPORT", "alice", "", 200)
	if len(out["data"].([]any)) != 1 {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "GET", "/api/v1/jobs", "bob", "", 200)
	if len(out["data"].([]any)) != 0 {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "PATCH", path, "alice", `{"name":"updated","retry":{"jitter":0}}`, 200)
	if out["version"] != float64(2) || out["retry"].(map[string]any)["max_attempts"] != float64(5) {
		t.Fatal(out)
	}
	v, _ := j.Queue.Claim(context.Background(), j.Clock.Now().Add(48*time.Hour), 100, time.Minute)
	if len(v) != 1 {
		t.Fatal("stale queued occurrence", v)
	}
	occ, e := queue.ParseOccurrence(v[0].Member)
	if e != nil || occ.Version != 2 {
		t.Fatal(occ, e)
	}
	jobRequest(t, h, "POST", path+"/pause", "alice", "", 200)
	out = jobRequest(t, h, "GET", path, "alice", "", 200)
	if out["next_run_at"] != nil || len(out["next_runs"].([]any)) != 0 {
		t.Fatal(out)
	}
	jobRequest(t, h, "POST", path+"/pause", "alice", "", 409)
	out = jobRequest(t, h, "POST", path+"/trigger", "alice", "", 202)
	runID := out["run_id"].(string)
	jobRequest(t, h, "GET", "/api/v1/runs/"+runID, "bob", "", 404)
	jobRequest(t, h, "GET", "/api/v1/runs/"+runID, "alice", "", 200)
	out = jobRequest(t, h, "GET", path+"/runs", "alice", "", 200)
	if len(out["data"].([]any)) != 1 {
		t.Fatal(out)
	}
	jobRequest(t, h, "POST", path+"/resume", "alice", "", 200)
	jobRequest(t, h, "POST", path+"/enable", "alice", "", 409)
	out = jobRequest(t, h, "POST", path+"/rotate-signing-secret", "alice", "", 200)
	if out["signing_secret"] == nil {
		t.Fatal(out)
	}
	jobRequest(t, h, "DELETE", path, "alice", "", 204)
	jobRequest(t, h, "GET", path, "alice", "", 404)
	jobRequest(t, h, "GET", "/api/v1/runs/"+runID, "alice", "", 200)
}
func TestJobValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"missing", `{}`, 400}, {"bad_schedule", `{"name":"x","schedule":{"expr":"gibberish"},"sink":{"kind":"webhook","config":{"url":"https://example.org"}}}`, 400}, {"unknown_secret", `{"name":"x","schedule":{"expr":"@daily"},"sink":{"kind":"webhook","config":{"url":"https://example.org","headers":{"Authorization":"{{secret.TOKEN}}"}}}}`, 400}, {"command", `{"name":"x","schedule":{"expr":"@daily"},"sink":{"kind":"command","config":{}}}`, 400}, {"bad_duration", `{"name":"x","schedule":{"expr":"@daily"},"sink":{"kind":"webhook","config":{"url":"https://example.org"}},"timeout":"0s"}`, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			_, h := newJobs(t)
			jobRequest(t, h, "POST", "/api/v1/jobs", "", tc.body, tc.status)
		})
	}
	j, h := newJobs(t)
	j.Config.Limits.MaxJobsPerWorkspace = 1
	jobRequest(t, h, "POST", "/api/v1/jobs", "", validJob, 201)
	jobRequest(t, h, "POST", "/api/v1/jobs", "", validJob, 403)
	j.Config.Auth.DevLogin = false // Mount captures auth setting; verify on a fresh router.
	r := chi.NewRouter()
	j.Mount(r)
	jobRequest(t, r, "GET", "/api/v1/jobs", "", "", 401)
}
func TestSinkTestDoesNotRecord(t *testing.T) {
	j, h := newJobs(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	body := `{"name":"test","schedule":{"expr":"@daily"},"sink":{"kind":"webhook","config":{"url":"` + target.URL + `"}}}`
	out := jobRequest(t, h, "POST", "/api/v1/jobs/test", "alice", body, 200)
	if out["outcome"] != "success" {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "POST", "/api/v1/jobs", "alice", body, 201)
	path := "/api/v1/jobs/" + out["id"].(string)
	out = jobRequest(t, h, "POST", path+"/test", "alice", "", 200)
	if out["outcome"] != "success" {
		t.Fatal(out)
	}
	j.Config.Sinks.Webhook.Enabled = false
	jobRequest(t, h, "POST", path+"/test", "alice", "", 403)
	out = jobRequest(t, h, "GET", path+"/runs", "alice", "", 200)
	if len(out["data"].([]any)) != 0 {
		t.Fatal(out)
	}
}
func TestCreateIdempotencyAndAnchor(t *testing.T) {
	j, h := newJobs(t)
	request := func(user, key, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/jobs", bytes.NewBufferString(body))
		r.Header.Set("X-Dev-User", user)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	first := request("alice", "key", validJob, 201)
	again := request("alice", "key", validJob, 201)
	if first["id"] != again["id"] || first["signing_secret"] != again["signing_secret"] {
		t.Fatal(first, again)
	}
	request("alice", "key", strings.Replace(validJob, "report", "different", 1), 409)
	other := request("bob", "key", validJob, 201)
	if first["id"] == other["id"] {
		t.Fatal("cross workspace idempotency")
	}
	body := `{"name":"relative","schedule":{"expr":"in 2 hours"},"sink":{"kind":"webhook","config":{"url":"https://example.org"}}}`
	out := jobRequest(t, h, "POST", "/api/v1/jobs", "alice", body, 201)
	path := "/api/v1/jobs/" + out["id"].(string)
	expected := j.Clock.Now().Add(2 * time.Hour)
	j.Clock.(*clockwork.FakeClock).Advance(time.Hour)
	out = jobRequest(t, h, "GET", path, "alice", "", 200)
	if out["next_run_at"] != expected.Format(time.RFC3339) {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "PATCH", path, "alice", `{"schedule":{"expr":"in 3 hours"}}`, 200)
	if out["next_run_at"] != j.Clock.Now().Add(3*time.Hour).Format(time.RFC3339) {
		t.Fatal(out)
	}
	j.Clock.(*clockwork.FakeClock).Advance(25 * time.Hour)
	renewed := request("alice", "key", validJob, 201)
	if renewed["id"] == first["id"] {
		t.Fatal("expired idempotency replay")
	}
}
func TestRunsPaginationAndFilters(t *testing.T) {
	_, h := newJobs(t)
	a := jobRequest(t, h, "POST", "/api/v1/jobs", "alice", validJob, 201)
	path := "/api/v1/jobs/" + a["id"].(string)
	jobRequest(t, h, "POST", path+"/trigger", "alice", "", 202)
	out := jobRequest(t, h, "GET", "/api/v1/runs?tag=reports&job_id="+a["id"].(string), "alice", "", 200)
	if len(out["data"].([]any)) != 1 {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "GET", "/api/v1/runs?tag=absent", "alice", "", 200)
	if len(out["data"].([]any)) != 0 {
		t.Fatal(out)
	}
	jobRequest(t, h, "POST", "/api/v1/jobs", "alice", validJob, 201)
	out = jobRequest(t, h, "GET", "/api/v1/jobs?limit=1", "alice", "", 200)
	if len(out["data"].([]any)) != 1 || out["next_cursor"] == nil {
		t.Fatal(out)
	}
	out = jobRequest(t, h, "GET", "/api/v1/jobs?limit=1&cursor="+out["next_cursor"].(string), "alice", "", 200)
	if len(out["data"].([]any)) != 1 || out["next_cursor"] != nil {
		t.Fatal(out)
	}
}

type unavailableQueue struct{ queue.Queue }

func (q unavailableQueue) Add(context.Context, string, time.Time, bool) error {
	return errors.New("queue unavailable")
}
func TestQueueOutagePreservesAPIWrites(t *testing.T) {
	j, h := newJobs(t)
	failing := unavailableQueue{j.Queue}
	j.Queue = failing
	j.Scheduler.(*QueueScheduler).Queue = failing
	out := jobRequest(t, h, "POST", "/api/v1/jobs", "alice", validJob, 201)
	path := "/api/v1/jobs/" + out["id"].(string)
	out = jobRequest(t, h, "GET", path, "alice", "", 200)
	if out["next_run_at"] == nil || len(out["next_runs"].([]any)) != 5 {
		t.Fatal(out)
	}
	trigger := jobRequest(t, h, "POST", path+"/trigger", "alice", "", 202)
	run := jobRequest(t, h, "GET", "/api/v1/runs/"+trigger["run_id"].(string), "alice", "", 200)
	if run["status"] != "deferred" || run["next_attempt_at"] == nil {
		t.Fatal(run)
	}
}
