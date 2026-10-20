package results

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/telemetry"
)

type flushStore struct{ store.Store }

func (flushStore) WithTx(_ context.Context, _ store.Database, fn func(store.Tx) error) error {
	return fn(store.Tx{})
}

func TestFlushIntervalAndBatchBound(t *testing.T) {
	for _, db := range []store.Database{store.History, store.Control} {
		t.Run(string(db), func(t *testing.T) {
			clk := clockwork.NewFakeClock()
			metrics, err := telemetry.NewInstruments(noop.NewMeterProvider().Meter("flush"))
			if err != nil {
				t.Fatal(err)
			}
			writer := New(flushStore{}, clk, metrics, time.Minute)
			writer.FlushInterval = 37 * time.Millisecond
			writer.MaxBatch = 2
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- writer.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			guard, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if err := clk.BlockUntilContext(guard, 2); err != nil {
				t.Fatal(err)
			}
			submit := func() <-chan error {
				future, err := writer.Submit(ctx, db, func(context.Context, store.Tx) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { result <- future.Wait(ctx) }()
				return result
			}
			first := submit()
			queue := writer.control
			if db == store.History {
				queue = writer.history
			}
			// Ensure the command is admitted to the batch before firing its ticker.
			for len(queue) > 0 {
				select {
				case <-guard.Done():
					t.Fatal("writer did not consume command")
				default:
					runtime.Gosched()
				}
			}
			clk.Advance(36 * time.Millisecond)
			select {
			case <-first:
				t.Fatal("flushed before configured tick")
			default:
			}
			clk.Advance(time.Millisecond)
			select {
			case err := <-first:
				if err != nil {
					t.Fatal(err)
				}
			case <-guard.Done():
				t.Fatal("configured tick did not flush")
			}
			second, third := submit(), submit()
			for _, result := range []<-chan error{second, third} {
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-guard.Done():
					t.Fatal("max batch did not flush without clock advance")
				}
			}
		})
	}
}

// gateStore signals each transaction after applying it and holds it until released.
type gateStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s gateStore) WithTx(_ context.Context, _ store.Database, fn func(store.Tx) error) error {
	err := fn(store.Tx{})
	s.entered <- struct{}{}
	<-s.release
	return err
}

func TestGroupCommit(t *testing.T) {
	for _, db := range []store.Database{store.History, store.Control} {
		t.Run(string(db), func(t *testing.T) {
			metrics, err := telemetry.NewInstruments(noop.NewMeterProvider().Meter("flush"))
			if err != nil {
				t.Fatal(err)
			}
			gate := gateStore{entered: make(chan struct{}, 16), release: make(chan struct{}, 16)}
			// A fake clock that is never advanced proves no ticker is involved.
			writer := New(gate, clockwork.NewFakeClock(), metrics, time.Minute)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- writer.Run(ctx) }()
			t.Cleanup(func() { cancel(); close(gate.release); <-done })
			guard, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			applied := make(chan int, 16)
			submit := func(n int, solo bool) <-chan error {
				apply := func(context.Context, store.Tx) error { applied <- n; return nil }
				result := make(chan error, 1)
				if solo {
					go func() { result <- writer.Maintain(ctx, db, apply) }()
					return result
				}
				future, err := writer.Submit(ctx, db, apply)
				if err != nil {
					t.Fatal(err)
				}
				go func() { result <- future.Wait(ctx) }()
				return result
			}
			wait := func(what string, ch <-chan error) {
				t.Helper()
				select {
				case err := <-ch:
					if err != nil {
						t.Fatal(what, err)
					}
				case <-guard.Done():
					t.Fatal(what, "timed out")
				}
			}
			batch := func(want ...int) {
				t.Helper()
				select {
				case <-gate.entered:
				case <-guard.Done():
					t.Fatal("no commit started")
				}
				for _, n := range want {
					if got := <-applied; got != n {
						t.Fatalf("applied %d, want %d", got, n)
					}
				}
				select {
				case got := <-applied:
					t.Fatalf("extra command %d in batch", got)
				default:
				}
			}
			// An idle writer commits the first command on its own, immediately.
			first := submit(1, false)
			batch(1)
			// Commands that arrive while that commit runs share the next one.
			second, third := submit(2, false), submit(3, false)
			queue := writer.control
			if db == store.History {
				queue = writer.history
			}
			for len(queue) < 2 {
				select {
				case <-guard.Done():
					t.Fatal("commands not queued")
				default:
					runtime.Gosched()
				}
			}
			gate.release <- struct{}{}
			wait("first", first)
			batch(2, 3)
			gate.release <- struct{}{}
			wait("second", second)
			wait("third", third)
			// Maintenance never shares a batch, even when group committing.
			solo := submit(4, true)
			batch(4)
			gate.release <- struct{}{}
			wait("solo", solo)
		})
	}
}
