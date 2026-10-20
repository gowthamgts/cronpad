package outbound

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

func TestPerHostTokenBucketDelaysAndCancels(t *testing.T) {
	clk := clockwork.NewFakeClock()
	h := &Hosts{Clock: clk, Rate: 2}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if e := h.Wait(ctx, "a"); e != nil {
			t.Fatal(e)
		}
	}
	done := make(chan error, 1)
	go func() { done <- h.Wait(ctx, "a") }()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if e := clk.BlockUntilContext(waitCtx, 1); e != nil {
		t.Fatal(e)
	}
	if e := h.Wait(ctx, "b"); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		t.Fatal("not delayed", e)
	default:
	}
	clk.Advance(500 * time.Millisecond)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if e := h.Wait(canceled, "a"); e == nil {
		t.Fatal("cancellation ignored")
	}
}
