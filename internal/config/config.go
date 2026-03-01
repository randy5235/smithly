package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Gateway     GatewayConfig     `toml:"gateway"`
	Sandbox     SandboxConfig     `toml:"sandbox"`
	Storage     StorageConfig     `toml:"storage"`
	Search      SearchConfig      `toml:"search"`
	Memory      *MemoryConfig     `toml:"memory"`
	OAuth2      []OAuth2Config    `toml:"oauth2"`
	Notify      NotifyConfig      `toml:"notify"`
	Credentials CredentialsConfig `toml:"credentials"`
	Sidecar     SidecarConfig     `toml:"sidecar"`
	DataStores  []DataStoreConfig `toml:"datastore"`
	Secrets     []SecretConfig    `toml:"secret"`
	Gatekeeper  GatekeeperConfig  `toml:"gatekeeper"`
	Agents      []AgentConfig     `toml:"agents"`
}


type GatewayConfig struct {
	Bind         string `toml:"bind"`
	Port         int    `toml:"port"`
	Token        string `toml:"token"`
	RateLimit    int    `toml:"rate_limit"`    // Max requests per minute per IP (0 = unlimited)
}

type SandboxConfig struct {
	Provider string `toml:"provider"`
	Memory   string `toml:"memory"` // Docker memory limit (default "256m")
	CPUs     string `toml:"cpus"`   // Docker CPU limit (default "1")
}

type StorageConfig struct {
	Database string `toml:"database"`
	FilesDir string `toml:"files_dir"`
}

type AgentConfig struct {
	ID         string           `toml:"id"`
	Model      string           `toml:"model"`
	Workspace  string           `toml:"workspace"`
	Provider   string           `toml:"provider"`
	APIKey     string           `toml:"api_key"`
	BaseURL    string           `toml:"base_url"`
	MaxContext int              `toml:"max_context"` // Context window in tokens (0 = 128k default)
	CostLimits []struct {
		Dollars float64 `toml:"dollars"` // Max spend in USD for this window
		Window  string  `toml:"window"`  // Duration: "8h", "24h", "daily", "weekly", "monthly"
	} `toml:"cost_limits"` // Rolling spending windows
	Pricing   *PricingConfig   `toml:"pricing"`   // Override auto-detected model pricing
	Tools     []string         `toml:"tools"`     // Allowed tool names (empty = all)
	Heartbeat *HeartbeatConfig `toml:"heartbeat"`
}

// PricingConfig allows overriding auto-detected model pricing (dollars per million tokens).
type PricingConfig struct {
	InputPerMillion  float64 `toml:"input_per_million"`  // Cost per 1M input tokens
	OutputPerMillion float64 `toml:"output_per_million"` // Cost per 1M output tokens
	CachedPerMillion float64 `toml:"cached_per_million"` // Cost per 1M cached input tokens
}

type HeartbeatConfig struct {
	Enabled    bool   `toml:"enabled"`
	Interval   string `toml:"interval"`
	QuietHours string `toml:"quiet_hours"`
	AutoResume bool   `toml:"auto_resume"` // Resume paused agent when window expires (default true)
	Skill      string `toml:"skill"`       // Run this code skill instead of LLM chat
}

// MemoryConfig enables optional vector embeddings for hybrid search.
// Omit entirely for FTS5-only search (zero setup).
type MemoryConfig struct {
	EmbeddingProvider string `toml:"embedding_provider"` // "ollama", "openai", "openrouter", etc.
	EmbeddingModel    string `toml:"embedding_model"`    // e.g. "nomic-embed-text"
	EmbeddingBaseURL  string `toml:"embedding_base_url"` // e.g. "http://localhost:11434/v1"
	EmbeddingAPIKey   string `toml:"embedding_api_key"`  // needed for remote providers
	Dimensions        int    `toml:"dimensions"`         // embedding dimensions (e.g. 768)
}

type SearchConfig struct {
	Provider string `toml:"provider"` // "brave" (default), "duckduckgo"
	APIKey   string `toml:"api_key"`  // Brave Search API key
}

