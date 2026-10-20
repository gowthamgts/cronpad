package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// Store keeps read pools separate from the single writers for each file.
// Store exposes query contracts rather than connection pools. Implementations may
// preserve the same query parameter/result types while changing database drivers.
type Store interface {
	Control() sqlitegen.Querier
	ControlReader() sqlitegen.Querier
	History() historygen.Querier
	HistoryReader() historygen.Querier
	WithTx(context.Context, Database, func(Tx) error) error
}

// SQLite implements Store with independent pools for control and history data.
type SQLite struct {
	DBs                    *DBs
	control, controlReader *sqlitegen.Queries
	history, historyReader *historygen.Queries
	audit                  func(context.Context, historygen.WriteAuditParams)
	auditEnabled           func() bool
}

func New(d *DBs) *SQLite {
	return &SQLite{DBs: d, control: sqlitegen.New(d.Control.Writer), controlReader: sqlitegen.New(d.Control.Reader), history: historygen.New(d.History.Writer), historyReader: historygen.New(d.History.Reader)}
}

type Database string

const (
	Control Database = "control"
	History Database = "history"
)

type Tx struct {
	SQL     *sql.Tx // SQL is reserved for bounded maintenance statements.
	Control *sqlitegen.Queries
	History *historygen.Queries
}

// WithTx runs against exactly one database; cross-file writes must be sequenced explicitly.
func (s *SQLite) WithTx(ctx context.Context, db Database, fn func(Tx) error) error {
	var handle *sql.DB
	switch db {
	case Control:
		handle = s.DBs.Control.Writer
	case History:
		handle = s.DBs.History.Writer
	default:
		return fmt.Errorf("unknown database %q", db)
	}
	tx, e := handle.BeginTx(ctx, nil)
	if e != nil {
		return fmt.Errorf("begin: %w", e)
	}
	defer func() { _ = tx.Rollback() }()
	t := Tx{SQL: tx}
	if db == Control {
		t.Control = s.control.WithTx(tx)
	} else {
		t.History = s.history.WithTx(tx)
	}
	audit := s.audit
	if s.auditEnabled != nil && !s.auditEnabled() {
		audit = nil
	}
	var auditAfter int64
	if db == History && audit != nil {
		if auditAfter, e = t.History.LastAuditRowID(ctx); e != nil {
			return e
		}
	}
	if e = fn(t); e != nil {
		return e
	}
	var events []historygen.WriteAuditParams
	if db == History && audit != nil {
		events, e = pendingAudit(ctx, tx, auditAfter)
		if e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return fmt.Errorf("commit: %w", e)
	}
	for _, event := range events {
		audit(ctx, event)
	}
	return nil
}

func (s *SQLite) Control() sqlitegen.Querier       { return s.control }
func (s *SQLite) ControlReader() sqlitegen.Querier { return s.controlReader }
func (s *SQLite) History() historygen.Querier {
	if s.audit != nil {
		return auditQueries{s.history, s.audit}
	}
	return s.history
}
func (s *SQLite) HistoryReader() historygen.Querier { return s.historyReader }

var _ Store = (*SQLite)(nil)

// NewPrepared prepares query plans once for the hot paths instead of reparsing
// every statement in every batch. DBs.Close owns the statement lifetimes.
// DECISION: keep New for lightweight callers; production and load tests use this
// identical Store implementation with prepared query plans.
func NewPrepared(ctx context.Context, d *DBs) (*SQLite, error) {
	s := New(d)
	var err error
	s.control, err = sqlitegen.Prepare(ctx, d.Control.Writer)
	if err != nil {
		return nil, fmt.Errorf("prepare control writer: %w", err)
	}
	s.controlReader, err = sqlitegen.Prepare(ctx, d.Control.Reader)
	if err != nil {
		return nil, fmt.Errorf("prepare control reader: %w", err)
	}
	s.history, err = historygen.Prepare(ctx, d.History.Writer)
	if err != nil {
		return nil, fmt.Errorf("prepare history writer: %w", err)
	}
	s.historyReader, err = historygen.Prepare(ctx, d.History.Reader)
	if err != nil {
		return nil, fmt.Errorf("prepare history reader: %w", err)
	}
	return s, nil
}
