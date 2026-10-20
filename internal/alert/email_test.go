package alert

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func smtpServer(t *testing.T) (string, int, <-chan string) {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan string, 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				p := textproto.NewConn(conn)
				_ = p.PrintfLine("220 test SMTP ready")
				for {
					line, err := p.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						_ = p.PrintfLine("250 localhost")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"), strings.HasPrefix(line, "RSET"):
						_ = p.PrintfLine("250 OK")
					case line == "DATA":
						_ = p.PrintfLine("354 send data")
						b, err := p.ReadDotBytes()
						if err != nil {
							return
						}
						messages <- string(b)
						_ = p.PrintfLine("250 queued")
					case line == "QUIT":
						_ = p.PrintfLine("221 bye")
						return
					default:
						_ = p.PrintfLine("250 OK")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return host, number, messages
}
func plainBody(t *testing.T, raw string) string {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain") {
			b, err := io.ReadAll(quotedprintable.NewReader(p))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatal("no text/plain body")
	return ""
}
func TestSMTPVerificationFlowAndUnverifiedSkipped(t *testing.T) {
	host, port, messages := smtpServer(t)
	f, _, n, job := setup(t, "https://example.com")
	n.Email = config.ConfigAlertsEmail{Enabled: true, SMTPHost: host, SMTPPort: port, From: "alerts@example.com"}
	ch := sqlitegen.AlertChannel{ID: "ch_email", WorkspaceID: job.WorkspaceID, Kind: "email", Name: "email", Config: `{"to":["first@example.com","second@example.com"]}`, CreatedAt: f.Clock.Now().UnixMilli()}
	err := f.Store.WithTx(context.Background(), store.Control, func(tx store.Tx) error {
		if err := tx.Control.SaveAlertChannel(context.Background(), sqlitegen.SaveAlertChannelParams{ID: ch.ID, WorkspaceID: ch.WorkspaceID, Kind: ch.Kind, Name: ch.Name, Config: ch.Config, CreatedAt: ch.CreatedAt}); err != nil {
			return err
		}
		for _, address := range []string{"first@example.com", "second@example.com"} {
			if err := tx.Control.AddAlertVerification(context.Background(), sqlitegen.AddAlertVerificationParams{ChannelID: ch.ID, Address: address}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := Payload{Event: "run.failed", WorkspaceID: job.WorkspaceID}
	p.Job.Name = "SMTP test"
	payload, _ := json.Marshal(p)
	f.Run(t, func(ctx context.Context) error {
		return f.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			return tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: "email_alert", WorkspaceID: job.WorkspaceID, RuleID: "test", ChannelID: ch.ID, EventType: p.Event, DedupeKey: "email", Payload: string(payload), Status: "pending", CreatedAt: f.Clock.Now().UnixMilli(), NextAttemptAt: f.Clock.Now().UnixMilli()})
		})
	})
	f.Run(t, n.Poll)
	select {
	case <-messages:
		t.Fatal("unverified email received alert")
	default:
	}
	f.Run(t, func(ctx context.Context) error { return n.QueueVerification(ctx, ch) })
	f.Run(t, n.Poll)
	for i := range 2 {
		raw := <-messages
		if !strings.Contains(raw, "text/html") {
			t.Fatal("HTML alternative missing")
		}
		body := strings.TrimSpace(plainBody(t, raw))
		link, err := url.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		if err = n.Verify(context.Background(), link.Query().Get("token")+"tampered"); err == nil {
			t.Fatal("tampered token accepted")
		}
		if err = n.Verify(context.Background(), link.Query().Get("token")); err != nil {
			t.Fatal(err)
		}
		saved, err := f.Store.ControlReader().GetAlertChannel(context.Background(), sqlitegen.GetAlertChannelParams{ID: ch.ID, WorkspaceID: ch.WorkspaceID})
		if err != nil {
			t.Fatal(err)
		}
		if saved.VerifiedAt.Valid != (i == 1) {
			t.Fatal("channel verified before every address confirmed")
		}
	}
	f.Clock.Advance(time.Minute)
	f.Run(t, n.Poll)
	if body := plainBody(t, <-messages); !strings.Contains(body, "SMTP test") {
		t.Fatal(body)
	}
	var status string
	var attempts int
	if err = f.DBs.History.Reader.QueryRowContext(t.Context(), "SELECT status,attempts FROM alert_outbox WHERE id='email_alert'").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 1 {
		t.Fatal(status, attempts)
	}
	expired, err := n.VerificationLink(ch, "first@example.com")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(expired)
	f.Clock.Advance(25 * time.Hour)
	if err = n.Verify(context.Background(), u.Query().Get("token")); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestMonthlyQuotaOwnerNoticesDeduplicateAndBypassRules(t *testing.T) {
	f, e, n, job := setup(t, "https://example.org")
	host, port, messages := smtpServer(t)
	n.Email = config.ConfigAlertsEmail{Enabled: true, SMTPHost: host, SMTPPort: port, From: "alerts@example.com"}
	e.Config.Alerts.Email = n.Email
	// Owner notices are independent of configured rules and delivered only once.
	if _, err := f.DBs.Control.Writer.ExecContext(context.Background(), `INSERT INTO users(id,email,name,created_at) VALUES('owner','owner@example.com','Owner',0); INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws_test','owner','owner')`); err != nil {
		t.Fatal(err)
	}
	e.rules.Store(nil)
	f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 5, OverQuota: "allow"}, Store: f.Store, Clock: f.Clock}
	for i := 0; i < 7; i++ {
		process(t, f, job)
		f.Clock.Advance(time.Second)
	}
	rows := outbox(t, f)
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.EventType]++
	}
	if counts[events.WorkspaceQuotaWarning] != 1 || counts[events.WorkspaceQuotaExceeded] != 1 {
		t.Fatal(counts)
	}
	f.Run(t, n.Poll)
	if len(messages) != 2 {
		t.Fatal("owner emails", len(messages))
	}
}
func TestAlertEmailDailyQuotaSuppressesAndReportsNextDay(t *testing.T) {
	f, _, n, job := setup(t, "https://example.org")
	host, port, messages := smtpServer(t)
	n.Email = config.ConfigAlertsEmail{Enabled: true, SMTPHost: host, SMTPPort: port, From: "alerts@example.com"}
	n.Limits = &limits.Registry{Config: config.ConfigLimits{MaxAlertEmailsPerDay: 1}, Store: f.Store, Clock: f.Clock}
	if err := f.Store.Control().SaveAlertChannel(context.Background(), sqlitegen.SaveAlertChannelParams{ID: "email", WorkspaceID: job.WorkspaceID, Kind: "email", Name: "mail", Config: `{"to":["owner@example.com"]}`, VerifiedAt: sql.NullInt64{Int64: 1, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string) {
		t.Helper()
		raw, _ := json.Marshal(Payload{Event: "run.failed", WorkspaceID: job.WorkspaceID})
		f.Run(t, func(ctx context.Context) error {
			return f.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
				return tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: id, WorkspaceID: job.WorkspaceID, RuleID: "test", ChannelID: "email", EventType: "run.failed", DedupeKey: id, Payload: string(raw), Status: "pending", CreatedAt: f.Clock.Now().UnixMilli(), NextAttemptAt: f.Clock.Now().UnixMilli()})
			})
		})
	}
	enqueue("first")
	f.Run(t, n.Poll)
	enqueue("second")
	f.Run(t, n.Poll)
	if len(messages) != 1 {
		t.Fatal(len(messages))
	}
	<-messages
	var status string
	if err := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT status FROM alert_outbox WHERE id='second'`).Scan(&status); err != nil || status != "suppressed" {
		t.Fatal(status, err)
	}
	// Move to a new UTC day without iterating the writer ticker for 24 hours.
	n.Clock = clockwork.NewFakeClockAt(f.Clock.Now().Add(24 * time.Hour))
	n.Limits.Clock = n.Clock
	enqueue("third")
	f.Run(t, n.Poll)
	if len(messages) != 1 {
		t.Fatal(len(messages))
	}
	body := plainBody(t, <-messages)
	if !strings.Contains(body, "+1 more failures suppressed") {
		t.Fatal(body)
	}
}
