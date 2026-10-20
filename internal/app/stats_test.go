package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestRetainedStatsWorkspaceIsolation(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) { c.Retention.Runs = config.Duration(24 * time.Hour) })
	created := object(t, f.request("POST", "/api/v1/jobs", m3Job, nil, nil, 201))
	job := created["id"].(string)
	var ws string
	if err := f.a.DBs.Control.Reader.QueryRowContext(context.Background(), `SELECT workspace_id FROM jobs WHERE id=?`, job).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	now := f.a.janitor.Clock.Now()
	day := now.Add(-10*24*time.Hour).UnixMilli() / 86400000
	db := f.a.DBs.History.Writer
	for _, args := range [][]any{{job, ws, day, 100, 90, 10, 2}, {"foreign", "other", day, 999, 0, 999, 999}} {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO job_stats_daily(job_id,workspace_id,day,runs,succeeded,failed,sla_breached) VALUES(?,?,?,?,?,?,?)`, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO job_stats_lags VALUES(?,?,10,95),(?,?,100,5),('foreign',?,999,999)`, job, day, job, day, day); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,start_lag_ms) VALUES('recent',?,?,?,'manual','succeeded',20)`, job, ws, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	response := f.request("GET", "/api/v1/stats/overview?window=30d", "", nil, nil, 200)
	var result struct {
		Runs map[string]int64 `json:"runs"`
		Lags map[string]int64 `json:"start_lag_ms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Runs["total"] != 101 || result.Runs["succeeded"] != 91 || result.Runs["failed"] != 10 || result.Runs["sla_breached"] != 2 {
		t.Fatal(result)
	}
	if result.Lags["p50"] != 10 || result.Lags["p95"] != 20 || result.Lags["p99"] != 100 {
		t.Fatal(result)
	}
}
