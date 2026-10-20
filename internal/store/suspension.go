package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// Suspended reads current operator state, so local CLI changes take effect immediately.
func Suspended(ctx context.Context, s Store, workspace string) (bool, error) {
	row, err := s.ControlReader().WorkspaceSuspension(ctx, workspace)
	if err != nil {
		return false, fmt.Errorf("workspace suspension: %w", err)
	}
	return row.SuspendedAt.Valid, nil
}

// SetSuspension is shared by local administration and extension workspace administration.
// The control change precedes its cross-file audit; failure to audit is returned.
func SetSuspension(ctx context.Context, s Store, workspace, reason string, suspended bool, now time.Time) error {
	at := sql.NullInt64{Int64: now.UnixMilli(), Valid: suspended}
	n, err := s.Control().SetWorkspaceSuspension(ctx, sqlitegen.SetWorkspaceSuspensionParams{ID: workspace, SuspendedAt: at, SuspendedReason: sql.NullString{String: reason, Valid: suspended}})
	if err != nil {
		return fmt.Errorf("set suspension: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	action := "workspace.unsuspend"
	if suspended {
		action = "workspace.suspend"
	}
	audit, err := id.New(id.Run, now)
	if err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]string{"reason": reason})
	return s.History().WriteAudit(ctx, historygen.WriteAuditParams{ID: "audit_" + audit, WorkspaceID: workspace, Actor: "system", Action: action, Details: sql.NullString{String: string(details), Valid: true}, CreatedAt: now.UnixMilli()})
}
