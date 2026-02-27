// Package gatekeeper enforces a domain allowlist for code skill outbound network access.
// Unknown domains are denied by default; an optional ApprovalFunc can prompt the user interactively.
package gatekeeper

import (
	"context"
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
		g.store.TouchDomain(ctx, domain)
		return entry.Status == "allow"
	}

	// Check built-in defaults
	if g.defaults[domain] {
		// Persist to DB so it shows up in listings
		g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:    domain,
			Status:    "allow",
			GrantedBy: "default",
		})
		g.store.TouchDomain(ctx, domain)
		return true
	}

	// Interactive approval
	if g.approve != nil && g.approve(domain) {
		g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:    domain,
			Status:    "allow",
			GrantedBy: "user",
		})
		g.store.TouchDomain(ctx, domain)
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
		if _, err := g.store.GetDomain(ctx, d); err == nil {
			continue
		}

		g.store.SetDomain(ctx, &db.DomainEntry{
			Domain:      d,
			Status:      "allow",
			GrantedBy:   "skill:" + skillName,
			RequestedBy: skillName,
		})
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
		"api.openai.com":    true,
		"api.anthropic.com": true,
		"openrouter.ai":     true,
		"api.github.com":    true,
		"ntfy.sh":           true,
		"pypi.org":          true,
		"registry.npmjs.org": true,
		"raw.githubusercontent.com": true,
	}
}
