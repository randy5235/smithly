package agent

import (
	"fmt"
	"strings"

	"smithly.dev/internal/config"
)

// Services describes data stores, sidecar endpoints, and secrets
// available to code skills. Its SystemPromptSection injects this
// information into the LLM's system prompt so it can write correct
// skill code.
type Services struct {
	DataStores  []config.DataStoreConfig
	SidecarURL  string
	SecretNames []string
}

// SystemPromptSection returns a markdown section describing available
// services, or "" if nothing is configured.
func (s *Services) SystemPromptSection() string {
	if s == nil {
		return ""
	}

	var sections []string

	if section := s.dataStoreSection(); section != "" {
		sections = append(sections, section)
	}
	if section := s.sidecarSection(); section != "" {
		sections = append(sections, section)
	}
	if section := s.secretsSection(); section != "" {
		sections = append(sections, section)
	}

	if len(sections) == 0 {
		return ""
	}

	return "## Available Services\n\n" + strings.Join(sections, "\n\n")
}

func (s *Services) dataStoreSection() string {
	if len(s.DataStores) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("### Data Stores\n\n")
	b.WriteString("Code skills receive database connections via environment variables.\n\n")

	for i, ds := range s.DataStores {
		prefix := "SMITHLY_" + strings.ToUpper(ds.Type)
		switch ds.Type {
		case "sqlite":
			fmt.Fprintf(&b, "- **%s** (sqlite): `%s_PATH`\n", ds.Type, prefix)
		default:
			fmt.Fprintf(&b, "- **%s**: `%s_URL`\n", ds.Type, prefix)
		}
		if i == 0 {
			b.WriteString("  - `SMITHLY_DB_TYPE` is set to `" + ds.Type + "`\n")
		}
	}

	return b.String()
}

func (s *Services) sidecarSection() string {
	if s.SidecarURL == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("### Sidecar API\n\n")
	fmt.Fprintf(&b, "Code skills connect to the sidecar at `%s`.\n", s.SidecarURL)
	b.WriteString("Authenticate with `Authorization: Bearer $SMITHLY_TOKEN`.\n\n")
	b.WriteString("Endpoints:\n")
	b.WriteString("- `GET /health` — health check\n")
	b.WriteString("- `GET /oauth2/{provider}` — get OAuth2 access token\n")
	b.WriteString("- `POST /notify` — send notification (`{\"title\", \"message\", \"priority\"}`)\n")
	b.WriteString("- `POST /audit` — log audit entry (`{\"action\", \"target\", \"details\"}`)\n")
	b.WriteString("- `GET /secrets/{name}` — read a secret value\n")
	b.WriteString("- `POST /store/put` — store object (`{\"id\", \"type\", \"data\", \"public\"}`)\n")
	b.WriteString("- `POST /store/get` — get object (`{\"id\"}`)\n")
	b.WriteString("- `POST /store/delete` — delete object (`{\"id\"}`)\n")
	b.WriteString("- `POST /store/query` — query objects (`{\"type\"}`)\n")
	b.WriteString("- `POST /store/history` — object history (`{\"id\"}`)\n")

	return b.String()
}

func (s *Services) secretsSection() string {
	if len(s.SecretNames) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("### Secrets\n\n")
	b.WriteString("Available via `GET /secrets/{name}` on the sidecar API:\n\n")
	for _, name := range s.SecretNames {
		fmt.Fprintf(&b, "- `%s`\n", name)
	}

	return b.String()
}
