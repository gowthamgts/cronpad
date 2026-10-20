// Package clock centralizes the injectable clock used by runtime components.
package clock

import "github.com/jonboulle/clockwork"

type Clock = clockwork.Clock

func New() Clock { return clockwork.NewRealClock() }
