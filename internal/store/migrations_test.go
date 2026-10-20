package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// Each independent database ships one v0 baseline, reversible as a unit.
func TestV0Baselines(t *testing.T) {
	for _, kind := range []string{"control", "history"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			sub, err := fs.Sub(migrations, "migrations/"+kind)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := fs.ReadDir(sub, ".")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "00001_initial.sql" {
				t.Fatalf("unexpected baseline files: %v", entries)
			}
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "baseline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err = p.Up(ctx); err != nil {
					t.Fatal(err)
				}
				version, err := p.GetDBVersion(ctx)
				if err != nil || version != 1 {
					t.Fatalf("version=%d err=%v", version, err)
				}
				if results, err := p.Up(ctx); err != nil || len(results) != 0 {
					t.Fatalf("restart migrations=%v err=%v", results, err)
				}
				if _, err = p.DownTo(ctx, 0); err != nil {
					t.Fatal(err)
				}
				var n int
				if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("rollback left %d schema objects", n)
				}
			}
		})
	}
}
