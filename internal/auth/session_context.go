package auth

import (
	"context"
	"time"
)

func (s *sessionStore) DeleteCtx(ctx context.Context, token string) error {
	copy := *s
	copy.ctx = ctx
	return copy.Delete(token)
}
func (s *sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	copy := *s
	copy.ctx = ctx
	return copy.Find(token)
}
func (s *sessionStore) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	copy := *s
	copy.ctx = ctx
	return copy.Commit(token, b, expiry)
}
