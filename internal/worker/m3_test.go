package worker_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/secrets"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func TestM3ClaimQuotaAndSecretResolver(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testM3ClaimQuotaAndSecretResolver(t)
		})
	}
}
func testM3ClaimQuotaAndSecretResolver(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "encrypted", f.Clock.Now())
	vault, e := secrets.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))), f.Store)
	if e != nil {
		t.Fatal(e)
	}
	encrypted, e := vault.Wrap(`[{"value":"whsec_dGVzdA=="}]`, "signing:"+job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Store.Control().SetSigningSecrets(context.Background(), sqlitegen.SetSigningSecretsParams{ID: job.ID, SigningSecrets: sql.NullString{String: encrypted, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	b, e := vault.Seal([]byte("private"), job.WorkspaceID+":TOKEN")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Store.Control().PutSecret(context.Background(), sqlitegen.PutSecretParams{WorkspaceID: job.WorkspaceID, Name: "TOKEN", Ciphertext: b}); e != nil {
		t.Fatal(e)
	}
	f.Engine.Vault = vault
	f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1}, Store: f.Store, Clock: f.Clock}
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		value, e := r.Secrets.Resolve(ctx, "TOKEN")
		if e != nil || value != "private" || len(r.Job.SigningSecrets) != 1 || r.Job.SigningSecrets[0].Value != "whsec_dGVzdA==" {
			t.Error("execution secret resolution", e)
		}
		return sink.ExecResult{Outcome: sink.Success}
	})
	first := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: job.Version, Trigger: "s"}
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: first, ClaimedAt: f.Clock.Now()})
	})
	second := first
	second.ScheduledAt = second.ScheduledAt.Add(time.Second)
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: second, ClaimedAt: f.Clock.Now()})
	})
	if calls.Load() != 1 {
		t.Fatal("quota ignored", calls.Load())
	}
	if stats, e := f.Queue.Stats(context.Background()); e != nil || stats.Due != 0 {
		t.Fatal("quota skip was requeued", stats, e)
	}
	if row, e := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: second.ScheduledAt.UnixMilli(), Trigger: "schedule"}); e != nil || row.Status != "skipped" || row.SkipReason.String != "quota_exceeded" || row.ClaimedAt.Valid {
		t.Fatal("over-quota run not durably skipped", row, e)
	}
}
