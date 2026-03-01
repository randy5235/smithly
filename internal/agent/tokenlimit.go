package agent

import (
	"fmt"
	"time"
)

// DISCLAIMER: Cost estimates are approximate. Actual costs depend on your provider's
// billing, which may differ from the pricing data used here. Smithly is not responsible
// for billing overages. Always monitor your provider's dashboard for authoritative usage.

// CostWindow tracks spending within a rolling time window.
// All costs are tracked in millicents (1/1000 of a cent) for precision without floats.
type CostWindow struct {
	LimitCents float64       // max spend in dollars for this window
	Window     time.Duration // window duration (minimum 1 hour)
	spent      int64         // millicents spent in current window
	started    time.Time     // when current window started
}

// ModelPricing holds per-model token pricing in dollars per million tokens.
type ModelPricing struct {
	InputPerMillion       float64 // cost per 1M input tokens
	OutputPerMillion      float64 // cost per 1M output tokens
	CachedInputPerMillion float64 // cost per 1M cached input tokens (0 = same as input)
}

// Known model pricing (dollars per million tokens).
var knownPricing = map[string]ModelPricing{
	// Anthropic
	"claude-opus-4-6":            {InputPerMillion: 5.0, OutputPerMillion: 25.0, CachedInputPerMillion: 0.50},
	"claude-sonnet-4-6":          {InputPerMillion: 3.0, OutputPerMillion: 15.0, CachedInputPerMillion: 0.30},
	"claude-sonnet-4-6-20250514": {InputPerMillion: 3.0, OutputPerMillion: 15.0, CachedInputPerMillion: 0.30},
	"claude-opus-4-5-20251101":   {InputPerMillion: 5.0, OutputPerMillion: 25.0, CachedInputPerMillion: 0.50},
	"claude-opus-4-5":            {InputPerMillion: 5.0, OutputPerMillion: 25.0, CachedInputPerMillion: 0.50},
	"claude-opus-4-1-20250805":   {InputPerMillion: 15.0, OutputPerMillion: 75.0, CachedInputPerMillion: 1.50},
	"claude-sonnet-4-5-20250929": {InputPerMillion: 3.0, OutputPerMillion: 15.0, CachedInputPerMillion: 0.30},
	"claude-sonnet-4-5":          {InputPerMillion: 3.0, OutputPerMillion: 15.0, CachedInputPerMillion: 0.30},
	"claude-sonnet-4-20250514":   {InputPerMillion: 3.0, OutputPerMillion: 15.0, CachedInputPerMillion: 0.30},
	"claude-haiku-4-5-20251001":  {InputPerMillion: 1.0, OutputPerMillion: 5.0, CachedInputPerMillion: 0.10},
	"claude-haiku-4-5":           {InputPerMillion: 1.0, OutputPerMillion: 5.0, CachedInputPerMillion: 0.10},
	// OpenAI — GPT-5.x + Codex (Responses API)
	"gpt-5.3-codex": {InputPerMillion: 1.75, OutputPerMillion: 14.0, CachedInputPerMillion: 0.175},
	"gpt-5.2-codex": {InputPerMillion: 1.75, OutputPerMillion: 14.0, CachedInputPerMillion: 0.175},
	"gpt-5.2":       {InputPerMillion: 1.75, OutputPerMillion: 14.0, CachedInputPerMillion: 0.175},
	"gpt-5.1-codex": {InputPerMillion: 1.25, OutputPerMillion: 10.0, CachedInputPerMillion: 0.125},
	"gpt-5.1":       {InputPerMillion: 1.25, OutputPerMillion: 10.0, CachedInputPerMillion: 0.125},
	"gpt-5":         {InputPerMillion: 1.25, OutputPerMillion: 10.0, CachedInputPerMillion: 0.125},
	"gpt-5-mini":    {InputPerMillion: 0.25, OutputPerMillion: 2.0, CachedInputPerMillion: 0.025},
	"gpt-5-nano":    {InputPerMillion: 0.05, OutputPerMillion: 0.40, CachedInputPerMillion: 0.005},
	// OpenAI — GPT-4.1
	"gpt-4.1":      {InputPerMillion: 2.0, OutputPerMillion: 8.0, CachedInputPerMillion: 0.50},
	"gpt-4.1-mini": {InputPerMillion: 0.40, OutputPerMillion: 1.60, CachedInputPerMillion: 0.10},
	"gpt-4.1-nano": {InputPerMillion: 0.10, OutputPerMillion: 0.40, CachedInputPerMillion: 0.025},
	// OpenAI — GPT-4o
	"gpt-4o":      {InputPerMillion: 2.50, OutputPerMillion: 10.0, CachedInputPerMillion: 1.25},
	"gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60, CachedInputPerMillion: 0.075},
	// OpenAI — o-series
	"o3":      {InputPerMillion: 2.0, OutputPerMillion: 8.0, CachedInputPerMillion: 0.50},
	"o3-mini": {InputPerMillion: 1.10, OutputPerMillion: 4.40, CachedInputPerMillion: 0.55},
	"o4-mini": {InputPerMillion: 1.10, OutputPerMillion: 4.40, CachedInputPerMillion: 0.275},
	// Gemini
	"gemini-3.1-pro-preview": {InputPerMillion: 2.0, OutputPerMillion: 12.0, CachedInputPerMillion: 0.20},
	"gemini-3-pro-preview":   {InputPerMillion: 2.0, OutputPerMillion: 12.0, CachedInputPerMillion: 0.20},
	"gemini-3-flash-preview": {InputPerMillion: 0.50, OutputPerMillion: 3.0, CachedInputPerMillion: 0.05},
	"gemini-2.5-pro":         {InputPerMillion: 1.25, OutputPerMillion: 10.0, CachedInputPerMillion: 0.125},
	"gemini-2.5-flash":       {InputPerMillion: 0.30, OutputPerMillion: 2.50, CachedInputPerMillion: 0.03},
	"gemini-2.5-flash-lite":  {InputPerMillion: 0.10, OutputPerMillion: 0.40, CachedInputPerMillion: 0.01},
}

