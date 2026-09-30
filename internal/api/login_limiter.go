package api

import "time"

// Login throttling is layered so that one source guessing passwords cannot lock
// every operator out:
//
//   - each source address has its own failure budget (maxLoginAttemptsPerMinute),
//     which is the normal brute-force control;
//   - a process-wide emergency cap (maxGlobalLoginFailuresPerMinute) still bounds
//     the total guessing rate when many sources are used at once.
//
// Both are rolling one-minute windows. The source is the trusted client address
// (see netx), so a direct client cannot pick a fresh bucket by sending a forged
// X-Forwarded-For.
const (
	// maxGlobalLoginFailuresPerMinute is the emergency cap across all sources.
	maxGlobalLoginFailuresPerMinute = 100
	// maxLoginSources bounds the per-source table. When full, expired entries are
	// dropped first and then the source whose window started earliest.
	maxLoginSources = 4096
)

type loginWindow struct {
	failures    int
	windowStart time.Time
}

func (w *loginWindow) roll(now time.Time) {
	if now.Sub(w.windowStart) >= loginAttemptWindow {
		w.failures, w.windowStart = 0, now
	}
}

type loginLimiter struct {
	global  loginWindow
	sources map[string]*loginWindow
}

func newLoginLimiter() loginLimiter {
	return loginLimiter{sources: make(map[string]*loginWindow)}
}

func (l *loginLimiter) allow(source string, now time.Time) bool {
	l.global.roll(now)
	if l.global.failures >= maxGlobalLoginFailuresPerMinute {
		return false
	}
	window, ok := l.sources[source]
	if !ok {
		return true
	}
	window.roll(now)
	return window.failures < maxLoginAttemptsPerMinute
}

func (l *loginLimiter) recordFailure(source string, now time.Time) {
	l.global.roll(now)
	l.global.failures++
	window, ok := l.sources[source]
	if !ok {
		l.makeRoom(now)
		window = &loginWindow{windowStart: now}
		l.sources[source] = window
	}
	window.roll(now)
	window.failures++
}

// recordSuccess clears only the succeeding source. The global counter is left
// alone: one operator logging in says nothing about a guesser elsewhere.
func (l *loginLimiter) recordSuccess(source string) {
	delete(l.sources, source)
}

func (l *loginLimiter) makeRoom(now time.Time) {
	if len(l.sources) < maxLoginSources {
		return
	}
	for source, window := range l.sources {
		if now.Sub(window.windowStart) >= loginAttemptWindow {
			delete(l.sources, source)
		}
	}
	for len(l.sources) >= maxLoginSources {
		var oldestSource string
		var oldest time.Time
		for source, window := range l.sources {
			if oldestSource == "" || window.windowStart.Before(oldest) {
				oldestSource, oldest = source, window.windowStart
			}
		}
		delete(l.sources, oldestSource)
	}
}
