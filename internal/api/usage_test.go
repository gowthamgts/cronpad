package api

import (
	"context"
	"testing"

	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store"
)

func TestUsageQuotaAndSuspension(t *testing.T) {
	j, h := newJobs(t)
	job := jobRequest(t, h, "POST", "/api/v1/jobs", "alice", validJob, 201)
	row, err := j.Store.ControlReader().AllWorkspaceIDs(context.Background())
	if err != nil || len(row) != 1 {
		t.Fatal(row, err)
	}
	ws := row[0]
	j.Config.Limits.MaxRunsPerMonthPerWorkspace = 1
	j.Limits = &limits.Registry{Config: j.Config.Limits, Store: j.Store, Clock: j.Clock}
	if err := j.Limits.Reserve(context.Background(), ws, "consumed"); err != nil {
		t.Fatal(err)
	}
	usage := jobRequest(t, h, "GET", "/api/v1/usage", "alice", "", 200)
	if usage["runs"] != float64(1) || usage["limit"] != float64(1) || usage["over_quota_policy"] != "block" {
		t.Fatal(usage)
	}
	path := "/api/v1/jobs/" + job["id"].(string)
	response := jobRequest(t, h, "POST", path+"/trigger", "alice", "", 429)
	if response["error"].(map[string]any)["limit"] != "max_runs_per_month_per_workspace" {
		t.Fatal(response)
	}
	if err := store.SetSuspension(context.Background(), j.Store, ws, "abuse", true, j.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	response = jobRequest(t, h, "PATCH", path, "alice", `{"name":"changed"}`, 403)
	if response["error"].(map[string]any)["code"] != "workspace_suspended" {
		t.Fatal(response)
	}
	jobRequest(t, h, "GET", path, "alice", "", 200)
	if err := store.SetSuspension(context.Background(), j.Store, ws, "", false, j.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	j.Limits.Config.MaxRunsPerMonthPerWorkspace = 0
	jobRequest(t, h, "POST", path+"/trigger", "alice", "", 202)
}
