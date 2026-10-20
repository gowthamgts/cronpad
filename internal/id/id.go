// Package id creates lexicographically sortable, resource-prefixed identifiers.
package id

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	Job       = "job"
	Run       = "run"
	Workspace = "ws"
	Key       = "key"
	User      = "usr"
)

func New(kind string, now time.Time) (string, error) {
	switch kind {
	case Job, Run, Workspace, Key, User:
	default:
		return "", fmt.Errorf("unknown ID kind %q", kind)
	}
	u, e := ulid.New(ulid.Timestamp(now), rand.Reader)
	if e != nil {
		return "", fmt.Errorf("generate ID: %w", e)
	}
	return kind + "_" + u.String(), nil
}
