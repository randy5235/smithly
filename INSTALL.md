# Smithly – Installation Guide

> **Smithly** is an autonomous‑agent controller written in Go. It runs LLM loops, supports code‑skill authoring, sandboxed execution, and network gatekeeping. The following steps walk you through getting a working local installation.

---

## 1. Prerequisites

| Component | Minimum version | Why it’s needed |
|-----------|----------------|-----------------|
| **Go** | 1.25.5 (or newer) | To compile the source (`go build`). |
| **Docker** | Engine ≥ 20.10 (optional) | Required for the Docker sandbox provider (default). If Docker is not available, Smithly will fall back to the “none” sandbox (unsandboxed subprocesses). |
| **Git** | any | To clone the repository. |
| **Brave Search API key** (optional) | – | Enables the built‑in `search` tool. If omitted, Smithly falls back to DuckDuckGo. |
| **Ollama** (optional) | – | Allows you to run local LLMs (e.g., `ollama run llama3.2`). |
| **KVM** (optional) | – | Needed only for the optional Fly sandbox provider. |
| **(Linux/macOS) `xdg-open`, `open`, or `wslview`** | – | Used by the OAuth2 flow to open a browser. |

> **Tip:** Run `smithly doctor` after installation to verify that all required dependencies are available.

---

## 2. Clone the Repository

```bash
git clone https://github.com/smithly/smithly.git
cd smithly
```

The repository is a standard Go module (`go.mod` declares `module smithly.dev`).

---

## 3. Build the Binary

```bash
# Build and place the binary in ./bin
go build -o ./bin/smithly ./cmd/smithly/main.go
```

You can also install it to `$GOPATH/bin`:

```bash
go install ./cmd/smithly
```

Make sure the binary is on your `$PATH` (e.g., `export PATH=$PATH:$(pwd)/bin`).

---

## 4. First‑Run Setup (`smithly init`)

```bash
smithly init
```

The wizard asks three questions:

1. **Agent name** – defaults to `assistant`.
2. **LLM provider** – choose OpenAI, Anthropic, OpenRouter, or Ollama.
3. **API key** – required for cloud providers; not needed for Ollama.

It also prompts for a **Brave Search API key** (you can skip this). The command creates:

- `smithly.toml` – the global configuration file.
- A workspace directory under `workspaces/<agent‑name>/` containing `SOUL.md` and `IDENTITY.toml`.

---

## 5. Verify the Environment

```bash
smithly doctor
```

You should see green `[ok]` lines for:

- Presence of `smithly.toml`.
- Detected sandbox provider (`docker` or `none`).
- Docker (if installed).
- Ollama (if installed).
- KVM (if present).

Any missing components will be reported with `[--]` and a short hint on how to install them.

---

## 6. Run the Gateway & Agents

```bash
smithly start
```

This command:

1. Starts the **sidecar** HTTP server (default `http://127.0.0.1:18791`).
2. Starts the **gatekeeper** proxy (default `http://127.0.0.1:18792`).
3. Starts all configured agents (the one you created with `init`).
4. Prints the URLs and the bearer token needed to call the API.

Leave this process running (it handles graceful shutdown on `Ctrl‑C`).

---

## 7. Interact via the CLI Chat

In a separate terminal:

```bash
smithly chat            # talks to the first agent
# or specify an explicit agent ID:
smithly chat assistant
```

You’ll get a REPL‑style chat interface. The agent can invoke tools (search, fetch, bash, etc.) after you approve each call.

---

## 8. Managing Agents, Skills & OAuth2

| Command | Description |
|---------|-------------|
| `smithly agent list` | Show all configured agents. |
| `smithly agent add` | Interactive wizard to add another agent. |
| `smithly agent remove <id>` | Delete an agent from the config. |
| `smithly skill list [--agent <id>]` | List installed instruction skills. |
| `smithly skill add <path> [--agent <id>]` | Install a skill from a local directory. |
| `smithly skill remove <name> [--agent <id>]` | Uninstall a skill. |
| `smithly oauth2 auth <provider>` | Run the OAuth2 flow for a configured provider (opens a browser). |
| `smithly oauth2 list` | Show configured OAuth2 providers and their status. |
| `smithly domain allow|deny <domain>` | Manage the network allowlist used by the gatekeeper. |
| `smithly audit` | View recent audit‑log entries (tool calls, agent actions, etc.). |

---

## 9. Running Tests

```bash
go test ./...
```

All unit tests are pure Go (no API keys needed). For LLM integration tests with real API keys, supported models, and pricing details, see [TESTING.md](TESTING.md).

---

## 10. Advanced Configuration (Optional)

The `smithly.toml` file supports many sections. Below are the most common knobs:

```toml
[agents]
# Add additional agents here (ID, model, provider, etc.)

[sandbox]
provider = "docker"   # or "none", "fly"

[gatekeeper]
bind = "127.0.0.1"
port = 18792

[sidecar]
bind = "127.0.0.1"
port = 18791

[search]
provider = "brave"    # "duckduckgo" is the fallback
api_key = "<BRAVE_API_KEY>"

[notify]
ntfy_topic = "smithly"
ntfy_server = "https://ntfy.sh"

[[oauth2]]
name = "google"
client_id = "<ID>"
client_secret = "<SECRET>"
scopes = ["https://www.googleapis.com/auth/gmail.readonly"]
auth_url = "https://accounts.google.com/o/oauth2/auth"
token_url = "https://oauth2.googleapis.com/token"

[[secret]]
name = "my-secret"
value = "s3cr3t"          # or `env = "ENV_VAR_NAME"` to read from the controller’s env
```

Edit the file manually or use `smithly init` again (it will not overwrite existing values).

---

## 11. Updating Smithly

```bash
git pull origin main
go install ./cmd/smithly   # rebuild the binary
```

If you have made local changes, consider creating a separate Git worktree (`smithly worktree …`) – but only if you explicitly request it.

---

## 12. Troubleshooting

| Symptom | Quick Fix |
|---------|-----------|
| `smithly start` fails with “cannot connect to Docker” | Install Docker, start the daemon, or set `sandbox.provider = "none"` in `smithly.toml`. |
| Search tool returns no results | Ensure `BRAVE_API_KEY` is set (or add it to `smithly.toml` under `[search]`). |
| OAuth2 flow never returns a code | Verify that port 18790 is not blocked by a firewall; the callback server binds to `localhost:18790`. |
| Agent cannot load a skill | The skill directory must contain a valid `manifest.toml`. Use `smithly skill add <path>` to copy it into the workspace. |
| “permission denied” when writing files | The repository directory must be writable by the current user (check `chmod`/ownership). |

---

## 13. License

Smithly is released under the **MIT License** (see `LICENSE`).

---

### TL;DR Quick Install (macOS / Linux)

```bash
# 1️⃣ Install Go & Docker (if you want sandboxing)
brew install go docker   # macOS (or apt/yum on Linux)

# 2️⃣ Clone & build
git clone https://github.com/smithly/smithly.git
cd smithly
go install ./cmd/smithly

# 3️⃣ Initialise
smithly init   # answer the three prompts

# 4️⃣ Verify
smithly doctor

# 5️⃣ Start the system (in one terminal)
smithly start

# 6️⃣ Chat with the agent (in another terminal)
smithly chat
```

You’re now ready to explore autonomous agents, author instruction skills, and run code‑skills safely with Smithly!