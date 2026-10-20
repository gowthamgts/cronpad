package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	smtp "github.com/wneessen/go-mail"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/outbound"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/secrets"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
)

type ChannelConfig struct {
	To     []string `json:"to,omitempty"`
	URL    string   `json:"url,omitempty"`
	Format string   `json:"format,omitempty"`
}

func ValidateChannel(kind string, raw json.RawMessage, p netguard.Policy) error {
	var c ChannelConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return fmt.Errorf("invalid channel config: %w", err)
	}
	switch kind {
	case "email":
		if len(c.To) == 0 || len(c.To) > 20 || c.URL != "" {
			return fmt.Errorf("email channels require 1..20 addresses")
		}
		for _, to := range c.To {
			address, err := mail.ParseAddress(to)
			if err != nil || address.Address != to {
				return fmt.Errorf("invalid email address")
			}
		}
	case "webhook":
		if len(c.To) > 0 {
			return fmt.Errorf("webhook channels cannot contain email addresses")
		}
		if err := p.CheckURL(c.URL); err != nil {
			return err
		}
		if !slices.Contains([]string{"", "generic", "slack", "discord"}, c.Format) {
			return fmt.Errorf("invalid webhook format")
		}
	default:
		return fmt.Errorf("invalid channel kind")
	}
	return nil
}

type Notifier struct {
	Limits     *limits.Registry
	Hosts      *outbound.Hosts
	Store      store.Store
	Writer     *results.Writer
	Clock      clockwork.Clock
	Vault      *secrets.Vault
	Email      config.ConfigAlertsEmail
	Policy     netguard.Policy
	PublicURL  string
	SigningKey []byte
	Metrics    *telemetry.Instruments
}

