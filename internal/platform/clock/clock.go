// Package clock provides the time source used by services and workers so that
// deadline driven business rules stay testable without sleeping.
package clock

import (
	"sync"
	"time"
)

// Clock is the narrow time abstraction consumed by the rest of the platform.
type Clock interface {
	// Now returns the current instant in UTC.
	Now() time.Time
	// In returns the current instant rendered in the given location.
	In(loc *time.Location) time.Time
}

// System is the production clock backed by the operating system.
type System struct{}

// Now returns the current UTC instant.
func (System) Now() time.Time { return time.Now().UTC() }

// In returns the current instant in the requested location.
func (System) In(loc *time.Location) time.Time {
	if loc == nil {
		return time.Now().UTC()
	}
	return time.Now().In(loc)
}

// Fixed is a deterministic clock. Tests advance it explicitly instead of
// relying on wall clock progress.
type Fixed struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFixed builds a deterministic clock anchored at the given instant.
func NewFixed(at time.Time) *Fixed {
	return &Fixed{now: at.UTC()}
}

// Now returns the currently configured instant.
func (f *Fixed) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

// In renders the configured instant in the requested location.
func (f *Fixed) In(loc *time.Location) time.Time {
	if loc == nil {
		return f.Now()
	}
	return f.Now().In(loc)
}

// Set replaces the configured instant.
func (f *Fixed) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = at.UTC()
}

// Advance moves the configured instant forward.
func (f *Fixed) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// BusinessLocation resolves the campus timezone. When the host image ships
// without a zone database the fixed +08:00 offset keeps deadline arithmetic
// correct instead of silently falling back to UTC.
func BusinessLocation(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}
