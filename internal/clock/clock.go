package clock

import "time"

// Clock provides current time readings.
type Clock interface {
	Now() time.Time
}

// SystemClock implements Clock using time.Now.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// FixedClock returns a constant time, useful for tests.
type FixedClock struct {
	At time.Time
}

// Now returns the fixed instant.
func (c FixedClock) Now() time.Time {
	return c.At
}
