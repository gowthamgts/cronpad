// Package store opens isolated writer and reader pools for both SQLite files.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/control/*.sql migrations/history/*.sql
var migrations embed.FS

type File struct{ Writer, Reader *sql.DB }
type DBs struct{ Control, History File }

func (d *DBs) Close() error { return errors.Join(closeFile(d.Control), closeFile(d.History)) }
func closeFile(f File) error {
	var errs []error
	if f.Reader != nil {
		errs = append(errs, f.Reader.Close())
	}
	if f.Writer != nil {
		errs = append(errs, f.Writer.Close())
	}
	return errors.Join(errs...)
}

// Open applies migrations before opening read-only pools. Partial failures close all handles.
func Open(ctx context.Context, control, history string, busy time.Duration, migrate bool) (*DBs, error) {
	d := &DBs{}
	var err error
	d.Control, err = openFile(ctx, control, busy, migrate, "control")
	if err != nil {
		return nil, fmt.Errorf("control DB: %w", err)
	}
	d.History, err = openFile(ctx, history, busy, migrate, "history")
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("history DB: %w", err)
	}
	return d, nil
}
func openFile(ctx context.Context, path string, busy time.Duration, migrate bool, kind string) (f File, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return f, fmt.Errorf("database path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return f, fmt.Errorf("database directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = closeFile(f)
		}
	}()
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+fmt.Sprint(busy.Milliseconds())+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	u := url.URL{Scheme: "file", Path: abs}
	u.RawQuery = q.Encode()
	f.Writer, err = sql.Open("sqlite", u.String())
	if err != nil {
		return f, fmt.Errorf("open writer: %w", err)
	}
	f.Writer.SetMaxOpenConns(1)
	// New files must enable incremental vacuum before WAL/migration tables allocate pages.
	var tables int
	if err = f.Writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
		return f, fmt.Errorf("inspect DB: %w", err)
	}
	if tables == 0 {
		if _, err = f.Writer.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return f, fmt.Errorf("auto vacuum: %w", err)
		}
	}
	if _, err = f.Writer.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return f, fmt.Errorf("WAL: %w", err)
	}
	if migrate {
		var sub fs.FS
		sub, err = fs.Sub(migrations, "migrations/"+kind)
		if err != nil {
			return f, fmt.Errorf("migration FS: %w", err)
		}
		if err = Migrate(ctx, f.Writer, sub, "goose_db_version"); err != nil {
			return f, err
		}
	}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	f.Reader, err = sql.Open("sqlite", u.String())
	if err != nil {
		return f, fmt.Errorf("open reader: %w", err)
	}
	f.Reader.SetMaxOpenConns(runtime.NumCPU() * 2)
	if err = f.Reader.PingContext(ctx); err != nil {
		return f, fmt.Errorf("ping reader: %w", err)
	}
	return f, nil
}

// Migrate runs embedded SQL with an independent goose version table.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS, table string) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, fsys, goose.WithTableName(table))
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	if _, err = p.Up(ctx); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	return nil
}
