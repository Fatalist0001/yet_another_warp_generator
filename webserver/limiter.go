package webserver

import (
	"sync"
	"time"
)

// rateLimiter — скользящее окно: не больше limit событий за window на ключ.
//
// Ключ — IP клиента или "global"; карта чистится, когда разрастается, чтобы
// долгоживущий процесс не копил записи о каждом посетителе.
type rateLimiter struct {
	mu          sync.Mutex
	hits        map[string][]time.Time
	limit       int
	window      time.Duration
	now         func() time.Time
	lastCleanup time.Time
}

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		hits:   make(map[string][]time.Time),
		limit:  limit,
		window: window,
		now:    now,
	}
}

// allow сообщает, можно ли пропустить событие, и засчитывает его.
func (l *rateLimiter) allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	hits := l.hits[key]
	first := 0
	for first < len(hits) && !hits[first].After(cutoff) {
		first++
	}
	hits = hits[first:]
	if len(hits) >= l.limit {
		l.hits[key] = hits
		return false
	}
	l.hits[key] = append(hits, now)
	// Карта чистится не чаще раза в окно: иначе при большом числе свежих
	// ключей каждый запрос сканировал бы её целиком.
	if len(l.hits) >= 4096 && (l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= l.window) {
		l.cleanup(now)
		l.lastCleanup = now
	}
	return true
}

// cleanup удаляет устаревшие ключи. Вызывается под mu и только при заметном
// размере карты: сканировать её на каждом запросе незачем.
func (l *rateLimiter) cleanup(now time.Time) {
	if len(l.hits) < 4096 {
		return
	}
	cutoff := now.Add(-l.window)
	for key, hits := range l.hits {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(l.hits, key)
		}
	}
}