func (n *Notifier) Run(ctx context.Context) error {
	ticker := n.Clock.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.Chan():
			if err := n.Poll(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}
func (n *Notifier) Poll(ctx context.Context) error {
	rows, err := n.Store.HistoryReader().PendingAlerts(ctx, n.Clock.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("poll alert outbox: %w", err)
	}
	// Delivery leases prevent concurrent sends from standby processes. A crash after send remains at-least-once; webhook-id permits receiver deduplication.
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		token, err := id.New(id.Run, n.Clock.Now())
		if err != nil {
			return err
		}
		var claimed int64
		err = n.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			var err error
			claimed, err = tx.History.ClaimAlert(ctx, historygen.ClaimAlertParams{ID: row.ID, DeliveryToken: token, DeliveryLeaseUntil: n.Clock.Now().Add(30 * time.Second).UnixMilli(), Now: n.Clock.Now().UnixMilli()})
			return err
		})
		if err != nil {
			return err
		}
		if claimed == 0 {
			continue
		}
		kind := "email"
		if row.ChannelID != "owners" {
			ch, e := n.Store.ControlReader().GetAlertChannel(ctx, sqlitegen.GetAlertChannelParams{ID: row.ChannelID, WorkspaceID: row.WorkspaceID})
			if e == nil {
				kind = ch.Kind
			}
		}
		suspended, e := store.Suspended(ctx, n.Store, row.WorkspaceID)
		if e != nil {
			return e
		}
		allowed := !suspended
		extra := int64(0)
		counted := false
		day := n.Clock.Now().UTC().Format("2006-01-02")
		// Quota notices always reach owners, even when ordinary alert email is capped.
		quotaNotice := strings.HasPrefix(row.EventType, "workspace.quota_")
		if allowed && kind == "email" && n.Limits != nil && !quotaNotice {
			set, e := n.Limits.For(ctx, row.WorkspaceID)
			if e != nil {
				return e
			}
			err = n.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
				_, e := tx.SQL.ExecContext(ctx, `INSERT INTO workspace_email_usage(workspace_id,day) VALUES(?,?) ON CONFLICT DO NOTHING`, row.WorkspaceID, day)
				if e != nil {
					return e
				}
				var sent int64
				if e = tx.SQL.QueryRowContext(ctx, `SELECT sent FROM workspace_email_usage WHERE workspace_id=? AND day=?`, row.WorkspaceID, day).Scan(&sent); e != nil {
					return e
				}
				if set.MaxAlertEmailsPerDay > 0 && sent >= int64(set.MaxAlertEmailsPerDay) {
					allowed = false
					_, e = tx.SQL.ExecContext(ctx, `UPDATE workspace_email_usage SET suppressed=suppressed+1 WHERE workspace_id=? AND day=?`, row.WorkspaceID, day)
					return e
				}
				if e = tx.SQL.QueryRowContext(ctx, `SELECT coalesce(sum(suppressed),0) FROM workspace_email_usage WHERE workspace_id=?`, row.WorkspaceID).Scan(&extra); e != nil {
					return e
				}
				_, e = tx.SQL.ExecContext(ctx, `UPDATE workspace_email_usage SET sent=sent+1 WHERE workspace_id=? AND day=?`, row.WorkspaceID, day)
				counted = e == nil
				return e
			})
			if err != nil {
				return err
			}
		}
		if row.EventType == "channel.verify" {
			extra = 0
		}
		if extra > 0 {
			var payload Payload
			if json.Unmarshal([]byte(row.Payload), &payload) == nil {
				payload.SuppressedCount += extra
				body, e := json.Marshal(payload)
				if e != nil {
					return e
				}
				row.Payload = string(body)
			}
		}
		if !allowed {
			err = n.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
				return tx.History.FinishAlert(ctx, historygen.FinishAlertParams{ID: row.ID, Status: "suppressed", Attempts: row.Attempts, NextAttemptAt: row.NextAttemptAt, DeliveryToken: token})
			})
			if err != nil {
				return err
			}
			continue
		}
		var deliveryErr error
		if row.ChannelID == "owners" {
			var to []string
			to, deliveryErr = n.Store.ControlReader().WorkspaceOwnerEmails(ctx, row.WorkspaceID)
			if deliveryErr == nil && len(to) > 0 {
				var p Payload
				deliveryErr = json.Unmarshal([]byte(row.Payload), &p)
				if deliveryErr == nil {
					deliveryErr = n.sendEmail(ctx, to, p.Event, message(p), row.ID)
				}
			}
		} else {
			ch, e := n.Store.ControlReader().GetAlertChannel(ctx, sqlitegen.GetAlertChannelParams{ID: row.ChannelID, WorkspaceID: row.WorkspaceID})
			deliveryErr = e
			if e == nil {
				kind = ch.Kind
				deliveryErr = n.deliver(ctx, ch, row)
			}
			if errors.Is(e, sql.ErrNoRows) {
				deliveryErr = errChannelGone
			}
		}
		status := "pending"
		attempts := row.Attempts + 1
		sent := sql.NullInt64{}
		if deliveryErr == nil {
			status = "sent"
			sent = sql.NullInt64{Int64: n.Clock.Now().UnixMilli(), Valid: true}
		} else if attempts >= 8 || errors.Is(deliveryErr, errChannelGone) {
			status = "failed"
		}
		if errors.Is(deliveryErr, errUnverified) {
			status = "pending"
			attempts = row.Attempts
		}
		next := n.Clock.Now().Add(time.Second * time.Duration(1<<min(attempts, 10))).UnixMilli()
		if errors.Is(deliveryErr, errUnverified) {
			next = n.Clock.Now().Add(time.Minute).UnixMilli()
		}
		err = n.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			if counted {
				if deliveryErr != nil {
					if _, e := tx.SQL.ExecContext(ctx, `UPDATE workspace_email_usage SET sent=max(0,sent-1) WHERE workspace_id=? AND day=?`, row.WorkspaceID, day); e != nil {
						return e
					}
				} else if extra > 0 {
					// Subtract only the snapshot reported in this successfully sent email.
					rows, e := tx.SQL.QueryContext(ctx, `SELECT day,suppressed FROM workspace_email_usage WHERE workspace_id=? AND suppressed>0 ORDER BY day`, row.WorkspaceID)
					if e != nil {
						return e
					}
					type suppression struct {
						day string
						n   int64
					}
					var snapshots []suppression
					for rows.Next() {
						var s suppression
						if e = rows.Scan(&s.day, &s.n); e != nil {
							_ = rows.Close()
							return e
						}
						snapshots = append(snapshots, s)
					}
					e = rows.Err()
					_ = rows.Close()
					if e != nil {
						return e
					}
					left := extra
					for _, s := range snapshots {
						take := min(left, s.n)
						if _, e = tx.SQL.ExecContext(ctx, `UPDATE workspace_email_usage SET suppressed=suppressed-? WHERE workspace_id=? AND day=?`, take, row.WorkspaceID, s.day); e != nil {
							return e
						}
						left -= take
						if left == 0 {
							break
						}
					}
				}
			}
			return tx.History.FinishAlert(ctx, historygen.FinishAlertParams{ID: row.ID, Status: status, Attempts: attempts, NextAttemptAt: next, SentAt: sent, DeliveryToken: token})
		})
		if err != nil {
			return err
		}
		if n.Metrics != nil && status != "pending" {
			n.Metrics.Add(ctx, "cronpad.alerts.sent", 1, attribute.String("channel", kind), attribute.String("status", status))
		}
	}
	return nil
}