// LookupPricing returns pricing for a model, falling back to Sonnet 4.6 if unknown.
func LookupPricing(model string) ModelPricing {
	if p, ok := knownPricing[model]; ok {
		return p
	}
	// Default to Sonnet 4.6 pricing as a safe estimate
	return knownPricing["claude-sonnet-4-6-20250514"]
}

// calculateCost returns cost in millicents for a given usage.
func calculateCost(pricing ModelPricing, inputTokens, outputTokens, cachedTokens int) int64 {
	// millicents = (tokens / 1_000_000) * dollars * 100_000
	// Simplified: millicents = tokens * dollars_per_million / 10
	input := int64(float64(inputTokens) * pricing.InputPerMillion / 10.0)
	output := int64(float64(outputTokens) * pricing.OutputPerMillion / 10.0)
	cached := int64(float64(cachedTokens) * pricing.CachedInputPerMillion / 10.0)
	return input + output + cached
}

// ParseCostWindows parses cost limit configs into live windows.
func ParseCostWindows(configs []CostLimitConfig) []*CostWindow {
	var windows []*CostWindow
	for _, c := range configs {
		d, err := parseWindowDuration(c.Window)
		if err != nil || c.Dollars <= 0 {
			continue
		}
		windows = append(windows, &CostWindow{
			LimitCents: c.Dollars,
			Window:     d,
		})
	}
	return windows
}

// parseWindowDuration parses duration strings like "8h", "24h", "168h" (weekly), "720h" (monthly).
// Also supports shorthand: "daily", "weekly", "monthly".
// Minimum is 1 hour.
func parseWindowDuration(s string) (time.Duration, error) {
	switch s {
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	case "monthly":
		return 30 * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < time.Hour {
		return 0, fmt.Errorf("minimum window is 1 hour, got %v", d)
	}
	return d, nil
}

// record adds cost to the window. Returns true if the limit is now exceeded.
func (cw *CostWindow) record(millicents int64) bool {
	now := time.Now()

	// Reset window if expired
	if cw.started.IsZero() || now.Sub(cw.started) >= cw.Window {
		cw.started = now
		cw.spent = 0
	}

	cw.spent += millicents
	limitMillicents := int64(cw.LimitCents * 100_000)
	return cw.spent >= limitMillicents
}

// exceeded returns true if the window's limit is currently exceeded.
func (cw *CostWindow) exceeded() bool {
	now := time.Now()

	// Window expired — usage resets
	if !cw.started.IsZero() && now.Sub(cw.started) >= cw.Window {
		return false
	}

	limitMillicents := int64(cw.LimitCents * 100_000)
	return cw.spent >= limitMillicents
}

// remaining returns how long until this window resets.
func (cw *CostWindow) remaining() time.Duration {
	if cw.started.IsZero() {
		return 0
	}
	end := cw.started.Add(cw.Window)
	r := time.Until(end)
	if r < 0 {
		return 0
	}
	return r
}

// spentDollars returns the amount spent in this window as dollars.
func (cw *CostWindow) spentDollars() float64 {
	return float64(cw.spent) / 100_000.0
}

// checkCostWindows checks all cost windows. Returns the first exceeded window, or nil.
func checkCostWindows(windows []*CostWindow) *CostWindow {
	for _, w := range windows {
		if w.exceeded() {
			return w
		}
	}
	return nil
}

// recordCostWindows records spending across all windows.
// Returns the first window that becomes exceeded, or nil.
func recordCostWindows(windows []*CostWindow, millicents int64) *CostWindow {
	var first *CostWindow
	for _, w := range windows {
		if w.record(millicents) && first == nil {
			first = w
		}
	}
	return first
}

// CostLimitConfig is the TOML-friendly config for a single cost window.
type CostLimitConfig struct {
	Dollars float64 `toml:"dollars"` // max spend in dollars for this window
	Window  string  `toml:"window"`  // duration: "8h", "24h", "weekly", "monthly"
}

// formatWindow returns a human-readable description of a window.
func (cw *CostWindow) formatWindow() string {
	switch {
	case cw.Window == time.Hour:
		return "1 hour"
	case cw.Window == 24*time.Hour:
		return "daily"
	case cw.Window == 7*24*time.Hour:
		return "weekly"
	case cw.Window == 30*24*time.Hour:
		return "monthly"
	default:
		return cw.Window.String()
	}
}
