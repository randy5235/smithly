package agent

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// HeartbeatConfig holds the scheduling parameters.
type HeartbeatConfig struct {
	Interval   time.Duration
	QuietStart int // hour (0-23), -1 = no quiet hours
	QuietEnd   int // hour (0-23)
}

// ParseHeartbeatConfig parses interval and quiet hours strings from config.
func ParseHeartbeatConfig(interval, quietHours string) HeartbeatConfig {
	hc := HeartbeatConfig{
		Interval:   30 * time.Minute,
		QuietStart: -1,
	}

	if interval != "" {
		if d, err := time.ParseDuration(interval); err == nil {
			hc.Interval = d
		}
	}

	// Parse quiet hours like "22-7" (10pm to 7am)
	if quietHours != "" {
		parts := strings.SplitN(quietHours, "-", 2)
		if len(parts) == 2 {
			if start, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				if end, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					hc.QuietStart = start
					hc.QuietEnd = end
				}
			}
		}
	}

	return hc
}

// StartHeartbeat runs a goroutine that periodically sends HEARTBEAT.md content
// as a user message. Stops when ctx is cancelled.
func (a *Agent) StartHeartbeat(ctx context.Context, hc HeartbeatConfig) {
	if a.Workspace.Heartbeat == "" {
		return
	}

	go func() {
		ticker := time.NewTicker(hc.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if isQuietHour(hc) {
					continue
				}
				_, err := a.Chat(ctx, a.Workspace.Heartbeat, nil)
				if err != nil {
					log.Printf("heartbeat for %s: %v", a.ID, err)
				}
			}
		}
	}()
}

func isQuietHour(hc HeartbeatConfig) bool {
	if hc.QuietStart < 0 {
		return false
	}

	hour := time.Now().Hour()

	if hc.QuietStart < hc.QuietEnd {
		// e.g., 9-17 (quiet during business hours)
		return hour >= hc.QuietStart && hour < hc.QuietEnd
	}
	// e.g., 22-7 (quiet overnight, wraps around midnight)
	return hour >= hc.QuietStart || hour < hc.QuietEnd
}
