package queue

import "time"

// backoff is a simple doubling backoff capped at max, used while Redis is
// unreachable so a Redis outage doesn't turn into a reconnect-storm.
type backoff struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	return &backoff{base: base, max: max, current: 0}
}

func (b *backoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.base
	} else {
		b.current *= 2
		if b.current > b.max {
			b.current = b.max
		}
	}
	return b.current
}

func (b *backoff) Reset() {
	b.current = 0
}