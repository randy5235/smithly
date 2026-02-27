package agent

import (
	"strings"
	"testing"

	"smithly.dev/internal/config"
)

func TestServicesEmpty(t *testing.T) {
	s := &Services{}
	if got := s.SystemPromptSection(); got != "" {
		t.Errorf("empty services should return empty string, got %q", got)
	}
}

func TestServicesNil(t *testing.T) {
	var s *Services
	if got := s.SystemPromptSection(); got != "" {
		t.Errorf("nil services should return empty string, got %q", got)
	}
}

func TestServicesDataStores(t *testing.T) {
	s := &Services{
		DataStores: []config.DataStoreConfig{
			{Type: "sqlite", Path: "/tmp/test.db"},
		},
	}
	got := s.SystemPromptSection()

	if !strings.Contains(got, "SMITHLY_SQLITE_PATH") {
		t.Error("should contain SMITHLY_SQLITE_PATH")
	}
	if !strings.Contains(got, "SMITHLY_DB_TYPE") {
		t.Error("should contain SMITHLY_DB_TYPE")
	}
}

func TestServicesDataStoresURL(t *testing.T) {
	s := &Services{
		DataStores: []config.DataStoreConfig{
			{Type: "postgres", URL: "postgres://localhost/test"},
		},
	}
	got := s.SystemPromptSection()

	if !strings.Contains(got, "SMITHLY_POSTGRES_URL") {
		t.Error("should contain SMITHLY_POSTGRES_URL")
	}
	if !strings.Contains(got, "SMITHLY_DB_TYPE") {
		t.Error("should contain SMITHLY_DB_TYPE")
	}
}

func TestServicesSidecar(t *testing.T) {
	s := &Services{
		SidecarURL: "http://127.0.0.1:18791",
	}
	got := s.SystemPromptSection()

	if !strings.Contains(got, "http://127.0.0.1:18791") {
		t.Error("should contain sidecar URL")
	}
	if !strings.Contains(got, "SMITHLY_TOKEN") {
		t.Error("should contain SMITHLY_TOKEN")
	}
	if !strings.Contains(got, "GET /secrets/{name}") {
		t.Error("should list secrets endpoint")
	}
}

func TestServicesSecrets(t *testing.T) {
	s := &Services{
		SidecarURL:  "http://127.0.0.1:18791",
		SecretNames: []string{"openai_key", "github_token"},
	}
	got := s.SystemPromptSection()

	if !strings.Contains(got, "openai_key") {
		t.Error("should list secret name openai_key")
	}
	if !strings.Contains(got, "github_token") {
		t.Error("should list secret name github_token")
	}
}

func TestServicesCombined(t *testing.T) {
	s := &Services{
		DataStores: []config.DataStoreConfig{
			{Type: "sqlite", Path: "/tmp/test.db"},
			{Type: "redis", URL: "redis://localhost:6379"},
		},
		SidecarURL:  "http://127.0.0.1:18791",
		SecretNames: []string{"api_key"},
	}
	got := s.SystemPromptSection()

	if !strings.Contains(got, "## Available Services") {
		t.Error("should have header")
	}
	if !strings.Contains(got, "### Data Stores") {
		t.Error("should have data stores section")
	}
	if !strings.Contains(got, "### Sidecar API") {
		t.Error("should have sidecar section")
	}
	if !strings.Contains(got, "### Secrets") {
		t.Error("should have secrets section")
	}
	if !strings.Contains(got, "SMITHLY_REDIS_URL") {
		t.Error("should contain SMITHLY_REDIS_URL")
	}
}
