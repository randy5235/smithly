package gateway

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter tracks request counts per IP using a sliding window.
type rateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientWindow
	window   time.Duration
	maxReqs  int
	cleanAge time.Duration // evict entries older than this
	stop     chan struct{}
}

type clientWindow struct {
	timestamps []time.Time
}

func newRateLimiter(window time.Duration, maxReqs int) *rateLimiter {
	rl := &rateLimiter{
		clients:  make(map[string]*clientWindow),
		window:   window,
		maxReqs:  maxReqs,
		cleanAge: 5 * time.Minute,
		stop:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Close stops the background cleanup goroutine.
func (rl *rateLimiter) Close() {
	close(rl.stop)
}

func (rl *rateLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stop:
			return
		}
	}
}

// allow checks if the IP is within rate limits. Returns true if allowed.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	cw, ok := rl.clients[ip]
	if !ok {
		cw = &clientWindow{}
		rl.clients[ip] = cw
	}

	// Remove timestamps outside the window
	valid := cw.timestamps[:0]
	for _, t := range cw.timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	cw.timestamps = valid

	if len(cw.timestamps) >= rl.maxReqs {
		return false
	}

	cw.timestamps = append(cw.timestamps, now)
	return true
}

func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-rl.cleanAge)
	for ip, cw := range rl.clients {
		if len(cw.timestamps) == 0 {
			delete(rl.clients, ip)
			continue
		}
		// If the newest timestamp is old, remove the entry
		if cw.timestamps[len(cw.timestamps)-1].Before(cutoff) {
			delete(rl.clients, ip)
		}
	}
}

// extractIP gets the client IP from a request, stripping the port.
func extractIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
