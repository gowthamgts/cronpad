package api

import (
	"net/http"
	"time"
)

func (j *Jobs) usage(w http.ResponseWriter, r *http.Request) {
	if j.Limits == nil {
		writeError(w, 503, "unavailable", "usage is unavailable", nil)
		return
	}
	ws := who(r).WorkspaceID
	set, err := j.Limits.For(r.Context(), ws)
	if limitError(w, err) {
		return
	}
	runs, err := j.Limits.Usage(r.Context(), ws)
	if limitError(w, err) {
		return
	}
	now := j.Clock.Now().UTC()
	reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	// DECISION: numeric zero consistently represents unlimited in the API.
	writeJSON(w, 200, map[string]any{"month": now.Format("2006-01"), "runs": runs, "limit": set.MaxRunsPerMonth, "over_quota_policy": set.OverQuota, "reset_at": reset.Format(time.RFC3339)})
}
