// Package retry computes bounded delivery retry delays without sleeping.
package retry

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
)

type Policy struct {
	MaxAttempts     int             `json:"max_attempts"`
	Backoff         string          `json:"backoff"`
	InitialInterval config.Duration `json:"initial_interval"`
	MaxInterval     config.Duration `json:"max_interval"`
	Multiplier      float64         `json:"multiplier"`
	Jitter          float64         `json:"jitter"`
}

func Defaults(c config.ConfigRetryDefaults) Policy {
	return Policy{c.MaxAttempts, c.Backoff, c.InitialInterval, c.MaxInterval, c.Multiplier, c.Jitter}
}

// Decode merges a partial JSON object into server defaults.
func Decode(b []byte, defaults Policy, hostedCap int) (Policy, error) {
	p := defaults
	if len(b) > 0 {
		if e := json.Unmarshal(b, &p); e != nil {
			return p, fmt.Errorf("retry: %w", e)
		}
	}
	return p, p.Validate(hostedCap)
}
func (p Policy) Validate(cap int) error {
	if p.MaxAttempts < 1 || (cap > 0 && p.MaxAttempts > cap) {
		return fmt.Errorf("max_attempts outside allowed range")
	}
	if p.Backoff != "fixed" && p.Backoff != "exponential" {
		return fmt.Errorf("invalid backoff")
	}
	if p.InitialInterval <= 0 || p.MaxInterval < p.InitialInterval || p.Multiplier < 1 || math.IsNaN(p.Multiplier) || math.IsInf(p.Multiplier, 0) || p.Jitter < 0 || p.Jitter > 1 || math.IsNaN(p.Jitter) || math.IsInf(p.Jitter, 0) {
		return fmt.Errorf("invalid retry intervals, multiplier or jitter")
	}
	return nil
}

type RNG interface{ Float64() float64 }

// Delay receives retry number 1 for the first retry. All hints and jitter are capped.
func (p Policy) Delay(n int, rng RNG, hint time.Duration) time.Duration {
	d := float64(p.InitialInterval)
	if p.Backoff == "exponential" && n > 1 {
		d *= math.Pow(p.Multiplier, float64(n-1))
	}
	max := float64(p.MaxInterval)
	if d > max {
		d = max
	}
	if rng != nil && p.Jitter > 0 {
		d *= 1 + p.Jitter*(2*rng.Float64()-1)
	}
	if hint > 0 {
		d = float64(hint)
	}
	if d >= max {
		return p.MaxInterval.Duration()
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}
func (p Policy) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MaxAttempts int     `json:"max_attempts"`
		Backoff     string  `json:"backoff"`
		Initial     string  `json:"initial_interval"`
		Max         string  `json:"max_interval"`
		Multiplier  float64 `json:"multiplier"`
		Jitter      float64 `json:"jitter"`
	}{p.MaxAttempts, p.Backoff, p.InitialInterval.String(), p.MaxInterval.String(), p.Multiplier, p.Jitter})
}
func (p *Policy) UnmarshalJSON(b []byte) error {
	type plain Policy
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(b, &fields); e != nil {
		return e
	}
	for _, key := range []string{"initial_interval", "max_interval"} {
		if raw, ok := fields[key]; ok {
			var v string
			if e := json.Unmarshal(raw, &v); e != nil {
				return e
			}
			var d config.Duration
			if e := d.UnmarshalText([]byte(v)); e != nil {
				return e
			}
			if key == "initial_interval" {
				p.InitialInterval = d
			} else {
				p.MaxInterval = d
			}
			delete(fields, key)
		}
	}
	remaining, e := json.Marshal(fields)
	if e != nil {
		return e
	}
	return json.Unmarshal(remaining, (*plain)(p))
}
