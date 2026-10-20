package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// Duration accepts Go duration syntax and decimal day components (one day is 24h).
type Duration time.Duration

var days = regexp.MustCompile(`([+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+))d`)

func (d *Duration) UnmarshalText(b []byte) error {
	s := string(b)
	s = days.ReplaceAllStringFunc(s, func(part string) string {
		n, _ := strconv.ParseFloat(part[:len(part)-1], 64)
		return strconv.FormatFloat(n*24, 'f', -1, 64) + "h"
	})
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(b), err)
	}
	*d = Duration(v)
	return nil
}

// Duration returns the standard library duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

// ValidMilliseconds reports whether SQLite can represent this timeout.
func (d Duration) ValidMilliseconds() bool {
	return d >= 0 && float64(d.Duration().Milliseconds()) <= float64(math.MaxInt32)
}
