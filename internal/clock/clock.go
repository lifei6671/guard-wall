// Package clock provides the process clock contract shared by runtime schedulers.
package clock

import "time"

// Timer exposes the signal and cancellation operations required by schedulers.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Clock provides the current time and one-shot timers.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// NewWallClock returns a Clock backed by the Go runtime wall clock.
func NewWallClock() Clock {
	return wallClock{}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) NewTimer(delay time.Duration) Timer {
	return wallTimer{timer: time.NewTimer(delay)}
}

type wallTimer struct {
	timer *time.Timer
}

func (t wallTimer) C() <-chan time.Time { return t.timer.C }

func (t wallTimer) Stop() bool { return t.timer.Stop() }
