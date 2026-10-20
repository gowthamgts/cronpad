// Package outbound bounds shared destination traffic using an injected clock.
package outbound

import (
	"context"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
)

type bucket struct {
	tokens float64
	at     time.Time
}

// Hosts is a process-wide, per-destination-host token bucket shared by all tenants.
type Hosts struct {
	Clock   clockwork.Clock
	Rate    int
	mu      sync.Mutex
	buckets map[string]bucket
}

// Wait delays excess requests; cancellation never drops an admitted delivery silently.
func (h *Hosts) Wait(ctx context.Context, host string) error {
	if h == nil || h.Rate <= 0 {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		now := h.Clock.Now()
		if h.buckets == nil {
			h.buckets = map[string]bucket{}
		}
		b, ok := h.buckets[host]
		if !ok {
			b = bucket{float64(h.Rate), now}
		}
		b.tokens = min(float64(h.Rate), b.tokens+max(0, now.Sub(b.at).Seconds())*float64(h.Rate))
		b.at = now
		wait := time.Duration(0)
		if b.tokens >= 1 {
			b.tokens--
		} else {
			wait = time.Duration((1 - b.tokens) / float64(h.Rate) * float64(time.Second))
			wait = max(time.Nanosecond, wait)
		}
		h.buckets[host] = b
		// DECISION: idle buckets refill completely; evict them opportunistically to bound memory.
		if len(h.buckets) > 1024 {
			for key, old := range h.buckets {
				if now.Sub(old.at) > time.Minute {
					delete(h.buckets, key)
				}
			}
		}
		h.mu.Unlock()
		if wait == 0 {
			return nil
		}
		timer := h.Clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.Chan():
		}
	}
}
