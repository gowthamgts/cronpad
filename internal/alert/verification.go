package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

type verification struct {
	Channel, Workspace, Address, Digest string
	Expires                             int64
	Link                                string `json:"link,omitempty"`
}

func (n *Notifier) VerificationLink(ch sqlitegen.AlertChannel, address string) (string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(ch.Config)))
	b, err := json.Marshal(verification{Channel: ch.ID, Workspace: ch.WorkspaceID, Address: address, Digest: digest, Expires: n.Clock.Now().Add(24 * time.Hour).Unix()})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, n.SigningKey)
	_, _ = mac.Write([]byte(encoded))
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return n.PublicURL + "/api/v1/alert-channels/verify?token=" + url.QueryEscape(token), nil
}
func (n *Notifier) QueueVerification(ctx context.Context, ch sqlitegen.AlertChannel) error {
	var c ChannelConfig
	if err := json.Unmarshal([]byte(ch.Config), &c); err != nil {
		return err
	}
	for _, address := range c.To {
		link, err := n.VerificationLink(ch, address)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(verification{Address: address, Link: link})
		oid, err := id.New(id.Run, n.Clock.Now())
		if err != nil {
			return err
		}
		if err = n.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			return tx.History.InsertAlertOutbox(ctx, historygen.InsertAlertOutboxParams{ID: oid, WorkspaceID: ch.WorkspaceID, RuleID: "verification", ChannelID: ch.ID, EventType: "channel.verify", DedupeKey: ch.ID + ":" + address, Payload: string(raw), Status: "pending", NextAttemptAt: n.Clock.Now().UnixMilli(), CreatedAt: n.Clock.Now().UnixMilli()})
		}); err != nil {
			return err
		}
	}
	return nil
}
func (n *Notifier) Verify(ctx context.Context, token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid verification link")
	}
	mac := hmac.New(sha256.New, n.SigningKey)
	_, _ = mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return fmt.Errorf("invalid verification signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid verification token")
	}
	var v verification
	if err = json.Unmarshal(b, &v); err != nil || v.Expires <= n.Clock.Now().Unix() {
		return fmt.Errorf("verification link expired")
	}
	err = n.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		ch, err := tx.Control.GetAlertChannel(ctx, sqlitegen.GetAlertChannelParams{ID: v.Channel, WorkspaceID: v.Workspace})
		if err != nil {
			return err
		}
		if v.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(ch.Config))) {
			return fmt.Errorf("channel addresses changed")
		}
		now := sql.NullInt64{Int64: n.Clock.Now().UnixMilli(), Valid: true}
		count, err := tx.Control.VerifyAlertAddress(ctx, sqlitegen.VerifyAlertAddressParams{ChannelID: ch.ID, Address: v.Address, VerifiedAt: now})
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("address not found")
		}
		return tx.Control.MarkAlertChannelVerified(ctx, sqlitegen.MarkAlertChannelVerifiedParams{ID: ch.ID, VerifiedAt: now})
	})
	if err != nil {
		return err
	}
	auditID, err := id.New(id.Run, n.Clock.Now())
	if err != nil {
		return err
	}
	return n.Store.History().WriteAudit(ctx, historygen.WriteAuditParams{ID: auditID, WorkspaceID: v.Workspace, Actor: "system", Action: "alert_channels.verify", Target: sql.NullString{String: v.Channel, Valid: true}, CreatedAt: n.Clock.Now().UnixMilli()})

}

// Test attempts a single delivery without consuming the rule outbox's retry budget.
func (n *Notifier) Test(ctx context.Context, ch sqlitegen.AlertChannel) error {
	p := Payload{Event: "run.failed", WorkspaceID: ch.WorkspaceID, OccurredAt: n.Clock.Now(), URL: n.PublicURL + "/alerts"}
	p.Job.Name = "Test alert"
	p.Job.Tags = []string{}
	p.Run.ScheduledAt = n.Clock.Now()
	b, _ := json.Marshal(p)
	oid, err := id.New(id.Run, n.Clock.Now())
	if err != nil {
		return err
	}
	return n.deliver(ctx, ch, historygen.AlertOutbox{ID: oid, WorkspaceID: ch.WorkspaceID, EventType: p.Event, Payload: string(b)})
}