var errUnverified = errors.New("email channel is unverified")
var errChannelGone = errors.New("alert channel deleted")

func message(p Payload) string {
	s := fmt.Sprintf("%s: %s\nRun: %s\nScheduled: %s\nAttempts: %d\n%s\n%s", p.Event, p.Job.Name, p.Run.ID, p.Run.ScheduledAt.Format(time.RFC3339), p.Run.Attempts, p.Run.LastError, p.URL)
	if p.SuppressedCount > 0 {
		s += fmt.Sprintf("\n+%d more failures suppressed", p.SuppressedCount)
	}
	return s
}

// Body shapes human-readable chat messages while preserving the generic JSON contract.
func Body(p Payload, format string) ([]byte, error) {
	switch format {
	case "slack":
		return json.Marshal(map[string]string{"text": message(p)})
	case "discord":
		text := message(p)
		if len(text) > 1900 {
			text = text[:1900]
		}
		return json.Marshal(map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}})
	default:
		return json.Marshal(p)
	}
}
func (n *Notifier) deliver(ctx context.Context, ch sqlitegen.AlertChannel, row historygen.AlertOutbox) error {
	var c ChannelConfig
	if err := json.Unmarshal([]byte(ch.Config), &c); err != nil {
		return err
	}
	if row.EventType == "channel.verify" {
		var v verification
		if err := json.Unmarshal([]byte(row.Payload), &v); err != nil {
			return err
		}
		if !slices.Contains(c.To, v.Address) {
			return errChannelGone
		}
		return n.sendEmail(ctx, []string{v.Address}, "Verify your cronpad alert channel", v.Link, row.ID)
	}
	if ch.Kind == "email" && !ch.VerifiedAt.Valid {
		return errUnverified
	}
	var p Payload
	if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
		return err
	}
	if ch.Kind == "email" {
		return n.sendEmail(ctx, c.To, p.Event, message(p), row.ID)
	}
	if n.Vault == nil {
		return fmt.Errorf("encryption key is required")
	}
	secret, err := n.Vault.Unwrap(ch.SigningSecret, "alert:"+ch.ID)
	if err != nil {
		return err
	}
	body, err := Body(p, c.Format)
	if err != nil {
		return err
	}
	if err = n.Policy.CheckURL(c.URL); err != nil {
		return err
	}
	transport := &http.Transport{DialContext: n.Policy.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(n.Clock.Now().Unix(), 10)
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(row.ID + "." + ts + "."))
	_, _ = mac.Write(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", row.ID)
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if err = n.Hosts.Wait(ctx, strings.ToLower(req.URL.Hostname())); err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("alert webhook delivery failed")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("alert webhook returned %d", response.StatusCode)
	}
	return nil
}
func (n *Notifier) sendEmail(ctx context.Context, to []string, subject, text, messageID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !n.Email.Enabled {
		return fmt.Errorf("email is not configured")
	}
	options := []smtp.Option{smtp.WithPort(n.Email.SMTPPort), smtp.WithTimeout(10 * time.Second), smtp.WithTLSPolicy(smtp.TLSMandatory)}
	// DECISION: local SMTP relays may be plaintext; remote SMTP requires STARTTLS.
	host := net.ParseIP(n.Email.SMTPHost)
	if n.Email.SMTPHost == "localhost" || host != nil && host.IsLoopback() {
		options = append(options, smtp.WithTLSPolicy(smtp.NoTLS))
	}
	if n.Email.Username != "" {
		options = append(options, smtp.WithSMTPAuth(smtp.SMTPAuthPlain), smtp.WithUsername(n.Email.Username), smtp.WithPassword(n.Email.Password))
	}
	client, err := smtp.NewClient(n.Email.SMTPHost, options...)
	if err != nil {
		return fmt.Errorf("SMTP configuration: %w", err)
	}
	m := smtp.NewMsg()
	m.SetDateWithValue(n.Clock.Now())
	m.SetMessageIDWithValue(messageID + "@cronpad")
	if err = m.From(n.Email.From); err != nil {
		return err
	}
	if err = m.To(to...); err != nil {
		return err
	}
	m.Subject(subject)
	m.SetBodyString(smtp.TypeTextPlain, text)
	m.AddAlternativeString(smtp.TypeTextHTML, "<html><body><pre>"+html.EscapeString(text)+"</pre></body></html>")
	if err = client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("SMTP delivery failed")
	}
	return nil
}
