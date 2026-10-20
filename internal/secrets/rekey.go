package secrets

import (
	"context"
	"fmt"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// Rekey changes all encrypted values in one transaction. The server must be stopped.
func (v *Vault) Rekey(ctx context.Context, newKey string) error {
	next, e := New(newKey, v.Store)
	if e != nil {
		return e
	}
	return v.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		rows, e := tx.Control.AllSecrets(ctx)
		if e != nil {
			return e
		}
		for _, row := range rows {
			binding := row.WorkspaceID + ":" + row.Name
			plain, e := v.Open(row.Ciphertext, binding)
			if e != nil {
				return fmt.Errorf("decrypt stored secret: %w", e)
			}
			b, e := next.Seal(plain, binding)
			if e != nil {
				return e
			}
			if e = tx.Control.PutSecret(ctx, sqlitegen.PutSecretParams{WorkspaceID: row.WorkspaceID, Name: row.Name, Ciphertext: b, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}); e != nil {
				return e
			}
		}
		jobs, e := tx.Control.AllSigningSecrets(ctx)
		if e != nil {
			return e
		}
		for _, job := range jobs {
			binding := "signing:" + job.ID
			plain, e := v.Unwrap(job.SigningSecrets.String, binding)
			if e != nil {
				return e
			}
			enc, e := next.Wrap(plain, binding)
			if e != nil {
				return e
			}
			if e = tx.Control.SetSigningSecrets(ctx, sqlitegen.SetSigningSecretsParams{ID: job.ID, SigningSecrets: sqlText(enc)}); e != nil {
				return e
			}
		}
		channels, e := tx.Control.AllAlertSigningSecrets(ctx)
		if e != nil {
			return e
		}
		for _, ch := range channels {
			binding := "alert:" + ch.ID
			plain, e := v.Unwrap(ch.SigningSecret, binding)
			if e != nil {
				return e
			}
			encrypted, e := next.Wrap(plain, binding)
			if e != nil {
				return e
			}
			if e = tx.Control.SetAlertSigningSecret(ctx, sqlitegen.SetAlertSigningSecretParams{ID: ch.ID, SigningSecret: encrypted}); e != nil {
				return e
			}
		}
		records, e := tx.Control.AllIdempotency(ctx)
		if e != nil {
			return e
		}
		for _, record := range records {
			binding := "idempotency:" + record.WorkspaceID + ":" + record.Key
			plain, e := v.Unwrap(record.Response, binding)
			if e != nil {
				return e
			}
			enc, e := next.Wrap(plain, binding)
			if e != nil {
				return e
			}
			if e = tx.Control.SetIdempotencyResponse(ctx, sqlitegen.SetIdempotencyResponseParams{WorkspaceID: record.WorkspaceID, Key: record.Key, Response: enc}); e != nil {
				return e
			}
		}
		return nil
	})
}
