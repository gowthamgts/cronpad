package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func TestEncryptionBindingMigrationAndRekey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbs, e := store.Open(ctx, filepath.Join(dir, "c.db"), filepath.Join(dir, "h.db"), time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = dbs.Close() }()
	s := store.New(dbs)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	v, e := New(key, s)
	if e != nil {
		t.Fatal(e)
	}
	for _, ws := range []string{"ws_a", "ws_b"} {
		if e = s.Control().CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: ws, Name: ws, Slug: ws, Plan: "free"}); e != nil {
			t.Fatal(e)
		}
	}
	_, e = s.Control().CreateJob(ctx, sqlitegen.CreateJobParams{ID: "job_legacy", WorkspaceID: "ws_a", Name: "legacy", Tags: "[]", ScheduleKind: "cron", ScheduleExpr: "@daily", ScheduleCanonical: "0 0 * * *", Timezone: "UTC", MisfirePolicy: "fire_once", SinkKind: "webhook", SinkConfig: `{"url":"https://example.com"}`, RetryPolicy: `{"max_attempts":1}`, TimeoutMs: 1000, State: "active", Version: 1, OverlapPolicy: "allow", SigningSecrets: sql.NullString{String: `[{"value":"whsec_legacy"}]`, Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	plain := []byte("private secret")
	b, e := v.Seal(plain, "ws_a:TOKEN")
	if e != nil {
		t.Fatal(e)
	}
	b2, e := v.Seal(plain, "ws_a:TOKEN")
	if e != nil || bytes.Equal(b, b2) {
		t.Fatal("nonce reused", e)
	}
	if bytes.Contains(b, plain) {
		t.Fatal("plaintext persisted")
	}
	if _, e = v.Open(b, "ws_b:TOKEN"); e == nil {
		t.Fatal("cross workspace decrypt")
	}
	damaged := bytes.Clone(b)
	damaged[len(damaged)-1] ^= 1
	if _, e = v.Open(damaged, "ws_a:TOKEN"); e == nil {
		t.Fatal("tampering accepted")
	}
	if e = s.Control().PutSecret(ctx, sqlitegen.PutSecretParams{WorkspaceID: "ws_a", Name: "TOKEN", Ciphertext: b}); e != nil {
		t.Fatal(e)
	}
	if e = s.Control().SaveIdempotency(ctx, sqlitegen.SaveIdempotencyParams{WorkspaceID: "ws_a", Key: "legacy", Response: `{"signing_secret":"old secret"}`, RequestHash: "hash", Status: 201}); e != nil {
		t.Fatal(e)
	}
	if e = v.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = v.Migrate(ctx); e != nil {
		t.Fatal("migration not repeatable", e)
	}
	row, e := s.ControlReader().GetIdempotency(ctx, sqlitegen.GetIdempotencyParams{WorkspaceID: "ws_a", Key: "legacy", ExpiresAt: -1})
	if e != nil || strings.Contains(row.Response, "old secret") {
		t.Fatal(row, e)
	}
	next := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	if e = v.Rekey(ctx, next); e != nil {
		t.Fatal(e)
	}
	signing, e := s.ControlReader().AllSigningSecrets(ctx)
	if e != nil || len(signing) != 1 || strings.Contains(signing[0].SigningSecrets.String, "whsec_legacy") {
		t.Fatal("signing secret not encrypted", e)
	}
	if _, e = v.Resolve(ctx, "ws_a", "TOKEN"); e == nil {
		t.Fatal("old key still decrypts")
	}
	newVault, e := New(next, s)
	if e != nil {
		t.Fatal(e)
	}
	got, e := newVault.Resolve(ctx, "ws_a", "TOKEN")
	if e != nil || got != string(plain) {
		t.Fatal(got, e)
	}
	if _, e = newVault.Resolve(ctx, "ws_b", "TOKEN"); e == nil {
		t.Fatal("foreign secret found")
	}
	if _, e = newVault.Resolve(ctx, "ws_a", "MISSING"); e == nil || !strings.Contains(e.Error(), "secret MISSING not found") {
		t.Fatal(e)
	}
	// A failed rekey must roll back earlier rows as well.
	if e = s.Control().PutSecret(ctx, sqlitegen.PutSecretParams{WorkspaceID: "ws_a", Name: "BROKEN", Ciphertext: []byte("broken")}); e != nil {
		t.Fatal(e)
	}
	if e = newVault.Rekey(ctx, key); e == nil {
		t.Fatal("corrupt data accepted")
	}
	got, e = newVault.Resolve(ctx, "ws_a", "TOKEN")
	if e != nil || got != string(plain) {
		t.Fatal("partial rekey", e)
	}
}
