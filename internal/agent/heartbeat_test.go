package agent_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"smithly.dev/internal/agent"
)

func TestParseHeartbeatConfig(t *testing.T) {
	// Default values
	hc := agent.ParseHeartbeatConfig("", "", true, "")
	if hc.Interval != 30*time.Minute {
		t.Errorf("default interval = %v, want 30m", hc.Interval)
	}
	if hc.QuietStart != -1 {
		t.Errorf("default quiet start = %d, want -1", hc.QuietStart)
	}

	// Custom interval
	hc = agent.ParseHeartbeatConfig("1h", "", true, "")
	if hc.Interval != time.Hour {
		t.Errorf("interval = %v, want 1h", hc.Interval)
	}

	// Quiet hours
	hc = agent.ParseHeartbeatConfig("", "22-7", true, "")
	if hc.QuietStart != 22 {
		t.Errorf("quiet start = %d, want 22", hc.QuietStart)
	}
	if hc.QuietEnd != 7 {
		t.Errorf("quiet end = %d, want 7", hc.QuietEnd)
	}

	// Both
	hc = agent.ParseHeartbeatConfig("15m", "9-17", true, "")
	if hc.Interval != 15*time.Minute {
		t.Errorf("interval = %v, want 15m", hc.Interval)
	}
	if hc.QuietStart != 9 || hc.QuietEnd != 17 {
		t.Errorf("quiet = %d-%d, want 9-17", hc.QuietStart, hc.QuietEnd)
	}
}

func TestBootNoContent(t *testing.T) {
	// An agent with no BOOT.md should return empty string
	mock := &mockLLM{
		responses: []mockResponse{},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	// Workspace.Boot is empty by default

	result, err := a.Boot(t.Context(), nil)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func TestBootWithContent(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{content: "Boot sequence complete."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Workspace.Boot = "Run startup checklist:\n- Check systems\n- Report status"

	result, err := a.Boot(t.Context(), nil)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if result != "Boot sequence complete." {
		t.Errorf("result = %q", result)
	}

	// Verify the boot message was persisted
	msgs, _ := a.Store.GetMessages(t.Context(), "test-agent", 50)
	if len(msgs) < 1 {
		t.Fatal("no messages persisted")
	}
	if msgs[0].Content != "Run startup checklist:\n- Check systems\n- Report status" {
		t.Errorf("boot message = %q", msgs[0].Content)
	}
}
