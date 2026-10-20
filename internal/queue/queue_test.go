package queue

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

func TestConformance(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			expire := func() { clockwork.NewRealClock().Sleep(5 * time.Millisecond) }
			newQ := func(t *testing.T) Queue {
				if backend == "memory" {
					return NewMemory(clockwork.NewRealClock())
				}
				q, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "queue.db"), clockwork.NewRealClock(), 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = q.Close() })
				return q
			}
			ctx := context.Background()
			now := time.Unix(1700000000, 0).UTC()
			add := func(t *testing.T, q Queue, m string, at time.Time, nx bool) {
				t.Helper()
				if e := q.Add(ctx, m, at, nx); e != nil {
					t.Fatal(e)
				}
			}
			t.Run("defer_preserves_member_without_extending_its_logical_time", func(t *testing.T) {
				q := newQ(t)
				member := "job_defer|1700000000000|0|1|s"
				add(t, q, member, now, false)
				items, e := q.Claim(ctx, now, 1, time.Minute)
				if e != nil || len(items) != 1 {
					t.Fatal(items, e)
				}
				if e = q.Defer(ctx, member, now.Add(250*time.Millisecond)); e != nil {
					t.Fatal(e)
				}
				stats, e := q.Stats(ctx)
				if e != nil || stats.Inflight != 0 || stats.Due != 1 {
					t.Fatal(stats, e)
				}
				items, e = q.Claim(ctx, now.Add(249*time.Millisecond), 1, time.Minute)
				if e != nil || len(items) != 0 {
					t.Fatal(items, e)
				}
				items, e = q.Claim(ctx, now.Add(250*time.Millisecond), 1, time.Minute)
				if e != nil || len(items) != 1 || items[0].Member != member {
					t.Fatal(items, e)
				}
			})
			t.Run("inflight_scan_is_read_only_and_paginated", func(t *testing.T) {
				q := newQ(t)
				const size = 2501
				for i := range size {
					add(t, q, fmt.Sprintf("member_%04d", i), now, false)
				}
				if claimed, err := q.Claim(ctx, now, size, time.Hour); err != nil || len(claimed) != size {
					t.Fatal(len(claimed), err)
				}
				add(t, q, "due_only", now, false)
				found := map[string]bool{}
				var cursor uint64
				pages := 0
				for {
					members, next, err := q.ScanInflight(ctx, cursor)
					if err != nil {
						t.Fatal(err)
					}
					pages++
					for _, member := range members {
						found[member] = true
					}
					if next == 0 {
						break
					}
					cursor = next
				}
				if len(found) != size || found["due_only"] || pages < 2 {
					t.Fatal(len(found), pages)
				}
				stats, err := q.Stats(ctx)
				if err != nil || stats.Inflight != size || stats.Due != 1 {
					t.Fatal(stats, err)
				}
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if _, _, err := q.ScanInflight(cancelled, 0); err == nil {
					t.Fatal("cancelled scan succeeded")
				}
			})
			t.Run("empty", func(t *testing.T) {
				q := newQ(t)
				if _, ok, e := q.Peek(ctx); ok || e != nil {
					t.Fatal(ok, e)
				}
				v, e := q.Claim(ctx, now, 10, time.Minute)
				if len(v) != 0 || e != nil {
					t.Fatal(v, e)
				}
			})
			t.Run("claim_order_and_limit", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "b", now, false)
				add(t, q, "a", now, false)
				add(t, q, "future", now.Add(time.Second), false)
				v, e := q.Claim(ctx, now, 1, time.Minute)
				if e != nil || !reflect.DeepEqual(v, []Item{{"a", now}}) {
					t.Fatal(v, e)
				}
				v, e = q.Claim(ctx, now, 10, time.Minute)
				if e != nil || !reflect.DeepEqual(v, []Item{{"b", now}}) {
					t.Fatal(v, e)
				}
				at, ok, e := q.Peek(ctx)
				if e != nil || !ok || !at.Equal(now.Add(time.Second)) {
					t.Fatal(at, ok, e)
				}
			})
			t.Run("add_idempotent_and_nx", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				add(t, q, "a", now.Add(time.Hour), true)
				at, _, _ := q.Peek(ctx)
				if !at.Equal(now) {
					t.Fatal(at)
				}
				add(t, q, "a", now.Add(time.Second), false)
				at, _, _ = q.Peek(ctx)
				if !at.Equal(now.Add(time.Second)) {
					t.Fatal(at)
				}
			})
			t.Run("membership_union", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				add(t, q, "b", now, false)
				_, _ = q.Claim(ctx, now, 1, time.Minute)
				v, e := q.Contains(ctx, []string{"a", "b", "missing"})
				if e != nil || !reflect.DeepEqual(v, []bool{true, true, false}) {
					t.Fatal(v, e)
				}
			})
			t.Run("extend_codes", func(t *testing.T) {
				q := newQ(t)
				n, e := q.Extend(ctx, "absent", now)
				if n != 0 || e != nil {
					t.Fatal(n, e)
				}
				add(t, q, "a", now, false)
				_, _ = q.Claim(ctx, now, 1, time.Second)
				n, e = q.Extend(ctx, "a", now.Add(2*time.Second))
				if n != 1 || e != nil {
					t.Fatal(n, e)
				}
				n, e = q.Reap(ctx, now.Add(time.Second), 10)
				if n != 0 || e != nil {
					t.Fatal(n, e)
				}
				n, e = q.Reap(ctx, now.Add(2*time.Second), 10)
				if n != 1 || e != nil {
					t.Fatal(n, e)
				}
				n, e = q.Extend(ctx, "a", now.Add(time.Minute))
				if n != 2 || e != nil {
					t.Fatal(n, e)
				}
			})
			t.Run("reap_limit", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				add(t, q, "b", now, false)
				_, _ = q.Claim(ctx, now, 2, time.Second)
				n, e := q.Reap(ctx, now.Add(time.Second), 1)
				if n != 1 || e != nil {
					t.Fatal(n, e)
				}
				v, _ := q.Claim(ctx, now.Add(time.Second), 10, time.Minute)
				if len(v) != 1 {
					t.Fatal(v)
				}
			})
			t.Run("ack_retry", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				_, _ = q.Claim(ctx, now, 1, time.Second)
				if e := q.Ack(ctx, "a", "retry", now.Add(time.Second)); e != nil {
					t.Fatal(e)
				}
				v, _ := q.Contains(ctx, []string{"a", "retry"})
				if !reflect.DeepEqual(v, []bool{false, true}) {
					t.Fatal(v)
				}
			})
			t.Run("ack_after_reap", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				_, _ = q.Claim(ctx, now, 1, time.Second)
				_, _ = q.Reap(ctx, now.Add(time.Second), 10)
				if e := q.Ack(ctx, "a", "", now); e != nil {
					t.Fatal(e)
				}
				v, _ := q.Contains(ctx, []string{"a"})
				if v[0] {
					t.Fatal(v)
				}
			})
			t.Run("remove_both", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				_, _ = q.Claim(ctx, now, 1, time.Minute)
				add(t, q, "a", now, false)
				if e := q.Remove(ctx, "a"); e != nil {
					t.Fatal(e)
				}
				v, _ := q.Contains(ctx, []string{"a"})
				if v[0] {
					t.Fatal(v)
				}
			})
			t.Run("lock_tokens", func(t *testing.T) {
				q := newQ(t)
				ok, e := q.Acquire(ctx, "owner", time.Minute)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Acquire(ctx, "other", time.Minute)
				if ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Renew(ctx, "other", time.Minute)
				if ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Release(ctx, "other")
				if ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Renew(ctx, "owner", time.Minute)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Release(ctx, "owner")
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Acquire(ctx, "other", time.Minute)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
			})
			t.Run("lock_expiry", func(t *testing.T) {
				q := newQ(t)
				ok, e := q.Acquire(ctx, "owner", time.Millisecond)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				expire()
				ok, e = q.Renew(ctx, "owner", time.Minute)
				if ok || e != nil {
					t.Fatal(ok, e)
				}
				ok, e = q.Acquire(ctx, "new", time.Minute)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
			})
			t.Run("epoch", func(t *testing.T) {
				q := newQ(t)
				v, e := q.Epoch(ctx)
				if v != "" || e != nil {
					t.Fatal(v, e)
				}
				if e = q.SetEpoch(ctx, "one"); e != nil {
					t.Fatal(e)
				}
				v, e = q.Epoch(ctx)
				if v != "one" || e != nil {
					t.Fatal(v, e)
				}
			})
			t.Run("heartbeat", func(t *testing.T) {
				q := newQ(t)
				v, e := q.Heartbeat(ctx)
				if !v.IsZero() || e != nil {
					t.Fatal(v, e)
				}
				if e = q.SetHeartbeat(ctx, now); e != nil {
					t.Fatal(e)
				}
				v, e = q.Heartbeat(ctx)
				if !v.Equal(now) || e != nil {
					t.Fatal(v, e)
				}
			})
			t.Run("invalid_ttl", func(t *testing.T) {
				q := newQ(t)
				if _, e := q.Acquire(ctx, "x", 0); e == nil {
					t.Fatal("zero TTL")
				}
				if _, e := q.Renew(ctx, "x", 0); e == nil {
					t.Fatal("zero renewal")
				}
				add(t, q, "a", now, false)
				if _, e := q.Claim(ctx, now, 1, 0); e == nil {
					t.Fatal("zero lease")
				}
			})
			t.Run("cancelled_context", func(t *testing.T) {
				q := newQ(t)
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if e := q.Add(cancelled, "a", now, false); e == nil {
					t.Fatal("cancellation ignored")
				}
				v, e := q.Contains(ctx, []string{"a"})
				if e != nil || v[0] {
					t.Fatal(v, e)
				}
			})
			t.Run("millisecond_precision", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				_, e := q.Claim(ctx, now.Add(900*time.Microsecond), 1, 1500*time.Microsecond)
				if e != nil {
					t.Fatal(e)
				}
				n, e := q.Reap(ctx, now.Add(time.Millisecond), 1)
				if n != 1 || e != nil {
					t.Fatal(n, e)
				}
			})
			t.Run("startup_recovery_and_scan", func(t *testing.T) {
				q := newQ(t)
				for n := range 1005 {
					add(t, q, fmt.Sprintf("m_%04d", n), now, false)
				}
				items, err := q.Claim(ctx, now, 1005, 24*time.Hour)
				if err != nil || len(items) != 1005 {
					t.Fatal(len(items), err)
				}
				n, err := q.RequeueAll(ctx, now)
				if err != nil || n != 1005 {
					t.Fatal(n, err)
				}
				stats, err := q.Stats(ctx)
				if err != nil || stats.Due != 1005 || stats.Inflight != 0 {
					t.Fatal(stats, err)
				}
				var cursor uint64
				seen := map[string]bool{}
				for {
					members, next, err := q.ScanDue(ctx, cursor)
					if err != nil {
						t.Fatal(err)
					}
					for _, m := range members {
						seen[m] = true
					}
					cursor = next
					if cursor == 0 {
						break
					}
				}
				if len(seen) != 1005 {
					t.Fatal("scan skipped stable members", len(seen))
				}
				for m := range seen {
					if err = q.Remove(ctx, m); err != nil {
						t.Fatal(err)
					}
				}
				if err = q.Ping(ctx); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("reap_event_members", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				add(t, q, "b", now, false)
				_, err := q.Claim(ctx, now, 2, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				members, err := q.ReapAndList(ctx, now.Add(time.Second), 1)
				if err != nil || !reflect.DeepEqual(members, []string{"a"}) {
					t.Fatal(members, err)
				}
				n, err := q.Reap(ctx, now.Add(time.Second), 10)
				if err != nil || n != 1 {
					t.Fatal(n, err)
				}
			})
			t.Run("zero_limits", func(t *testing.T) {
				q := newQ(t)
				add(t, q, "a", now, false)
				v, e := q.Claim(ctx, now, 0, time.Second)
				if len(v) != 0 || e != nil {
					t.Fatal(v, e)
				}
				n, e := q.Reap(ctx, now, 0)
				if n != 0 || e != nil {
					t.Fatal(n, e)
				}
			})
		})
	}
}
func TestOccurrence(t *testing.T) {
	o := Occurrence{"job_ABC", time.UnixMilli(-1).UTC(), 2, 3, "s"}
	got, e := ParseOccurrence(o.Format())
	if e != nil || got != o {
		t.Fatal(got, e)
	}
	for _, s := range []string{"", "job_a|x|0|1|s", "job_a|1|-1|1|s", "job_a|1|0|0|s", "job_a|1|0|1|x", "job_a|1|0|1|s|extra"} {
		if _, e := ParseOccurrence(s); e == nil {
			t.Fatal(s)
		}
	}
}
func FuzzOccurrence(f *testing.F) {
	f.Add("job_ABC|1700000000000|0|1|s")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		start := clockwork.NewRealClock().Now()
		o, e := ParseOccurrence(s)
		if e == nil {
			p, e := ParseOccurrence(o.Format())
			if e != nil || p != o {
				t.Fatal(s, p, e)
			}
		}
		if clockwork.NewRealClock().Since(start) > 200*time.Millisecond {
			t.Fatalf("occurrence parse exceeded 200ms: %q", s)
		}
	})
}