type OAuth2Config struct {
	Name         string   `toml:"name"`
	ClientID     string   `toml:"client_id"`
	ClientSecret string   `toml:"client_secret"`
	Scopes       []string `toml:"scopes"`
	AuthURL      string   `toml:"auth_url"`
	TokenURL     string   `toml:"token_url"`
}

type NotifyConfig struct {
	Provider   string `toml:"provider"`    // "ntfy" (default)
	NtfyTopic  string `toml:"ntfy_topic"`
	NtfyServer string `toml:"ntfy_server"` // default: "https://ntfy.sh"
}

type CredentialsConfig struct {
	Provider string `toml:"provider"` // "file" (default)
	Path     string `toml:"path"`     // file path for file provider
}

type SidecarConfig struct {
	Bind string `toml:"bind"` // default "127.0.0.1"
	Port int    `toml:"port"` // default 18791
}

type GatekeeperConfig struct {
	Bind string `toml:"bind"` // default "127.0.0.1"
	Port int    `toml:"port"` // default 18792
}

type DataStoreConfig struct {
	Type string `toml:"type"` // "sqlite", "postgres", "redis", "mongodb", "elasticsearch"
	Path string `toml:"path"` // for sqlite
	URL  string `toml:"url"`  // for everything else
}

// SecretConfig defines a secret available to skills via the sidecar API.
// Skills request secrets via GET /secrets/{name} — values never touch env vars.
type SecretConfig struct {
	Name  string `toml:"name"`            // secret name, used in GET /secrets/{name}
	Value string `toml:"value,omitempty"` // literal value
	Env   string `toml:"env,omitempty"`   // read from controller's environment variable
}

func DefaultConfig() Config {
	return Config{
		Gateway: GatewayConfig{
			Bind:      "127.0.0.1",
			Port:      18789,
			RateLimit: 60,
		},
		Sandbox: SandboxConfig{
			Provider: "none",
		},
		Storage: StorageConfig{
			Database: "smithly.db",
			FilesDir: "data/skills/",
		},
		Search: SearchConfig{
			Provider: "brave",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Generate token if not set
	if cfg.Gateway.Token == "" {
		token, err := generateToken()
		if err != nil {
			return nil, fmt.Errorf("generating token: %w", err)
		}
		cfg.Gateway.Token = token
		// Write token back to config file
		if err := appendToken(path, token); err != nil {
			return nil, fmt.Errorf("saving token: %w", err)
		}
	}

	return &cfg, nil
}

// WriteDefault writes a default smithly.toml for first-run setup.
func WriteDefault(dir string, agent AgentConfig, search SearchConfig) error {
	path := filepath.Join(dir, "smithly.toml")

	token, err := generateToken()
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, `[gateway]
bind = "127.0.0.1"
port = 18789
token = %q
rate_limit = 60  # max requests per minute per IP, 0 = unlimited

[sandbox]
provider = "none"

[storage]
database = "smithly.db"
files_dir = "data/skills/"

[search]
provider = %q
`, token, search.Provider)

	if search.APIKey != "" {
		fmt.Fprintf(f, "api_key = %q\n", search.APIKey)
	}

	fmt.Fprintf(f, `
[[agents]]
id = %q
model = %q
workspace = %q
`, agent.ID, agent.Model, agent.Workspace)

	if agent.Provider != "" {
		fmt.Fprintf(f, "provider = %q\n", agent.Provider)
	}
	if agent.APIKey != "" {
		fmt.Fprintf(f, "api_key = %q\n", agent.APIKey)
	}
	if agent.BaseURL != "" {
		fmt.Fprintf(f, "base_url = %q\n", agent.BaseURL)
	}

	return nil
}

// AppendAgent adds a new agent section to an existing config file.
func AppendAgent(path string, agent AgentConfig) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "\n[[agents]]\nid = %q\nmodel = %q\nworkspace = %q\n",
		agent.ID, agent.Model, agent.Workspace)
	if agent.Provider != "" {
		fmt.Fprintf(f, "provider = %q\n", agent.Provider)
	}
	if agent.APIKey != "" {
		fmt.Fprintf(f, "api_key = %q\n", agent.APIKey)
	}
	if agent.BaseURL != "" {
		fmt.Fprintf(f, "base_url = %q\n", agent.BaseURL)
	}
	if len(agent.Tools) > 0 {
		fmt.Fprintf(f, "tools = [")
		for i, t := range agent.Tools {
			if i > 0 {
				fmt.Fprint(f, ", ")
			}
			fmt.Fprintf(f, "%q", t)
		}
		fmt.Fprint(f, "]\n")
	}
	return nil
}

