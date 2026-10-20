package queue

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
)

type scanPage struct {
	members []string
	expires time.Time
}
type Memory struct {
	scans             map[uint64]scanPage
	scanID            uint64
	mu                sync.Mutex
	due, inflight     map[string]int64
	clock             clockwork.Clock
	token, epoch      string
	expiry, heartbeat time.Time
}

func NewMemory(c clockwork.Clock) *Memory {
	return &Memory{due: map[string]int64{}, inflight: map[string]int64{}, clock: c}
}
func (q *Memory) Add(ctx context.Context, m string, t time.Time, nx bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.due[m]; !nx || !ok {
		q.due[m] = t.UnixMilli()
	}
	return nil
}
func (q *Memory) Remove(ctx context.Context, m string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.due, m)
	delete(q.inflight, m)
	return nil
}
func ordered(set map[string]int64) []string {
	v := make([]string, 0, len(set))
	for m := range set {
		v = append(v, m)
	}
	sort.Slice(v, func(i, j int) bool {
		if set[v[i]] == set[v[j]] {
			return v[i] < v[j]
		}
		return set[v[i]] < set[v[j]]
	})
	return v
}
func (q *Memory) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && lease < time.Millisecond {
		return nil, fmt.Errorf("lease must be at least 1ms")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []Item{}
	for _, m := range ordered(q.due) {
		if len(out) >= limit || q.due[m] > now.UnixMilli() {
			break
		}
		out = append(out, Item{m, time.UnixMilli(q.due[m]).UTC()})
		delete(q.due, m)
		q.inflight[m] = now.UnixMilli() + lease.Milliseconds()
	}
	return out, nil
}
func (q *Memory) Ack(ctx context.Context, m, retry string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.due, m)
	delete(q.inflight, m)
	if retry != "" {
		q.due[retry] = at.UnixMilli()
	}
	return nil
}
func (q *Memory) Extend(ctx context.Context, m string, at time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.inflight[m]; ok {
		q.inflight[m] = at.UnixMilli()
		return 1, nil
	}
	if _, ok := q.due[m]; ok {
		delete(q.due, m)
		q.inflight[m] = at.UnixMilli()
		return 2, nil
	}
	return 0, nil
}
func (q *Memory) Reap(ctx context.Context, now time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, m := range ordered(q.inflight) {
		if n >= limit || q.inflight[m] > now.UnixMilli() {
			break
		}
		delete(q.inflight, m)
		q.due[m] = now.UnixMilli()
		n++
	}
	return n, nil
}
func (q *Memory) Peek(ctx context.Context) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v := ordered(q.due)
	if len(v) == 0 {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(q.due[v[0]]).UTC(), true, nil
}
func (q *Memory) Contains(ctx context.Context, ms []string) ([]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v := make([]bool, len(ms))
	for i, m := range ms {
		_, a := q.due[m]
		_, b := q.inflight[m]
		v[i] = a || b
	}
	return v, nil
}
func (q *Memory) Acquire(ctx context.Context, t string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ttl < time.Millisecond {
		return false, fmt.Errorf("lock TTL must be at least 1ms")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.clock.Now().Before(q.expiry) {
		return false, nil
	}
	q.token = t
	q.expiry = q.clock.Now().Truncate(time.Millisecond).Add(ttl.Truncate(time.Millisecond))
	return true, nil
}
func (q *Memory) Renew(ctx context.Context, t string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ttl < time.Millisecond {
		return false, fmt.Errorf("lock TTL must be at least 1ms")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if t != q.token || !q.clock.Now().Before(q.expiry) {
		return false, nil
	}
	q.expiry = q.clock.Now().Truncate(time.Millisecond).Add(ttl.Truncate(time.Millisecond))
	return true, nil
}
func (q *Memory) Release(ctx context.Context, t string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if t != q.token || !q.clock.Now().Before(q.expiry) {
		return false, nil
	}
	q.token = ""
	q.expiry = time.Time{}
	return true, nil
}
func (q *Memory) SetEpoch(ctx context.Context, v string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.epoch = v
	return nil
}
func (q *Memory) Epoch(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.epoch, nil
}
func (q *Memory) SetHeartbeat(ctx context.Context, t time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeat = t.Truncate(time.Millisecond)
	return nil
}
func (q *Memory) Heartbeat(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.heartbeat, nil
}

// Ping verifies context liveness for the in-memory backend.
func (q *Memory) Ping(ctx context.Context) error { return ctx.Err() }

// Stats returns both cardinalities under one lock.
func (q *Memory) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{int64(len(q.due)), int64(len(q.inflight))}, nil
}

// RequeueAll mirrors atomic orphan recovery without waiting for lease expiry.
func (q *Memory) RequeueAll(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.inflight)
	for m := range q.inflight {
		delete(q.inflight, m)
		q.due[m] = now.UnixMilli()
	}
	return n, nil
}

// ScanDue returns a bounded lexicographically ordered cursor page.
func (q *Memory) ScanDue(ctx context.Context, cursor uint64) ([]string, uint64, error) {
	return q.scan(ctx, cursor, false)
}

// ScanInflight reads a bounded cursor page without changing leases.
func (q *Memory) ScanInflight(ctx context.Context, cursor uint64) ([]string, uint64, error) {
	return q.scan(ctx, cursor, true)
}

func (q *Memory) scan(ctx context.Context, cursor uint64, inflight bool) ([]string, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.scans == nil {
		q.scans = map[uint64]scanPage{}
	}
	now := q.clock.Now()
	for id, page := range q.scans {
		if !now.Before(page.expires) {
			delete(q.scans, id)
		}
	}
	var all []string
	if cursor == 0 {
		set := q.due
		if inflight {
			set = q.inflight
		}
		all = make([]string, 0, len(set))
		for m := range set {
			all = append(all, m)
		}
		sort.Strings(all)
		q.scanID++
		cursor = q.scanID
	} else {
		page := q.scans[cursor]
		all = page.members
		delete(q.scans, cursor)
	}
	end := min(1000, len(all))
	next := uint64(0)
	if end < len(all) {
		q.scans[cursor] = scanPage{all[end:], now.Add(time.Minute)}
		next = cursor
	}
	return all[:end], next, nil
}

// ReapAndList atomically moves expired leases and returns their deterministic order.
func (q *Memory) ReapAndList(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []string{}
	for _, m := range ordered(q.inflight) {
		if len(out) >= limit || q.inflight[m] > now.UnixMilli() {
			break
		}
		delete(q.inflight, m)
		q.due[m] = now.UnixMilli()
		out = append(out, m)
	}
	return out, nil
}

// Defer releases a claim without changing its occurrence identity.
func (q *Memory) Defer(ctx context.Context, member string, at time.Time) error {
	return q.Ack(ctx, member, member, at)
}
