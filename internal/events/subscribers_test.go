package events

import (
	"context"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

type recorder []Event

func (r *recorder) Publish(_ context.Context, e Event) { *r = append(*r, e) }

func TestMultiPublishesToEveryBus(t *testing.T) {
	var a, b recorder
	Multi{&a, &b}.Publish(t.Context(), Event{Type: RunFailed})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("a=%v b=%v", a, b)
	}
}

func TestSubscribersDeliverInOrderAndSurvivePanics(t *testing.T) {
	s := NewSubscribers(nil)
	s.Publish(t.Context(), Event{Type: RunSucceeded})
	if len(s.queue) != 0 {
		t.Fatal("queued without subscribers")
	}
	got := make(chan ext.Event, 2)
	s.Subscribe(func(context.Context, ext.Event) { panic("subscriber bug") })
	s.Subscribe(func(_ context.Context, e ext.Event) { got <- e })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	s.Publish(ctx, Event{Type: RunFailed, JobID: "job_1"})
	s.Publish(ctx, Event{Type: JobAutoDisabled, JobID: "job_1"})
	for _, want := range []string{RunFailed, JobAutoDisabled} {
		select {
		case e := <-got:
			if e.Type != want || e.JobID != "job_1" {
				t.Fatalf("got %+v, want %s", e, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s not delivered", want)
		}
	}
	cancel()
	<-done
}

func TestSubscribersDropWhenFull(t *testing.T) {
	s := NewSubscribers(nil)
	s.Subscribe(func(context.Context, ext.Event) {})
	for range cap(s.queue) + 5 {
		s.Publish(t.Context(), Event{Type: RunSucceeded})
	}
	if len(s.queue) != cap(s.queue) {
		t.Fatalf("queue %d of %d", len(s.queue), cap(s.queue))
	}
}
