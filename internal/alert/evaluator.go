// Package alert evaluates domain events into a durable, asynchronously delivered outbox.
package alert

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

type Selector struct {
	All    bool     `json:"all,omitempty"`
	JobIDs []string `json:"job_ids,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}
type Conditions struct {
	MinConsecutive int `json:"min_consecutive,omitempty"`
	MinAttempt     int `json:"min_attempt,omitempty"`
}
type Rule struct {
	Row              sqlitegen.AlertRule
	Selector         Selector
	Events, Channels []string
	Conditions       Conditions
}
type Payload struct {
	Event       string    `json:"event"`
	OccurredAt  time.Time `json:"occurred_at"`
	WorkspaceID string    `json:"workspace_id"`
	Job         struct {
		ID   string   `json:"id"`
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	} `json:"job"`
	Run struct {
		ID          string    `json:"id"`
		ScheduledAt time.Time `json:"scheduled_at"`
		Status      string    `json:"status"`
		Attempts    int64     `json:"attempts"`
		LastError   string    `json:"last_error"`
	} `json:"run"`
	SuppressedCount int64  `json:"suppressed_count"`
	URL             string `json:"url"`
}
type Evaluator struct {
	Store     store.Store
	Writer    *results.Writer
	Clock     clockwork.Clock
	Config    config.Config
	PublicURL string
	rules     atomic.Pointer[[]Rule]
	refreshMu sync.Mutex
}

func (e *Evaluator) Refresh(ctx context.Context) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	rows, err := e.Store.ControlReader().AllAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("load alert rules: %w", err)
	}
	rules := make([]Rule, 0, len(rows))
	for _, row := range rows {
		r := Rule{Row: row}
		if err = json.Unmarshal([]byte(row.JobSelector), &r.Selector); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(row.Events), &r.Events); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(row.ChannelIds), &r.Channels); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(row.Conditions), &r.Conditions); err != nil {
			return err
		}
		rules = append(rules, r)
	}
	e.rules.Store(&rules)
	return nil
}
func (e *Evaluator) payload(ev events.Event, j sqlitegen.Job, r historygen.Run) Payload {
	p := Payload{Event: ev.Type, OccurredAt: ev.At, WorkspaceID: ev.WorkspaceID}
	p.Job.ID = j.ID
	p.Job.Name = j.Name
	p.Job.Tags = []string{}
	_ = json.Unmarshal([]byte(j.Tags), &p.Job.Tags)
	p.Run.ID = r.ID
	p.Run.Status = r.Status
	p.Run.Attempts = r.Attempts
	p.Run.LastError = r.LastError.String
	if p.Run.LastError == "" {
		p.Run.LastError = ev.Error
	}
	p.Run.ScheduledAt = time.UnixMilli(r.ScheduledAt).UTC()
	p.URL = e.PublicURL + "/runs/" + r.ID
	if r.ID == "" {
		p.URL = e.PublicURL + "/alerts"
		if j.ID != "" {
			p.URL = e.PublicURL + "/jobs/" + j.ID
		}
	}
	return p
}
func (e *Evaluator) evaluate(ctx context.Context, tx store.Tx, ev events.Event, j sqlitegen.Job, r historygen.Run, consecutive int) error {
	suspended, err := store.Suspended(ctx, e.Store, ev.WorkspaceID)
	if err != nil {
		return err
	}
	if suspended {
		return nil
	}
	rules := e.rules.Load()
	if rules == nil {
		return nil
	}
	for _, rule := range *rules {
		if rule.Row.WorkspaceID != ev.WorkspaceID || !slices.Contains(rule.Events, ev.Type) {
			continue
		}
		if ev.Type == events.Degraded && e.Config.Mode == "hosted" {
			continue
		}
		p := e.payload(ev, j, r)
		match := rule.Selector.All || slices.Contains(rule.Selector.JobIDs, j.ID)
		for _, tag := range p.Job.Tags {
			match = match || slices.Contains(rule.Selector.Tags, tag)
		}
		if !match || ev.Type == events.RunFailed && consecutive < rule.Conditions.MinConsecutive || ev.Type == events.RunDeferred && ev.Attempt+1 < rule.Conditions.MinAttempt {
			continue
		}
		for _, channel := range rule.Channels {
			key := rule.Row.ID + ":" + j.ID + ":" + ev.Type
			status := "pending"
			last, err := tx.History.LastAlert(ctx, historygen.LastAlertParams{DedupeKey: key, ChannelID: channel})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				// DECISION: reserve cooldown when queued as well as sent, so a slow destination cannot accumulate a burst.
				if ev.Type != events.JobRecovered && ev.Type != events.JobAutoDisabled && ev.At.UnixMilli()-last.CreatedAt < rule.Row.CooldownMs {
					status = "suppressed"
				}
				if status == "pending" {
					p.SuppressedCount, err = tx.History.CountSuppressedAlerts(ctx, historygen.CountSuppressedAlertsParams{DedupeKey: key, ChannelID: channel, AfterID: last.ID})
					if err != nil {
						return err
					}
				}
			}
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			// Stable event/channel IDs make replay after commit safe. Webhook receivers get this same ID on every delivery attempt.
			identity := fmt.Sprintf("%s:%s:%d:%s:%s", key, r.ID, ev.Attempt, channel, ev.At.Format(time.RFC3339Nano))
			if r.ID != "" {
				identity = fmt.Sprintf("%s:%s:%d:%s", key, r.ID, ev.Attempt, channel)
			}
			oid := fmt.Sprintf("al_%x", sha256.Sum256([]byte(identity)))
			if err = tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: oid, WorkspaceID: ev.WorkspaceID, RuleID: rule.Row.ID, ChannelID: channel, EventType: ev.Type, DedupeKey: key, Payload: string(raw), Status: status, CreatedAt: ev.At.UnixMilli(), NextAttemptAt: ev.At.UnixMilli()}); err != nil {
				return err
			}
		}
	}
	if ev.Type == events.JobAutoDisabled && e.Config.AutoDisable.Notify && e.Config.Alerts.Email.Enabled {
		raw, _ := json.Marshal(e.payload(ev, j, r))
		key := "owners:" + j.ID + ":" + r.ID
		return tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: fmt.Sprintf("al_%x", sha256.Sum256([]byte(key))), WorkspaceID: j.WorkspaceID, RuleID: "owners", ChannelID: "owners", EventType: ev.Type, DedupeKey: key, Payload: string(raw), Status: "pending", CreatedAt: ev.At.UnixMilli(), NextAttemptAt: ev.At.UnixMilli()})
	}
	return nil
}

// RecordRun is called inside the result writer's history transaction, before it commits.
func (e *Evaluator) RecordRun(ctx context.Context, tx store.Tx, j sqlitegen.Job, r historygen.Run, outcome sink.Outcome, first bool) error {
	rules := e.rules.Load()
	if (rules == nil || len(*rules) == 0) && !e.Config.Alerts.Email.Enabled {
		return nil
	}
	// Per-job finish serialization keeps this committed counter current until the
	// subsequent control write. Reading it avoids scanning retained run history.
	previous, err := e.Store.ControlReader().AlertJobState(ctx, sqlitegen.AlertJobStateParams{ID: j.ID, WorkspaceID: j.WorkspaceID, Version: j.Version})
	before := previous.ConsecutiveFailures
	current := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	consecutive := int(before)
	if r.Status == "failed" {
		consecutive++
	}
	kinds := []string{events.RunFailed}
	if r.Status == "succeeded" {
		kinds = []string{events.RunSucceeded}
		if before > 0 {
			kinds = append(kinds, events.JobRecovered)
		}
	}
	if r.Status == "deferred" {
		kinds = []string{events.RunDeferred}
	}
	if outcome == sink.Timeout {
		kinds = append(kinds, events.RunTimeout)
	}
	if first && r.SlaBreached != 0 {
		kinds = append(kinds, events.RunSLABreached)
	}
	threshold := int(e.Config.AutoDisable.ConsecutiveFailures)
	if previous.AutoDisableThreshold.Valid {
		threshold = int(previous.AutoDisableThreshold.Int64)
	}
	if e.Config.Mode == "hosted" {
		threshold = min(threshold, e.Config.AutoDisable.ConsecutiveFailures)
	}
	if current && previous.State != "disabled" && e.Config.AutoDisable.Enabled && r.Status == "failed" && consecutive >= threshold {
		kinds = append(kinds, events.JobAutoDisabled)
	}
	for _, kind := range kinds {
		if err = e.evaluate(ctx, tx, events.Event{Type: kind, WorkspaceID: j.WorkspaceID, JobID: j.ID, RunID: r.ID, At: e.Clock.Now(), Attempt: int(r.Attempts) - 1}, j, r, consecutive); err != nil {
			return err
		}
	}
	return nil
}

// Publish persists non-run events through the result writer; network delivery never runs here.
func (e *Evaluator) Publish(ctx context.Context, ev events.Event) {
	// Run events have already been inserted atomically by RecordRun.
	if ev.RunID != "" {
		return
	}
	_, _ = e.Writer.Submit(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
		workspaces := []string{ev.WorkspaceID}
		if ev.WorkspaceID == "" {
			var err error
			workspaces, err = e.Store.ControlReader().AllWorkspaceIDs(ctx)
			if err != nil {
				return err
			}
		}
		for _, ws := range workspaces {
			ev.WorkspaceID = ws
			j := sqlitegen.Job{ID: ev.JobID, WorkspaceID: ws, Tags: "[]"}
			if ev.JobID != "" {
				var err error
				j, err = e.Store.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: ev.JobID, WorkspaceID: ws})
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
			}
			if err := e.evaluate(ctx, tx, ev, j, historygen.Run{}, 0); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordStart commits SLA alerts before delivery begins, including for long-running commands.
func (e *Evaluator) RecordStart(ctx context.Context, tx store.Tx, j sqlitegen.Job, r historygen.Run, attempt int) error {
	return e.evaluate(ctx, tx, events.Event{Type: events.RunSLABreached, WorkspaceID: j.WorkspaceID, JobID: j.ID, RunID: r.ID, At: e.Clock.Now(), Attempt: attempt}, j, r, 0)
}

// RecordQuota atomically deduplicates owner notices with the admission counter.
func (e *Evaluator) RecordQuota(ctx context.Context, tx store.Tx, ws string, max int) error {
	rules := e.rules.Load()
	if max <= 0 || (!e.Config.Alerts.Email.Enabled && (rules == nil || len(*rules) == 0)) {
		return nil
	}
	suspended, err := store.Suspended(ctx, e.Store, ws)
	if err != nil {
		return err
	}
	if suspended {
		return nil
	}
	month := e.Clock.Now().UTC().Format("2006-01")
	n, err := tx.History.CountMonthlyRuns(ctx, historygen.CountMonthlyRunsParams{WorkspaceID: ws, Month: month})
	if err != nil {
		return err
	}
	for _, notice := range []struct {
		kind, column string
		threshold    int64
	}{{events.WorkspaceQuotaWarning, "quota_warned_at", (int64(max)*4 + 4) / 5}, {events.WorkspaceQuotaExceeded, "quota_exceeded_at", int64(max)}} {
		if n < notice.threshold {
			continue
		}
		result, err := tx.SQL.ExecContext(ctx, "UPDATE workspace_usage_monthly SET "+notice.column+"=? WHERE workspace_id=? AND month=? AND "+notice.column+" IS NULL", e.Clock.Now().UnixMilli(), ws, month)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			continue
		}
		ev := events.Event{Type: notice.kind, WorkspaceID: ws, At: e.Clock.Now(), Error: fmt.Sprintf("%d of %d monthly runs used; resets next UTC month", n, max)}
		if err = e.evaluate(ctx, tx, ev, sqlitegen.Job{WorkspaceID: ws, Tags: "[]"}, historygen.Run{}, 0); err != nil {
			return err
		}
		if !e.Config.Alerts.Email.Enabled {
			continue
		}
		raw, err := json.Marshal(e.payload(ev, sqlitegen.Job{}, historygen.Run{}))
		if err != nil {
			return err
		}
		key := "owners:" + ws + ":" + month + ":" + notice.kind
		if err = tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: fmt.Sprintf("al_%x", sha256.Sum256([]byte(key))), WorkspaceID: ws, RuleID: "owners", ChannelID: "owners", EventType: notice.kind, DedupeKey: key, Payload: string(raw), Status: "pending", CreatedAt: e.Clock.Now().UnixMilli(), NextAttemptAt: e.Clock.Now().UnixMilli()}); err != nil {
			return err
		}
	}
	return nil
}
