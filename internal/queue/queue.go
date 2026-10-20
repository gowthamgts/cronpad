// Package queue provides atomic occurrence leasing and dispatcher leadership.
package queue

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Occurrence struct {
	JobID       string
	ScheduledAt time.Time
	Attempt     int
	Version     int64
	Trigger     string
}

func (o Occurrence) Format() string {
	return fmt.Sprintf("%s|%d|%d|%d|%s", o.JobID, o.ScheduledAt.UnixMilli(), o.Attempt, o.Version, o.Trigger)
}
func ParseOccurrence(s string) (Occurrence, error) {
	var o Occurrence
	parts := strings.Split(s, "|")
	if len(parts) != 5 || !strings.HasPrefix(parts[0], "job_") || len(parts[0]) < 5 {
		return o, fmt.Errorf("invalid occurrence %q", s)
	}
	for _, r := range parts[0] {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if !valid {
			return o, fmt.Errorf("invalid job ID")
		}
	}
	ms, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil {
		return o, fmt.Errorf("scheduled timestamp: %w", e)
	}
	a, e := strconv.ParseInt(parts[2], 10, 32)
	if e != nil || a < 0 {
		return o, fmt.Errorf("invalid attempt")
	}
	v, e := strconv.ParseInt(parts[3], 10, 64)
	if e != nil || v < 1 {
		return o, fmt.Errorf("invalid version")
	}
	if parts[4] != "s" && parts[4] != "m" && parts[4] != "r" {
		return o, fmt.Errorf("invalid trigger")
	}
	o = Occurrence{parts[0], time.UnixMilli(ms).UTC(), int(a), v, parts[4]}
	return o, nil
}

type Item struct {
	Member string
	Due    time.Time
}

// Queue times have millisecond precision. Ack removes from both sets even after reap.
// Extend returns 0 absent, 1 extended, 2 rescued from due after reap.
type Queue interface {
	Ping(context.Context) error
	Stats(context.Context) (Stats, error)
	RequeueAll(context.Context, time.Time) (int, error)
	ScanDue(context.Context, uint64) ([]string, uint64, error)
	ScanInflight(context.Context, uint64) ([]string, uint64, error)
	Add(context.Context, string, time.Time, bool) error
	Remove(context.Context, string) error
	Claim(context.Context, time.Time, int, time.Duration) ([]Item, error)
	Ack(context.Context, string, string, time.Time) error
	Defer(context.Context, string, time.Time) error
	Extend(context.Context, string, time.Time) (int, error)
	Reap(context.Context, time.Time, int) (int, error)
	ReapAndList(context.Context, time.Time, int) ([]string, error)
	Peek(context.Context) (time.Time, bool, error)
	Contains(context.Context, []string) ([]bool, error)
	Acquire(context.Context, string, time.Duration) (bool, error)
	Renew(context.Context, string, time.Duration) (bool, error)
	Release(context.Context, string) (bool, error)
	SetEpoch(context.Context, string) error
	Epoch(context.Context) (string, error)
	SetHeartbeat(context.Context, time.Time) error
	Heartbeat(context.Context) (time.Time, error)
}

// Stats is a point-in-time queue size snapshot.
type Stats struct{ Due, Inflight int64 }
