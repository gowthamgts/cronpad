package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func TestPoolsMigrationsAndQueries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, filepath.Join(dir, "control.db"), filepath.Join(dir, "history.db"), 3*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, f := range []File{d.Control, d.History} {
		if f.Writer.Stats().MaxOpenConnections != 1 {
			t.Fatal("writer concurrency")
		}
		for _, db := range []*sql.DB{f.Writer, f.Reader} {
			var mode string
			if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Fatalf("WAL %s %v", mode, err)
			}
			for pragma, want := range map[string]int{"busy_timeout": 3000, "foreign_keys": 1, "synchronous": 1, "auto_vacuum": 2} {
				var got int
				if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
					t.Fatalf("%s=%d want %d: %v", pragma, got, want, err)
				}
			}
		}
		if _, err := f.Reader.ExecContext(ctx, "CREATE TABLE forbidden (id INTEGER)"); err == nil {
			t.Fatal("reader allowed write")
		}
	}
	q := sqlitegen.New(d.Control.Writer)
	if err := q.CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: "ws_1", Name: "test", Slug: "test", Plan: "free", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := sqlitegen.New(d.Control.Reader).GetWorkspace(ctx, "ws_1")
	if err != nil || got.Name != "test" {
		t.Fatalf("query: %+v %v", got, err)
	}
	if _, err := d.History.Reader.ExecContext(ctx, "SELECT * FROM jobs"); err == nil {
		t.Fatal("history unexpectedly includes control tables")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(ctx, filepath.Join(dir, "control.db"), filepath.Join(dir, "history.db"), 3*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
}
