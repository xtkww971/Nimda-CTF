package queue

import "time"

// Redis에 못 붙는 동안 재연결 간격을 2배씩 늘리다 max에서 멈추는 간단한 백오프.
// Redis가 잠깐 죽어도 재연결 폭풍이 안 나게 한다.
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