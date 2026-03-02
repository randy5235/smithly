// Package gatekeeper enforces a domain allowlist for code skill outbound network access.
// Unknown domains are denied by default; an optional ApprovalFunc can prompt the user interactively.
package gatekeeper

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"smithly.dev/internal/db"
)

// ApprovalFunc is called when a domain is unknown. It should return true to allow.
type ApprovalFunc func(domain string) bool

// Gatekeeper checks outbound domain access against a database-backed allowlist.
type Gatekeeper struct {
	store    db.Store
	approve  ApprovalFunc
	defaults map[string]bool
}

// New creates a gatekeeper with the given store and optional approval callback.
// If approve is nil, unknown domains are denied (headless mode).
func New(store db.Store, approve ApprovalFunc) *Gatekeeper {
	return &Gatekeeper{
		store:    store,
		approve:  approve,
		defaults: defaultDomains(),
	}
}

// CheckDomain returns true if the domain is allowed to be accessed.
// Order: DB lookup → built-in defaults → approval func → deny.
func (g *Gatekeeper) CheckDomain(ctx context.Context, domain string) bool {
	domain = normalizeDomain(domain)

	// Check DB first
	entry, err := g.store.GetDomain(ctx, domain)
	if err == nil {
		if err := g.store.TouchDomain(ctx, domain); err != nil {
			slog.Error("gatekeeper: touch domain failed", "domain", domain, "err", err)
		}
		return entry.Status == "allow"
	}
	if !errors.Is(err, db.ErrNotFound) {
		// Real DB error — log and deny (fail closed).
		slog.Error("gatekeeper: domain lookup failed", "domain", domain, "err", err)
		return false
	}

	// Check built-in defaults
	if g.defaults[domain] {
		// Persist to DB so it shows up in listings
		if err := g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:    domain,
			Status:    "allow",
			GrantedBy: "default",
		}); err != nil {
			slog.Error("gatekeeper: set domain failed", "domain", domain, "err", err)
		}
		if err := g.store.TouchDomain(ctx, domain); err != nil {
			slog.Error("gatekeeper: touch domain failed", "domain", domain, "err", err)
		}
		return true
	}

	// Interactive approval
	if g.approve != nil && g.approve(domain) {
		if err := g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:    domain,
			Status:    "allow",
			GrantedBy: "user",
		}); err != nil {
			slog.Error("gatekeeper: set domain failed", "domain", domain, "err", err)
		}
		if err := g.store.TouchDomain(ctx, domain); err != nil {
			slog.Error("gatekeeper: touch domain failed", "domain", domain, "err", err)
		}
		return true
	}

	return false
}

// SeedSkillDomains pre-approves domains declared in a skill manifest.
// It won't override existing user-set denials (preserves user intent).
func (g *Gatekeeper) SeedSkillDomains(ctx context.Context, domains []string, skillName string) []string {
	var seeded []string
	for _, d := range domains {
		d = normalizeDomain(d)

		// Don't override existing entries (especially user denials)
		_, err := g.store.GetDomain(ctx, d)
		if err == nil {
			continue // already exists
		}
		if !errors.Is(err, db.ErrNotFound) {
			slog.Error("gatekeeper: seed domain lookup failed", "domain", d, "err", err)
			continue
		}

		if err := g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:      d,
			Status:      "allow",
			GrantedBy:   "skill:" + skillName,
			RequestedBy: skillName,
		}); err != nil {
			slog.Error("gatekeeper: seed domain failed", "domain", d, "skill", skillName, "err", err)
			continue
		}
		seeded = append(seeded, d)
	}
	return seeded
}

// normalizeDomain lowercases the domain and strips any port suffix.
// Uses net.SplitHostPort for correct IPv6 handling.
func normalizeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = host
	}
	return domain
}

// defaultDomains returns the set of pre-approved domains.
func defaultDomains() map[string]bool {
	return map[string]bool{
		"api.openai.com":             true,
		"api.anthropic.com":          true,
		"openrouter.ai":              true,
		"api.github.com":             true,
		"ntfy.sh":                    true,
		"pypi.org":                   true,
		"registry.npmjs.org":         true,
		"raw.githubusercontent.com":  true,
	}
}