// RemoveAgent removes an agent from the config by rewriting the file without it.
func RemoveAgent(path string, agentID string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}

	found := false
	var remaining []AgentConfig
	for _, a := range cfg.Agents {
		if a.ID == agentID {
			found = true
			continue
		}
		remaining = append(remaining, a)
	}
	if !found {
		return fmt.Errorf("agent %q not found", agentID)
	}

	// Rewrite config with remaining agents
	return rewriteConfig(path, cfg, remaining)
}

// rewriteConfig writes the full config with a new set of agents.
func rewriteConfig(path string, cfg *Config, agents []AgentConfig) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "[gateway]\nbind = %q\nport = %d\ntoken = %q\nrate_limit = %d\n",
		cfg.Gateway.Bind, cfg.Gateway.Port, cfg.Gateway.Token, cfg.Gateway.RateLimit)

	fmt.Fprintf(f, "\n[sandbox]\nprovider = %q\n", cfg.Sandbox.Provider)
	fmt.Fprintf(f, "\n[storage]\ndatabase = %q\nfiles_dir = %q\n",
		cfg.Storage.Database, cfg.Storage.FilesDir)
	fmt.Fprintf(f, "\n[search]\nprovider = %q\n", cfg.Search.Provider)
	if cfg.Search.APIKey != "" {
		fmt.Fprintf(f, "api_key = %q\n", cfg.Search.APIKey)
	}

	for _, a := range agents {
		fmt.Fprintf(f, "\n[[agents]]\nid = %q\nmodel = %q\nworkspace = %q\n",
			a.ID, a.Model, a.Workspace)
		if a.Provider != "" {
			fmt.Fprintf(f, "provider = %q\n", a.Provider)
		}
		if a.APIKey != "" {
			fmt.Fprintf(f, "api_key = %q\n", a.APIKey)
		}
		if a.BaseURL != "" {
			fmt.Fprintf(f, "base_url = %q\n", a.BaseURL)
		}
		if len(a.Tools) > 0 {
			fmt.Fprintf(f, "tools = [")
			for i, t := range a.Tools {
				if i > 0 {
					fmt.Fprint(f, ", ")
				}
				fmt.Fprintf(f, "%q", t)
			}
			fmt.Fprint(f, "]\n")
		}
		if a.Heartbeat != nil && a.Heartbeat.Enabled {
			fmt.Fprintf(f, "\n[agents.heartbeat]\nenabled = true\n")
			if a.Heartbeat.Interval != "" {
				fmt.Fprintf(f, "interval = %q\n", a.Heartbeat.Interval)
			}
			if a.Heartbeat.QuietHours != "" {
				fmt.Fprintf(f, "quiet_hours = %q\n", a.Heartbeat.QuietHours)
			}
			if a.Heartbeat.Skill != "" {
				fmt.Fprintf(f, "skill = %q\n", a.Heartbeat.Skill)
			}
		}
	}

	return nil
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func appendToken(path, token string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	// Insert token after [gateway] section header
	marker := "[gateway]\n"
	idx := strings.Index(content, marker)
	if idx >= 0 {
		insert := idx + len(marker)
		content = content[:insert] + fmt.Sprintf("token = %q\n", token) + content[insert:]
	} else {
		// No [gateway] section found — append one
		content = content + fmt.Sprintf("\n[gateway]\ntoken = %q\n", token)
	}
	return os.WriteFile(path, []byte(content), 0600)
}
