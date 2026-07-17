package queue

import (
	"testing"
	"time"
)

func TestBackoffDoublesAndCaps(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1 * time.Second, // 1600ms would exceed max, so it caps here
		1 * time.Second, // stays capped
	}

	for i, w := range want {
		if got := b.Next(); got != w {
			t.Errorf("Next() call #%d = %v, want %v", i+1, got, w)
		}
	}
}

func TestBackoffResetStartsOverAtBase(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)

	b.Next()
	b.Next() // current is now 200ms

	b.Reset()

	if got := b.Next(); got != 100*time.Millisecond {
		t.Errorf("Next() after Reset() = %v, want base (100ms)", got)
	}
}