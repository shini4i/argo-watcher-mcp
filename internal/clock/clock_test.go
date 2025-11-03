package clock

import (
	"testing"
	"time"
)

func TestSystemClockNow(t *testing.T) {
	sut := SystemClock{}

	before := time.Now().UTC()
	got := sut.Now()
	after := time.Now().UTC()

	if got.Before(before) || got.After(after) {
		t.Fatalf("expected Now to return between %s and %s, got %s", before, after, got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("expected time in UTC, got %s", got.Location())
	}
}

func TestFixedClockNow(t *testing.T) {
	want := time.Date(2024, time.August, 1, 10, 0, 0, 0, time.UTC)
	sut := FixedClock{At: want}

	if got := sut.Now(); !got.Equal(want) {
		t.Fatalf("expected fixed time %s, got %s", want, got)
	}
}
