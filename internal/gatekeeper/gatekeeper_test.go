package gatekeeper

import (
	"context"
	"path/filepath"
	"testing"

	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
)

func testStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := sqlite.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCheckDomainAllowed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.SetDomain(ctx, &db.DomainEntry{Domain: "api.example.com", Status: "allow", GrantedBy: "user"})

	g := New(s, nil)
	if !g.CheckDomain(ctx, "api.example.com") {
		t.Error("expected allowed domain to be permitted")
	}

	// Verify touch incremented access count
	entry, _ := s.GetDomain(ctx, "api.example.com")
	if entry.AccessCount != 1 {
		t.Errorf("AccessCount = %d, want 1", entry.AccessCount)
	}
}

func TestCheckDomainDenied(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.SetDomain(ctx, &db.DomainEntry{Domain: "evil.com", Status: "deny", GrantedBy: "user"})

	g := New(s, nil)
	if g.CheckDomain(ctx, "evil.com") {
		t.Error("expected denied domain to be rejected")
	}
}

func TestCheckDomainUnknownDenied(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	g := New(s, nil) // no approval func = headless deny
	if g.CheckDomain(ctx, "unknown.com") {
		t.Error("expected unknown domain to be denied in headless mode")
	}
}

func TestCheckDomainApprovalFunc(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	approvedDomain := ""
	approve := func(domain string) bool {
		approvedDomain = domain
		return true
	}

	g := New(s, approve)
	if !g.CheckDomain(ctx, "new-domain.com") {
		t.Error("expected approval func to allow domain")
	}
	if approvedDomain != "new-domain.com" {
		t.Errorf("approval func received %q, want %q", approvedDomain, "new-domain.com")
	}

	// Should be persisted now
	entry, err := s.GetDomain(ctx, "new-domain.com")
	if err != nil {
		t.Fatalf("domain should be persisted: %v", err)
	}
	if entry.Status != "allow" {
		t.Errorf("persisted status = %q, want allow", entry.Status)
	}
}

func TestCheckDomainApprovalDenied(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	g := New(s, func(domain string) bool { return false })
	if g.CheckDomain(ctx, "rejected.com") {
		t.Error("expected rejected domain when approval func returns false")
	}
}

func TestCheckDomainDefaults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	g := New(s, nil)
	if !g.CheckDomain(ctx, "api.openai.com") {
		t.Error("expected default domain to be allowed")
	}

	// Should be persisted
	entry, err := s.GetDomain(ctx, "api.openai.com")
	if err != nil {
		t.Fatalf("default domain should be persisted: %v", err)
	}
	if entry.GrantedBy != "default" {
		t.Errorf("GrantedBy = %q, want default", entry.GrantedBy)
	}
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"API.Example.COM", "api.example.com"},
		{"api.example.com:443", "api.example.com"},
		{"  api.example.com  ", "api.example.com"},
		{"API.EXAMPLE.COM:8080", "api.example.com"},
	}
	for _, tt := range tests {
		got := normalizeDomain(tt.input)
		if got != tt.want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSeedSkillDomains(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	g := New(s, nil)
	seeded := g.SeedSkillDomains(ctx, []string{"api.weather.com", "api.maps.com"}, "weather")

	if len(seeded) != 2 {
		t.Fatalf("seeded = %d, want 2", len(seeded))
	}

	// Check they're stored
	entry, err := s.GetDomain(ctx, "api.weather.com")
	if err != nil {
		t.Fatalf("seeded domain not found: %v", err)
	}
	if entry.GrantedBy != "skill:weather" {
		t.Errorf("GrantedBy = %q, want skill:weather", entry.GrantedBy)
	}
}

func TestSeedSkillDomainsNoOverride(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// User denied this domain
	s.SetDomain(ctx, &db.DomainEntry{Domain: "api.blocked.com", Status: "deny", GrantedBy: "user"})

	g := New(s, nil)
	seeded := g.SeedSkillDomains(ctx, []string{"api.blocked.com", "api.new.com"}, "test")

	// Should only seed api.new.com, not override the denied one
	if len(seeded) != 1 {
		t.Fatalf("seeded = %d, want 1", len(seeded))
	}
	if seeded[0] != "api.new.com" {
		t.Errorf("seeded[0] = %q, want api.new.com", seeded[0])
	}

	// Verify the denied one is still denied
	entry, _ := s.GetDomain(ctx, "api.blocked.com")
	if entry.Status != "deny" {
		t.Errorf("denied domain was overridden: status = %q", entry.Status)
	}
}
