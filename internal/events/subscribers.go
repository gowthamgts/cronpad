package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

// Multi publishes each event to every bus in order.
type Multi []Bus

func (m Multi) Publish(ctx context.Context, e Event) {
	for _, b := range m {
		b.Publish(ctx, e)
	}
}

// Subscribers hands events to extensions on a bounded, nonblocking queue.
// DECISION: like the audit bus, overflow drops the newest event with a warning;
// slow subscribers never delay a worker or the dispatcher.
type Subscribers struct {
	mu          sync.RWMutex
	subscribers []func(context.Context, ext.Event)
	queue       chan ext.Event
	Log         *slog.Logger
}

func NewSubscribers(log *slog.Logger) *Subscribers {
	return &Subscribers{queue: make(chan ext.Event, 1024), Log: log}
}

func (s *Subscribers) Subscribe(fn func(context.Context, ext.Event)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers = append(s.subscribers, fn)
}

func (s *Subscribers) Publish(ctx context.Context, e Event) {
	s.mu.RLock()
	active := len(s.subscribers) > 0
	s.mu.RUnlock()
	if !active {
		return
	}
	select {
	case s.queue <- ext.Event{Type: e.Type, WorkspaceID: e.WorkspaceID, JobID: e.JobID, RunID: e.RunID, Attempt: e.Attempt, Error: e.Error, At: e.At.UTC()}:
	default:
		if s.Log != nil {
			s.Log.WarnContext(ctx, "event subscriber queue overflow", logx.WorkspaceID, e.WorkspaceID)
		}
	}
}

func (s *Subscribers) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-s.queue:
			s.mu.RLock()
			subscribers := append([]func(context.Context, ext.Event){}, s.subscribers...)
			s.mu.RUnlock()
			for _, fn := range subscribers {
				s.deliver(ctx, fn, event)
			}
		}
	}
}

func (s *Subscribers) deliver(ctx context.Context, fn func(context.Context, ext.Event), e ext.Event) {
	defer func() {
		if p := recover(); p != nil && s.Log != nil {
			s.Log.ErrorContext(ctx, "event subscriber panic", logx.Error, fmt.Sprint(p))
		}
	}()
	fn(ctx, e)
}
