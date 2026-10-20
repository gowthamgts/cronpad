package store

import (
	"context"
	"database/sql"

	"github.com/gowthamgts/cronpad/internal/store/historygen"
)

// auditQueries publishes only after the autocommitted insert succeeds.
type auditQueries struct {
	historygen.Querier
	notify func(context.Context, historygen.WriteAuditParams)
}

func (q auditQueries) WriteAudit(ctx context.Context, p historygen.WriteAuditParams) error {
	if err := q.Querier.WriteAudit(ctx, p); err != nil {
		return err
	}
	q.notify(ctx, p)
	return nil
}

// SetAuditListener installs a nonblocking listener before serving requests.
// Transactional records are buffered until WithTx has committed successfully.
func (s *SQLite) SetAuditListener(fn func(context.Context, historygen.WriteAuditParams), enabled ...func() bool) {
	s.audit = fn
	if len(enabled) > 0 {
		s.auditEnabled = enabled[0]
	}
}

// DECISION: sqlc cannot resolve SQLite's implicit rowid in a WHERE clause.
// This bounded internal commit cursor is the only raw audit read; public writes
// and the cursor high-watermark use generated query contracts.
func pendingAudit(ctx context.Context, tx *sql.Tx, after int64) ([]historygen.WriteAuditParams, error) {
	const batchSize = 256
	var out []historygen.WriteAuditParams
	for {
		rows, err := tx.QueryContext(ctx, "SELECT rowid,id,workspace_id,actor,action,target,details,created_at FROM audit_log WHERE rowid>? ORDER BY rowid LIMIT ?", after, batchSize)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var p historygen.WriteAuditParams
			if err = rows.Scan(&after, &p.ID, &p.WorkspaceID, &p.Actor, &p.Action, &p.Target, &p.Details, &p.CreatedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, p)
			count++
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if count < batchSize {
			return out, nil
		}
	}
}
