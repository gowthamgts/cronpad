package retry

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestPolicy(t *testing.T) {
	defaults := Policy{3, "exponential", config.Duration(10 * time.Second), config.Duration(time.Minute), 2, .2}
	for _, tc := range []struct {
		input string
		cap   int
		valid bool
	}{{`{}`, 0, true}, {`{"max_attempts":5,"initial_interval":"2s","max_interval":"1m"}`, 0, true}, {`{"max_attempts":11}`, 10, false}, {`{"initial_interval":"oops"}`, 0, false}, {`{"backoff":"linear"}`, 0, false}, {`{"jitter":2}`, 0, false}, {`{"max_interval":"1s"}`, 0, false}} {
		t.Run(tc.input, func(t *testing.T) {
			p, e := Decode([]byte(tc.input), defaults, tc.cap)
			if (e == nil) != tc.valid {
				t.Fatal(p, e)
			}
			if e == nil {
				b, e := json.Marshal(p)
				if e != nil {
					t.Fatal(e)
				}
				var round Policy
				if e = json.Unmarshal(b, &round); e != nil || round != p {
					t.Fatal(round, e, string(b))
				}
			}
		})
	}
	p := defaults
	p.Jitter = 0
	for _, tc := range []struct {
		n          int
		hint, want time.Duration
	}{{1, 0, 10 * time.Second}, {1, 2 * time.Second, 2 * time.Second}, {2, 0, 20 * time.Second}, {1000, 0, time.Minute}, {1, 2 * time.Hour, time.Minute}} {
		if got := p.Delay(tc.n, nil, tc.hint); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	p.Backoff = "fixed"
	if p.Delay(9, nil, 0) != 10*time.Second {
		t.Fatal("fixed changed")
	}
	p = defaults
	a, b := rand.New(rand.NewSource(42)), rand.New(rand.NewSource(42))
	for range 1000 {
		x, y := p.Delay(1, a, 0), p.Delay(1, b, 0)
		if x != y || x < 8*time.Second || x > 12*time.Second {
			t.Fatal(x, y)
		}
	}
}
