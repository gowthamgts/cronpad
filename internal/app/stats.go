package app

import (
	"context"
	"fmt"

	"github.com/gowthamgts/cronpad/internal/store"
)

// stats implements ext.StatsReader from the control database's read pool.
type stats struct{ store store.Store }

func (s stats) ReadStats(ctx context.Context) (map[string]int64, error) {
	q := s.store.ControlReader()
	users, err := q.CountActiveUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}
	ws, err := q.CountWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("count workspaces: %w", err)
	}
	jobs, err := q.JobCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	out := map[string]int64{"users": users, "workspaces": ws.Total, "workspaces.suspended": ws.Suspended, "jobs": 0}
	for _, state := range []string{"active", "paused", "disabled", "completed"} {
		out["jobs."+state] = 0
	}
	for _, row := range jobs {
		out["jobs."+row.State] = row.Total
		out["jobs"] += row.Total
	}
	return out, nil
}
