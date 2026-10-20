// Package secrets encrypts workspace credentials and other recoverable secrets.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

type Vault struct {
	aead  cipher.AEAD
	Store store.Store
}

func New(key string, s store.Store) (*Vault, error) {
	b, e := base64.StdEncoding.DecodeString(key)
	if e != nil || len(b) != 32 {
		return nil, fmt.Errorf("secrets.encryption_key must contain 32 base64 encoded bytes")
	}
	block, e := aes.NewCipher(b)
	if e != nil {
		return nil, fmt.Errorf("AES: %w", e)
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, fmt.Errorf("GCM: %w", e)
	}
	return &Vault{a, s}, nil
}
func (v *Vault) Seal(value []byte, binding string) ([]byte, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return nil, fmt.Errorf("nonce: %w", e)
	}
	return v.aead.Seal(n, n, value, []byte(binding)), nil
}
func (v *Vault) Open(value []byte, binding string) ([]byte, error) {
	if len(value) < v.aead.NonceSize() {
		return nil, fmt.Errorf("invalid ciphertext")
	}
	n := v.aead.NonceSize()
	b, e := v.aead.Open(nil, value[:n], value[n:], []byte(binding))
	if e != nil {
		return nil, fmt.Errorf("decrypt: %w", e)
	}
	return b, nil
}

// Wrap preserves the JSON constraint on signing_secrets while encrypting its contents.
func (v *Vault) Wrap(value, binding string) (string, error) {
	b, e := v.Seal([]byte(value), binding)
	if e != nil {
		return "", e
	}
	out, e := json.Marshal(map[string]string{"encrypted": base64.StdEncoding.EncodeToString(b)})
	return string(out), e
}
func (v *Vault) Unwrap(value, binding string) (string, error) {
	var obj struct {
		Encrypted string `json:"encrypted"`
	}
	if e := json.Unmarshal([]byte(value), &obj); e != nil || obj.Encrypted == "" {
		return "", fmt.Errorf("unencrypted credential")
	}
	b, e := base64.StdEncoding.DecodeString(obj.Encrypted)
	if e != nil {
		return "", fmt.Errorf("ciphertext: %w", e)
	}
	out, e := v.Open(b, binding)
	return string(out), e
}
func (v *Vault) Resolve(ctx context.Context, workspace, name string) (string, error) {
	b, e := v.Store.ControlReader().GetSecret(ctx, sqlitegen.GetSecretParams{WorkspaceID: workspace, Name: name})
	if e != nil {
		return "", fmt.Errorf("secret %s not found", name)
	}
	out, e := v.Open(b, workspace+":"+name)
	return string(out), e
}
func (v *Vault) Resolver(workspace string) sink.SecretResolver {
	return sink.SecretFunc(func(ctx context.Context, name string) (string, error) { return v.Resolve(ctx, workspace, name) })
}

// Migrate encrypts legacy signing secrets and idempotency responses atomically.
func (v *Vault) Migrate(ctx context.Context) error {
	return v.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		jobs, e := tx.Control.AllSigningSecrets(ctx)
		if e != nil {
			return e
		}
		for _, j := range jobs {
			var obj map[string]json.RawMessage
			if json.Unmarshal([]byte(j.SigningSecrets.String), &obj) == nil && obj["encrypted"] != nil {
				if _, e = v.Unwrap(j.SigningSecrets.String, "signing:"+j.ID); e != nil {
					return e
				}
				continue
			}
			s, e := v.Wrap(j.SigningSecrets.String, "signing:"+j.ID)
			if e != nil {
				return e
			}
			if e = tx.Control.SetSigningSecrets(ctx, sqlitegen.SetSigningSecretsParams{ID: j.ID, SigningSecrets: sqlText(s)}); e != nil {
				return e
			}
		}
		channels, e := tx.Control.AllAlertSigningSecrets(ctx)
		if e != nil {
			return e
		}
		for _, ch := range channels {
			if _, e = v.Unwrap(ch.SigningSecret, "alert:"+ch.ID); e != nil {
				return e
			}
		}
		records, e := tx.Control.AllIdempotency(ctx)
		if e != nil {
			return e
		}
		for _, r := range records {
			var obj map[string]json.RawMessage
			if json.Unmarshal([]byte(r.Response), &obj) == nil && obj["encrypted"] != nil {
				if _, e = v.Unwrap(r.Response, "idempotency:"+r.WorkspaceID+":"+r.Key); e != nil {
					return e
				}
				continue
			}
			s, e := v.Wrap(r.Response, "idempotency:"+r.WorkspaceID+":"+r.Key)
			if e != nil {
				return e
			}
			if e = tx.Control.SetIdempotencyResponse(ctx, sqlitegen.SetIdempotencyResponseParams{WorkspaceID: r.WorkspaceID, Key: r.Key, Response: s}); e != nil {
				return e
			}
		}
		return nil
	})
}

func sqlText(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
