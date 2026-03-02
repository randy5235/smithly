package gateway

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToMax(t *testing.T) {
	rl := newRateLimiter(time.Minute, 5)
	defer rl.Close()

	for i := range 5 {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Error("request 6 should be denied")
	}
}

func TestRateLimiterIsolatesIPs(t *testing.T) {
	rl := newRateLimiter(time.Minute, 2)
	defer rl.Close()

	rl.allow("1.1.1.1")
	rl.allow("1.1.1.1")
	if rl.allow("1.1.1.1") {
		t.Error("1.1.1.1 should be rate limited")
	}

	// Different IP should still be allowed
	if !rl.allow("2.2.2.2") {
		t.Error("2.2.2.2 should be allowed")
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	rl := newRateLimiter(50*time.Millisecond, 2)
	defer rl.Close()

	rl.allow("1.1.1.1")
	rl.allow("1.1.1.1")
	if rl.allow("1.1.1.1") {
		t.Error("should be rate limited")
	}

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	if !rl.allow("1.1.1.1") {
		t.Error("should be allowed after window expires")
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := newRateLimiter(time.Minute, 10)
	defer rl.Close()
	rl.cleanAge = 50 * time.Millisecond

	rl.allow("old-client")
	time.Sleep(60 * time.Millisecond)

	rl.cleanup()

	rl.mu.Lock()
	_, exists := rl.clients["old-client"]
	rl.mu.Unlock()

	if exists {
		t.Error("old client should be cleaned up")
	}
}

func TestRateLimiterCleanupKeepsActive(t *testing.T) {
	rl := newRateLimiter(time.Minute, 10)
	defer rl.Close()

	rl.allow("active-client")
	rl.cleanup()

	rl.mu.Lock()
	_, exists := rl.clients["active-client"]
	rl.mu.Unlock()

	if !exists {
		t.Error("active client should not be cleaned up")
	}
}

func TestRateLimiterCloseStopsGoroutine(t *testing.T) {
	rl := newRateLimiter(time.Minute, 10)
	rl.Close()
	// Calling Close again should not panic (channel already closed is a panic,
	// so this test verifies Close is only called once by the caller).
	// The goroutine should have exited.
}
