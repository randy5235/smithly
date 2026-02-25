# Smithly — Design & Architecture Plan

> A secure, lightweight AI agent runtime built on Go + SQLite.
> "Secure by default, useful by choice."
>
> **Brand:** Smithly — your digital smith, forging and running tasks on your behalf.
> **CLI:** `smithly`
> **Domain:** smithly.dev (TBD)

---

## 1. What We're Fixing

OpenClaw proved persistent AI agents are genuinely useful. But it has fundamental problems:

| Problem | Our Fix |
|---|---|
| Skills run arbitrary code with full system access | Skills must be **cryptographically signed** by author + **auto-scanned** before install |
| ClawHub marketplace is 20% malware | No marketplace. Git clone → scan → sign → approve. Chain of trust. |
| Prompt injection via untrusted content | Content firewall with trust tagging + human-in-the-loop for untrusted actions |
| 30,000+ instances exposed with no auth | Localhost-only, mandatory auth, cannot be disabled |
| Memory can be poisoned by attackers | Identity memory is read-only. All memory trust-tagged + append-only audit log |
| Browser sessions give access to all your accounts | Isolated browser in Docker container, no cookies, destroyed after each task |
| Skills silently exfiltrate data to unknown servers | **Runtime domain allowlist** — first access to ANY new domain requires user approval |
| Skills are stateless input→output only | **Persistent skill storage** — database tables + files, with access control |
| Node.js runtime is heavy, not cross-compilable | Go — single static binary, 5-15MB RSS, cross-compiles to any OS/arch |
| Single agent only | **Multi-agent** — run a team of agents with isolated memory, permissions, and channel bindings |
| No proactive behavior | **Heartbeat/cron** — agents wake on a schedule to check conditions and run tasks |
| Skills require code + complex setup | **Two skill types** — instruction skills (Markdown, zero friction) and code skills (signed, sandboxed) |
| Flat identity model | **Soul + Identity** — layered personality: soul (behavior), identity (appearance), user profile, per agent |

---

## 2. Design Principles

1. **Deny by default.** Nothing works until explicitly enabled.
2. **Every action is auditable.** Append-only audit log in SQLite.
3. **Trust is first-class.** Every piece of data is tagged: trusted / semi-trusted / untrusted.
4. **Skills are signed code.** Author signs with their key, we verify + scan before loading.
5. **Network is allowlisted at runtime.** First contact with any domain → ask user.
6. **Single binary, single config, single database.** Go + TOML + SQLite.
7. **Localhost is sacred.** Gateway never binds 0.0.0.0.
8. **Small controller, remote workers.** The control plane runs on a tiny machine. Skill execution can be local (Docker) or remote (Fly Machines / Sprites).
9. **Skills own their data.** Persistent storage with explicit access control — private by default, public read opt-in, never public write.
10. **Agents have souls.** Each agent has a distinct personality, values, and behavioral identity — not just configuration.

---

## 3. Tech Stack

| Layer | Choice | Why |
|---|---|---|
| Runtime | **Go** | Single static binary, 5-15MB memory, cross-compiles to any OS/arch, goroutines for concurrency |
| Database | **SQLite** (local) / **Postgres** (remote) | One file locally, managed DB for remote providers. Storage interface abstracts both. |
| Config | **TOML** | Single file, human-readable |
| LLM | **OpenAI-compatible API** | Works with Anthropic, OpenAI, OpenRouter, Ollama |
| Channels | **Modular adapters** | Telegram, Discord, Slack, Web UI, CLI |
| Sandboxing | **Docker** (default) | Runs everywhere, ephemeral containers, scoped mounts. Abstraction supports `none` / `docker` / `fly` (remote VMs) |
| Skill Files | **Local filesystem** (local) / **R2** (remote) | Provider-dependent — local for docker/none, R2 for remote/hosted |
| Skill Registry | **Cloudflare Workers + D1 + R2** | Zero egress, SQLite-at-edge, dirt cheap at scale |
| Vector search | **sqlite-vec** | KNN search via SQL |
| Embeddings | **Ollama (local)** or LLM provider | Keep memory private by default, cloud optional |

### Key Go Dependencies (minimal)

| Need | Library | Why |
|---|---|---|
| SQLite | `modernc.org/sqlite` | Pure Go, no CGo, cross-compiles cleanly |
| TOML | `github.com/BurntSushi/toml` | Standard, minimal |
| HTTP | `net/http` (stdlib) | No framework needed |
| WebSocket | `nhooyr.io/websocket` | Lightweight, stdlib-compatible |
| Ed25519 | `crypto/ed25519` (stdlib) | Built-in |
| CLI | `flag` (stdlib) or `cobra` | TBD based on complexity |
| Vector search | `sqlite-vec` via CGo or pure-Go KNN | Evaluate at implementation time |

---

## 4. Architecture

```
┌──────────────────────────────────────────────────┐
│                    User                          │
│  Telegram / Discord / Slack / Web UI / CLI       │
└────────────────────┬─────────────────────────────┘
                     │
          ┌──────────▼──────────┐
          │  Content Firewall   │ ← Tags trust level, flags injection patterns
          └──────────┬──────────┘
                     │
          ┌──────────▼──────────┐
          │     Gateway         │ ← 127.0.0.1 only, bearer token auth
          │   (Go net/http)     │
          └──────────┬──────────┘
                     │
          ┌──────────▼──────────┐
          │    Agent Loop       │ ← LLM reasoning + tool selection
          └──┬──────────┬───────┘
             │          │
  ┌──────────▼──┐  ┌───▼──────────────┐
  │   Memory    │  │   Skill Runner   │
  │  (SQLite)   │  │                  │
  │             │  │  ┌────────────┐  │
  │ • identity  │  │  │ Signature  │  │ ← Verify author signature
  │   (r/o)     │  │  │ Verify     │  │
  │ • convos    │  │  └─────┬──────┘  │
  │   (tagged)  │  │  ┌─────▼──────┐  │
  │ • vectors   │  │  │ Auto Scan  │  │ ← AST analysis for dangers
  │   (sqlite-  │  │  └─────┬──────┘  │
  │    vec)     │  │  ┌─────▼──────┐  │
  │ • audit log │  │  │  Sandbox   │  │ ← none / docker / fly
  │   (append)  │  │  └──┬─────┬──┘  │
  └─────────────┘  └─────┼─────┼─────┘
                         │     │
              ┌──────────▼┐   ┌▼───────────────┐
              │  Storage   │   │ Network        │
              │  Provider  │   │ Gatekeeper     │
              │            │   │                │
              │ SQLite +   │   │ Known? → OK    │
              │ local files│   │ New?   → ASK   │
              │  — or —    │   │ Denied → DROP  │
              │ Postgres + │   └────────────────┘
              │ S3         │
              └────────────┘
```

---

## 5. Multi-Agent Architecture

A single Smithly gateway runs multiple isolated agents. Each agent is a separate "brain" with its own personality, memory, permissions, and skill bindings.

### 5.1 Agent Isolation

Each agent gets:
- Its own **workspace directory** containing soul, identity, and memory files
- Its own **memory partition** in SQLite (conversations, vectors scoped per agent)
- Its own **skill bindings** (which skills this agent can use)
- Its own **permission set** (which tools, domains, channels it can access)
- Its own **LLM model** (one agent can use Claude, another Ollama)

Agents do NOT share: conversation history, soul, active sessions.
Agents CAN share: skill storage marked public, trusted author keys, domain allowlist, audit log.

### 5.2 Channel Bindings

The gateway routes incoming messages to agents via bindings — rules that match channels, servers, or contacts to agent IDs. Most specific match wins.

```toml
[[agents]]
id = "assistant"
model = "claude-sonnet-4-5"
workspace = "workspaces/assistant/"

[[agents]]
id = "codebot"
model = "claude-opus-4-5"
workspace = "workspaces/codebot/"
tools = { deny = ["browser"] }

[[agents]]
id = "home"
model = "ollama/llama3"
workspace = "workspaces/home/"
tools = { allow = ["smart-home", "weather"] }

# Bindings: route channels → agents
[[bindings]]
channel = "telegram"
contact = "@wife"
agent = "home"

[[bindings]]
channel = "discord"
server = "my-dev-server"
agent = "codebot"

[[bindings]]
channel = "*"          # default catch-all
agent = "assistant"
```

### 5.3 Dynamic Agent Spawning

An agent can spawn a sub-agent mid-conversation for parallel work. The sub-agent:
- Inherits the parent's permissions (or a subset)
- Gets a temporary workspace
- Returns its result to the parent
- Is destroyed after completion

This is how an agent delegates: "research these three things in parallel" → spawns three sub-agents → collects results.

---

## 6. Soul, Identity & Memory

OpenClaw uses flat Markdown files for personality. We keep the Markdown authoring experience (it's natural for writing personality) but back it with structured storage for search, trust tagging, and multi-agent isolation.

### 6.1 The Three Layers

| Layer | What it defines | Who writes it | Loaded when |
|---|---|---|---|
| **Soul** | Behavioral philosophy — values, temperament, boundaries, how the agent thinks | User (agent can suggest changes) | Every session start, injected into system prompt |
| **Identity** | External presentation — name, avatar, emoji, vibe, greeting style | User | Session start, used by channel adapters |
| **User Profile** | Info about the user — name, preferences, context, timezone | User | Session start, injected into system prompt |

### 6.2 Workspace Structure

Each agent has a workspace directory:

```
workspaces/assistant/
├── SOUL.md              ← Who the agent IS (values, behavior, boundaries)
├── IDENTITY.toml        ← How the agent APPEARS (name, avatar, emoji)
├── USER.md              ← Who the user IS (preferences, context)
├── INSTRUCTIONS.md      ← Operating instructions (priorities, workflow, quality bar)
├── HEARTBEAT.md         ← Recurring task checklist (cron)
├── BOOT.md              ← Startup checklist (what to do on wake)
└── memory/
    └── (managed by the system — not hand-edited)
```

### 6.3 SOUL.md

The soul defines the agent's internal behavioral identity. This is NOT instructions — it's personality.

```markdown
# Soul

You are a careful, direct thinker. You value precision over speed.

## Values
- Honesty over comfort. If something is wrong, say so.
- Brevity. Don't pad responses with filler.
- Curiosity. Ask clarifying questions rather than guessing.

## Boundaries
- Never pretend to have emotions you don't have.
- Never make up facts. Say "I don't know" when you don't.
- Never take destructive actions without explicit confirmation.

## Voice
- Direct, not terse. Warm, not effusive.
- Use technical language when precision matters, plain language otherwise.
```

The agent reads SOUL.md at the start of every session. It's injected into the system prompt before any conversation. The user authors it, but the agent can propose changes to its own soul ("I've noticed I work better when I...") — the user approves or rejects.

### 6.4 IDENTITY.toml

```toml
name = "Atlas"
emoji = "🔨"
creature = "digital smith"
vibe = "calm, precise, quietly helpful"
greeting = "What are we building?"
```

Channel adapters use this: the name becomes a message prefix, the emoji becomes the acknowledgment reaction, the greeting is sent on first contact.

### 6.5 USER.md

```markdown
# User

- Name: JT
- Timezone: America/Los_Angeles
- Prefers direct language, no fluff
- Working on Smithly — an AI agent runtime
- Uses Neovim, Go, TypeScript
```

### 6.6 INSTRUCTIONS.md

Operating instructions — the stable rules for how this agent works. Not personality (that's SOUL.md), not tasks (those come via messages).

```markdown
# Instructions

## Priorities
1. Security first. Never bypass safety checks.
2. Ask before acting on anything destructive.
3. Keep responses concise unless detail is requested.

## Workflow
- When given a coding task, read existing code before proposing changes.
- Run tests after making changes.
- Commit messages follow: TYPE(scope): description
```

### 6.7 How It All Loads

On session start, the system prompt is assembled from the workspace files:

```
System Prompt = SOUL.md + INSTRUCTIONS.md + USER.md + [active instruction skills]
```

Total capped at a configurable token limit (default 20,000 chars). If it exceeds the limit, INSTRUCTIONS.md is summarized first, then instruction skills are pruned by relevance.

### 6.8 Memory (Structured, Not Flat Files)

Unlike OpenClaw's flat Markdown memory files, Smithly stores memory in SQLite with trust tagging and vector search. But the experience is similar:

- **Short-term**: current conversation context (in-session, not persisted until explicitly saved)
- **Long-term**: facts, decisions, preferences stored in the `memory` table with trust tags + embeddings
- **Semantic search**: hybrid vector (sqlite-vec) + keyword (FTS5) search, weighted by trust level

The agent can save memories during conversation:
```
Agent: "Should I remember that you prefer dark mode for all UIs?"
User: "Yes"
→ INSERT INTO memory (content, source, trust) VALUES ('User prefers dark mode for all UIs', 'user', 'trusted')
→ Embedding generated and stored in memory_vec
```

Memory is per-agent (each agent has its own partition). An agent doesn't see another agent's conversation memory.

---

## 7. Heartbeat & Cron

Agents can wake on a schedule to do proactive work without being prompted.

### 7.1 HEARTBEAT.md

Each agent's workspace can include a heartbeat checklist:

```markdown
# Heartbeat

Run every 30 minutes.

## Tasks
- Check email inbox for anything urgent. Summarize new messages.
- Check GitHub notifications. Flag any PR reviews needed.
- If it's 9am, send the daily briefing to Telegram.
```

### 7.2 Configuration

```toml
[[agents]]
id = "assistant"
# ...

[agents.heartbeat]
interval = "30m"        # how often to wake
enabled = true
quiet_hours = "23:00-07:00"   # don't wake during these hours
```

### 7.3 How It Works

1. The controller runs a scheduler (goroutine per agent with heartbeat enabled)
2. On tick: loads the agent's HEARTBEAT.md into context
3. Agent processes the checklist, takes actions within its permissions
4. Results are logged to audit_log
5. If the agent needs user input, it sends a message via the agent's bound channels

The heartbeat runs with the same permissions as the agent — no escalation. If a heartbeat task requires an unapproved domain, it queues the approval request and skips that task until approved.

---

## 8. Two Skill Types

### 8.1 Instruction Skills (Markdown — no code)

The simplest way to extend an agent. An instruction skill is a Markdown file that gets injected into the LLM's context when relevant. No code execution, no sandbox, no signing needed.

```
skills/
├── email-triage/
│   ├── manifest.toml
│   └── INSTRUCTIONS.md     ← Just text. Loaded into LLM context.
├── code-review/
│   ├── manifest.toml
│   └── INSTRUCTIONS.md
```

Example instruction skill (`email-triage/INSTRUCTIONS.md`):
```markdown
# Email Triage

When processing emails:
1. Categorize as: urgent, action-needed, informational, spam
2. For urgent: summarize in one sentence and notify immediately
3. For action-needed: add to task list with deadline
4. For informational: file in daily digest
5. For spam: archive silently

Never auto-reply without user confirmation.
```

Manifest for instruction skills:
```toml
[skill]
name = "email-triage"
type = "instruction"       # vs "code"
version = "1.0.0"
description = "Email categorization and triage rules"
triggers = ["email", "inbox", "message triage"]   # when to load into context

# Instruction skills can declare dependencies on code skills and tools
[skill.requires]
code_skills = ["email-reader"]        # code skills that must be installed
tools = ["search", "browser"]         # built-in tools this skill expects
domains = ["imap.gmail.com"]          # domains that need to be approved for this to work
```

The `triggers` field tells the agent when this skill is relevant. When the agent sees a message about email, it loads the instruction skill into context. This is how OpenClaw's 3000+ skills work — most are just prompt engineering.

The `requires` section declares what the instruction skill needs to actually be useful. On install:
- Missing code skills → prompt to install them
- Missing tool access → warn the user
- Unapproved domains → prompt to approve them

**No signing needed** — there's no code to execute. But instruction skills **are still scanned** for prompt injection patterns (instruction overrides, role injection, authority claims, encoded payloads). The content firewall's injection detection runs against the Markdown at install time.

Instruction skills are also **tied to an author identity** (same as code skills). If an author publishes instruction skills that repeatedly get flagged for injection patterns, the registry can warn users or block the author from publishing. This prevents a low-effort attack vector where someone floods the registry with poisoned instruction skills.

### 8.2 Code Skills (Signed, Sandboxed)

For skills that need to execute code, access APIs, or do computation. These go through the full trust chain: sign → scan → approve → sandbox.

*(Same as sections 7 and 8 in this plan — storage, signing, scanning, sandbox execution.)*

### 8.3 Summary

| | Instruction Skills | Code Skills |
|---|---|---|
| Format | Markdown | Code (any language) |
| Execution | Injected into LLM context | Runs in sandbox |
| Author identity | Yes — tied to author account | Yes — Ed25519 signed |
| Signing required | No | Yes |
| Scanned for injection | Yes — content firewall patterns | Yes — AST + content analysis |
| Sandbox required | No | Yes (docker/fly/none) |
| Can access network | No (LLM-mediated only) | Yes (through gatekeeper) |
| Can have storage | No | Yes (tables + files) |
| Friction to create | Low — write Markdown, publish | Higher — write code, sign, approve |
| Risk | Medium (prompt injection) | Higher (code execution) |

---

## 9. Project Structure

```
smithly/
├── cmd/
│   └── smithly/
│       └── main.go              ← Entry point, CLI
├── internal/
│   ├── gateway/
│   │   └── gateway.go           ← HTTP/WS server, 127.0.0.1 only, bearer auth
│   ├── agent/
│   │   ├── agent.go             ← LLM agent loop, tool selection
│   │   ├── spawner.go           ← Dynamic sub-agent spawning
│   │   └── scheduler.go        ← Heartbeat/cron scheduler
│   ├── workspace/
│   │   └── workspace.go         ← Load soul, identity, user, instructions from workspace dir
│   ├── memory/
│   │   └── memory.go            ← Per-agent memory, vector search, audit
│   ├── config/
│   │   └── config.go            ← TOML config loader
│   ├── permissions/
│   │   └── permissions.go       ← Capability engine
│   ├── firewall/
│   │   └── firewall.go          ← Content trust tagger + injection detection
│   ├── sandbox/
│   │   ├── provider.go          ← SandboxProvider interface
│   │   ├── none.go              ← Raw shell provider
│   │   ├── docker.go            ← Docker provider (default)
│   │   └── fly.go               ← Remote VM provider (Fly Machines / Sprites)
│   ├── storage/
│   │   ├── provider.go          ← StorageProvider interface (DB + files)
│   │   ├── local.go             ← SQLite + local filesystem
│   │   └── remote.go            ← Postgres + R2 (future, for remote providers)
│   ├── skills/
│   │   ├── loader.go            ← Discovery + manifest validation
│   │   ├── instruction.go       ← Instruction skill loader (Markdown → context)
│   │   ├── signer.go            ← Ed25519 signing + verification
│   │   ├── scanner.go           ← Static analysis
│   │   └── runner.go            ← Invoke code skill via sandbox provider
│   ├── network/
│   │   ├── gatekeeper.go        ← Domain allowlist proxy
│   │   └── parser.go            ← Extract domains from curl/wget commands
│   ├── webhooks/
│   │   └── webhooks.go          ← Inbound webhook handler, HMAC verification
│   └── channels/
│       ├── channel.go           ← Channel interface
│       ├── binding.go           ← Route channels → agents
│       ├── telegram.go
│       ├── discord.go
│       ├── slack.go
│       └── webchat.go
├── workspaces/                  ← Per-agent workspace directories
│   └── default/
│       ├── SOUL.md
│       ├── IDENTITY.toml
│       ├── USER.md
│       ├── INSTRUCTIONS.md
│       ├── HEARTBEAT.md
│       └── BOOT.md
├── skills/                      ← Local skill directory
├── smithly.toml                 ← Single config file
├── go.mod
└── go.sum
```

```
smithly/
├── cmd/
│   └── smithly/
│       └── main.go              ← Entry point, CLI
├── internal/
│   ├── gateway/
│   │   └── gateway.go           ← HTTP/WS server, 127.0.0.1 only, bearer auth
│   ├── agent/
│   │   └── agent.go             ← LLM agent loop, tool selection
│   ├── memory/
│   │   └── memory.go            ← Identity, conversations, vector search, audit
│   ├── config/
│   │   └── config.go            ← TOML config loader
│   ├── permissions/
│   │   └── permissions.go       ← Capability engine
│   ├── firewall/
│   │   └── firewall.go          ← Content trust tagger + injection detection
│   ├── sandbox/
│   │   ├── provider.go          ← SandboxProvider interface
│   │   ├── none.go              ← Raw shell provider
│   │   ├── docker.go            ← Docker provider (default)
│   │   └── fly.go               ← Remote VM provider (Fly Machines / Sprites)
│   ├── storage/
│   │   ├── provider.go          ← StorageProvider interface (DB + files)
│   │   ├── local.go             ← SQLite + local filesystem
│   │   └── remote.go            ← Postgres + R2 (future, for remote providers)
│   ├── skills/
│   │   ├── loader.go            ← Discovery + manifest validation
│   │   ├── signer.go            ← Ed25519 signing + verification
│   │   ├── scanner.go           ← Static analysis
│   │   └── runner.go            ← Invoke skill via sandbox provider
│   ├── network/
│   │   ├── gatekeeper.go        ← Domain allowlist proxy
│   │   └── parser.go            ← Extract domains from curl/wget commands
│   └── channels/
│       ├── channel.go           ← Channel interface
│       ├── telegram.go
│       ├── discord.go
│       ├── slack.go
│       └── webchat.go
├── smithly.toml                 ← Single config file
├── go.mod
└── go.sum
```

---

## 6. SandboxProvider Interface

The interface supports both local and remote execution. Skills don't know or care which provider they're running in.

```go
type SandboxProvider interface {
    Name() string
    Available() (bool, error)

    Run(ctx context.Context, opts RunOpts) (*RunResult, error)
    Destroy(ctx context.Context, id string) error
}

type RunOpts struct {
    Skill        SkillManifest
    Input        []byte                // JSON
    Env          map[string]string     // Only declared env vars
    Timeout      time.Duration
    NetworkProxy string                // Gatekeeper proxy URL
    Storage      SkillStorage          // Storage handle for this skill
}

type RunResult struct {
    Output   []byte   // JSON
    ExitCode int
    Logs     string
}
```

### Providers

| Provider | Execution | Storage Backend | Network Control |
|---|---|---|---|
| `none` | Local shell | SQLite + local files | Go-level proxy only |
| `docker` | Local container | SQLite + local files (mounted) | Docker network → gatekeeper |
| `fly` | Remote Fly Machine / Sprite | Postgres + R2 | VM firewall rules + proxy |

### How Each Provider Works

**Docker (default):** Each skill invocation spins up an ephemeral container (`--rm`), mounts skill code read-only and workspace read-write, routes all traffic through the gatekeeper proxy, and destroys the container after completion.

**Fly (remote):** Creates an ephemeral Fly Machine or Sprite, ships skill code + input + storage credentials, collects the result, and destroys the machine. Storage calls go to Postgres/R2 directly from the VM (authenticated with a short-lived token scoped to that skill invocation).

**None (dangerous):** Runs skills directly in the user's shell. Domain gatekeeper still works at the Go-level proxy, but there's no filesystem or process isolation. Requires explicit opt-in with a scary warning.

### Fallback Chain

```
smithly doctor
  → Is Docker available?     → Use docker provider
  → No Docker?               → Warn user, offer "none" provider
  → Fly API configured?      → Note: remote execution available
```

---

## 7. Skill Storage

### 7.1 Two Storage Types

**Database tables** — structured data:
- Each skill gets a namespaced set of tables
- Skills define their schema in `manifest.toml`
- Controller creates/migrates tables on skill install

**Files/folders** — a persistent directory per skill:
- Each skill gets a directory: `data/skills/{skill-name}/`
- Skill can create subdirectories, read/write files freely within its space
- Files persist across invocations

### 7.2 Access Model

| | Read | Write |
|---|---|---|
| **Private** (default) | Owner skill only | Owner skill only |
| **Public read** | Any skill | Owner skill only |

- Skills declare visibility per-table and per-file-path in `manifest.toml`
- **Never public write** — a skill always owns its data exclusively
- Other skills reference shared data as `{skill-name}:{table}` or `{skill-name}:{path}`
- All storage operations are logged in the audit table

### 7.3 Manifest Declaration

```toml
[skill]
name = "weather-cache"
version = "1.0.0"

[storage.tables]
  [storage.tables.forecasts]
  access = "public"   # other skills can read this table
  columns = [
    { name = "location", type = "text", primary = true },
    { name = "data", type = "json" },
    { name = "fetched_at", type = "datetime" },
  ]

  [storage.tables.api_usage]
  access = "private"   # only this skill can see its usage tracking

[storage.files]
  [storage.files.cache]
  path = "cache/"
  access = "private"

  [storage.files.exports]
  path = "exports/"
  access = "public"   # other skills can read exported files
```

### 7.4 Skill Storage API

Skills interact with storage through a JSON-over-stdin/stdout protocol. The sandbox provider handles the translation to the actual backend.

```jsonc
// Own data:
{"storage": "query", "table": "forecasts", "where": {"location": "Portland"}}
{"storage": "put", "table": "forecasts", "row": {"location": "Portland", "data": {...}}}
{"storage": "read_file", "path": "cache/portland.json"}
{"storage": "write_file", "path": "cache/portland.json", "content": "..."}
{"storage": "list_files", "path": "cache/"}

// Another skill's public data:
{"storage": "query", "skill": "weather-cache", "table": "forecasts", "where": {...}}
{"storage": "read_file", "skill": "weather-cache", "path": "exports/summary.json"}
```

For Docker: the controller runs a small Go sidecar in the container that proxies storage calls back to the host over a Unix socket.

For remote VMs: the skill talks to a storage API endpoint provided as an env var, authenticated with a short-lived token.

### 7.5 Storage Provider Interface

```go
type StorageProvider interface {
    // Table operations
    CreateTable(skill string, table TableDef) error
    Query(skill string, table string, where map[string]any) ([]map[string]any, error)
    Put(skill string, table string, row map[string]any) error
    Delete(skill string, table string, where map[string]any) error

    // File operations
    ReadFile(skill string, path string) (io.Reader, error)
    WriteFile(skill string, path string, r io.Reader) error
    ListFiles(skill string, prefix string) ([]FileInfo, error)
    DeleteFile(skill string, path string) error

    // Cross-skill reads (enforces access control)
    QueryPublic(requestor string, owner string, table string, where map[string]any) ([]map[string]any, error)
    ReadPublicFile(requestor string, owner string, path string) (io.Reader, error)
}
```

Two implementations:
- **`local.go`**: SQLite tables (namespaced as `skill_{name}_{table}`) + files under `data/skills/{name}/`
- **`remote.go`**: Postgres schemas + R2 buckets (for Fly/firecracker providers)

### 7.6 Access Control Enforcement

```
Skill "travel-planner" requests: read weather-cache:forecasts
    │
    ├── Is "forecasts" table marked public in weather-cache manifest?
    │     YES → allow read
    │     NO  → deny, log attempt
    │
    └── Write attempt to another skill's table?
          → ALWAYS deny. Log + audit.
```

---

## 10. Code Skill Signing & Scanning

*(Ed25519 signatures, AST-based scanning, three-step verify→scan→approve flow — applies to code skills only, not instruction skills.)*

### 10.1 How Signing Works

Every skill author generates an Ed25519 keypair (`smithly key generate`). When publishing, the author signs the skill (`smithly skill sign`), creating a SIGNATURE file with author public key, SHA-256 hash of each file, Ed25519 signature, and timestamp.

### 10.2 Verification Flow

On `smithly skill add`:

1. **Signature check** — SIGNATURE present, signature verifies, file hashes match, author key trusted
2. **Automated scan** — AST-parse all code, flag network calls, process spawning, filesystem access, env access, dynamic code, obfuscation
3. **User review** — show scan report, author identity, required capabilities, user approves or rejects

### 10.3 On Skill Update

If any file changes after signing → skill disabled immediately. Must be re-signed and re-approved. Prevents supply chain attacks via compromised repos.

---

## 11. Network Gatekeeper

### 11.1 The Search Tool — Flexible Read Access

Agents need to research freely. Rather than making all GET requests unrestricted (which opens exfiltration via URL params), we solve this with a **built-in search tool** that has broad read access:

| Capability | Domain restriction | Why |
|---|---|---|
| **Search tool** (built-in) | Any domain — read only | Agent needs to look things up, Google, read articles, fetch docs. Runs in the controller, not in a skill sandbox. |
| **Code skills** | Declared domains only (read + write) | Skills declare their domains in the manifest. Anything undeclared gets blocked. |
| **Agent direct actions** | Allowlisted domains only | When the agent itself calls an API (not via a skill), the domain must be approved. |

The search tool is a first-class agent capability — like "memory" or "browser." It can:
- Search via Google, DuckDuckGo, or other configured search APIs
- Fetch and read any URL's content (GET only)
- Return content to the agent for processing

Because the search tool runs inside the controller (not in a sandbox), it's trusted code. It never POSTs data, never submits forms, never sends credentials. It just reads.

### 11.2 Domain Gatekeeper for Everything Else

All other outbound network traffic goes through the gatekeeper:

```
Outbound request (not from search tool)
    │
    ├── Is the domain in the allowlist? → Allow. Log it.
    ├── Is the domain in the denylist?  → Block. Log it.
    └── First time seeing this domain?  → Pause. Ask user.
```

Code skills declare their domains in the manifest:
```toml
[skill.network]
domains = ["api.openweathermap.org", "api.weather.gov"]
```

At install time, these domains are shown to the user. At runtime, undeclared domains are blocked — even for reads. The search tool is the only path for open-ended web access.

### 11.3 Docker Network Isolation (The Key Layer)

The Docker container has no direct internet access. All TCP/DNS routes through the gatekeeper proxy. It doesn't matter how the skill constructs the URL — we intercept at the TCP level.

```
┌─ Docker Container ──────────────────┐
│  skill code → fetch("evil.com")     │
│       │ all traffic routes through  │
└───────┼─────────────────────────────┘
        ▼
┌─ Gatekeeper Proxy ──────────────────┐
│  evil.com not in skill's manifest   │
│  → BLOCKED                          │
└─────────────────────────────────────┘
```

### 11.4 Pre-seeded Lists

Allowlist (based on config):
- LLM provider endpoints (api.anthropic.com, api.openai.com, etc.)
- Ollama localhost
- Search provider APIs

Denylist:
- Known malware C2 domains
- Common exfiltration patterns (random subdomains of free DNS services)

### 11.5 The Allowlist in SQLite

```sql
CREATE TABLE domain_allowlist (
  domain TEXT PRIMARY KEY,
  status TEXT NOT NULL,          -- 'allow', 'deny'
  granted_by TEXT DEFAULT 'user',
  granted_at TEXT DEFAULT (datetime('now')),
  last_accessed TEXT,
  access_count INTEGER DEFAULT 0,
  requested_by TEXT,             -- which skill/agent first requested it
  notes TEXT
);
```

---

## 12. SQLite Schema

```sql
-- Agents
CREATE TABLE agents (
  id TEXT PRIMARY KEY,              -- e.g., "assistant", "codebot"
  model TEXT NOT NULL,
  workspace_path TEXT NOT NULL,
  heartbeat_interval TEXT,          -- e.g., "30m", null if disabled
  heartbeat_enabled INTEGER DEFAULT 0,
  quiet_hours TEXT,                 -- e.g., "23:00-07:00"
  created_at TEXT DEFAULT (datetime('now'))
);

-- Conversation memory with trust tags (per-agent)
CREATE TABLE memory (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  content TEXT NOT NULL,
  source TEXT NOT NULL,
  trust TEXT NOT NULL DEFAULT 'untrusted',
  created_at TEXT DEFAULT (datetime('now')),
  expires_at TEXT,
  deleted INTEGER DEFAULT 0
);

-- Vector embeddings for semantic search (sqlite-vec)
CREATE VIRTUAL TABLE memory_vec USING vec0(
  embedding float[384]
);

-- Full-text search (built-in SQLite)
CREATE VIRTUAL TABLE memory_fts USING fts5(
  content, source,
  content=memory, content_rowid=id
);

-- Channel → agent bindings
CREATE TABLE bindings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,            -- "telegram", "discord", "*"
  server TEXT,                      -- optional: specific server/group
  contact TEXT,                     -- optional: specific user
  agent_id TEXT NOT NULL REFERENCES agents(id),
  priority INTEGER DEFAULT 0       -- higher = more specific = wins
);

-- Domain allowlist
CREATE TABLE domain_allowlist (
  domain TEXT PRIMARY KEY,
  status TEXT NOT NULL,             -- 'allow', 'deny'
  granted_by TEXT DEFAULT 'user',
  granted_at TEXT DEFAULT (datetime('now')),
  last_accessed TEXT,
  access_count INTEGER DEFAULT 0,
  requested_by TEXT,
  notes TEXT
);

-- Skill registry
CREATE TABLE skills (
  name TEXT PRIMARY KEY,
  type TEXT NOT NULL,               -- 'instruction' or 'code'
  version TEXT NOT NULL,
  author_id TEXT NOT NULL,          -- author identity (both types)
  author_pubkey TEXT,               -- Ed25519 key (code skills)
  signature TEXT,                   -- Ed25519 signature (code skills)
  scan_result TEXT,                 -- injection scan (instruction) or AST scan (code)
  scan_date TEXT,
  flagged INTEGER DEFAULT 0,       -- flagged by injection scanner
  approved INTEGER DEFAULT 0,
  approved_at TEXT,
  disabled INTEGER DEFAULT 0,
  path TEXT NOT NULL
);

-- Trusted author keys
CREATE TABLE trusted_authors (
  pubkey TEXT PRIMARY KEY,
  name TEXT,
  trusted_at TEXT DEFAULT (datetime('now')),
  trust_reason TEXT
);

-- Audit log: append-only, never deleted
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp TEXT DEFAULT (datetime('now')),
  actor TEXT NOT NULL,              -- "agent:assistant", "skill:weather", "user"
  action TEXT NOT NULL,
  target TEXT,
  details TEXT,                     -- JSON
  trust_level TEXT NOT NULL,
  approved_by TEXT,
  domain TEXT
);
```

---

## 13. Content Firewall

| Source | Trust Level | Can trigger tools? | Can write memory? |
|---|---|---|---|
| User typing directly | `trusted` | Yes | Yes |
| Approved channel contact | `semi-trusted` | Yes | Yes (tagged) |
| Email body | `untrusted` | Needs approval | Config-dependent |
| Web page content | `untrusted` | Needs approval | Config-dependent |
| Skill output | `skill:{name}` | Via permission engine | Via permission engine |

Injection detection flags (doesn't strip) known patterns: instruction overrides, role injection, authority claims, encoded payloads. Flagged content triggers automatic human approval gate.

---

## 14. Gateway Security

| Setting | Default | Changeable? |
|---|---|---|
| Bind address | `127.0.0.1` | Only with `--i-understand-the-risk` flag |
| Bearer token | Auto-generated, 256-bit | Rotatable via CLI |
| Session TTL | 60 minutes | Yes, in config |
| CSRF protection | On | No |
| Rate limiting | 60 req/min | Yes, in config |
| Remote access | Off | Via `smithly tunnel setup` |

---

## 15a. How Instruction Skills Run

```
1. User message arrives, routed to agent via channel binding
2. Agent loads SOUL.md + INSTRUCTIONS.md + USER.md into system prompt
3. Agent evaluates message against instruction skill triggers
4. Matching instruction skills are loaded into context
5. LLM processes message with full context
6. LLM uses agent's built-in tools (search, memory, etc.) as needed
7. Response sent back via channel
```

No sandbox, no signing, no scanning. The instruction skill is just additional context for the LLM.

## 15b. How Code Skills Run

```
1. Agent decides to invoke code skill "weather-cache"
2. Controller verifies signature + file hashes (every run)
3. Controller creates a SkillStorage handle (scoped to this skill)
4. SandboxProvider.Run() is called with the storage handle
5. Skill code runs in sandbox:
   a. Reads input from stdin (JSON)
   b. Sends storage operations via JSON protocol (intercepted by controller/sidecar)
   c. Network requests checked against skill's declared domains
   d. Writes final output to stdout (JSON)
6. Controller collects output, tears down sandbox
7. Storage writes are committed (or rolled back on failure)
8. All operations logged to audit_log
```

---

## 15. Config (smithly.toml)

```toml
[gateway]
bind = "127.0.0.1"
port = 18789
# token = auto-generated on first run

[sandbox]
provider = "docker"    # "none" | "docker" | "fly"

[storage]
database = "smithly.db"
files_dir = "data/skills/"
# Remote providers (only needed for fly):
# postgres_url = "postgres://..."
# r2_endpoint = "https://..."
# r2_bucket = "smithly-skills"

[search]
provider = "duckduckgo"   # "google" | "duckduckgo" | "searxng"
# api_key = "..."         # for Google Custom Search
# searxng_url = "..."     # for self-hosted SearXNG

[[agents]]
id = "assistant"
model = "claude-sonnet-4-5"
workspace = "workspaces/assistant/"

[agents.heartbeat]
enabled = true
interval = "30m"
quiet_hours = "23:00-07:00"

[[agents]]
id = "codebot"
model = "claude-opus-4-5"
workspace = "workspaces/codebot/"
tools = { deny = ["browser"] }

# Channel → agent routing
[[bindings]]
channel = "discord"
server = "my-dev-server"
agent = "codebot"

[[bindings]]
channel = "*"
agent = "assistant"

[fly]
# Only needed if sandbox.provider = "fly"
# api_token = "..."
# region = "ord"
# app = "smithly-workers"
```

---

## 16. CLI Commands

```sh
# Setup & Runtime
smithly init                          # First-time setup wizard
smithly start                         # Start gateway + all agents
smithly stop                          # Graceful shutdown
smithly status                        # Running state, agents, channels, skills
smithly doctor                        # Diagnostics (Docker? Fly? Ollama?)

# Agents
smithly agent list                    # Show all agents + status
smithly agent add <id>                # Create a new agent with workspace
smithly agent remove <id>             # Remove an agent
smithly agent edit <id>               # Open agent's workspace files

# Key Management
smithly key generate --name <n>       # Generate Ed25519 keypair
smithly key list                      # Show your keys
smithly key export <n>                # Export public key for sharing

# Skills
smithly skill add <path>              # Verify + scan + approve (code) or install (instruction)
smithly skill remove <n>              # Revoke + disable
smithly skill list                    # Show skills + type + status + storage
smithly skill scan <path>             # Re-run scan without installing
smithly skill update <n>              # Re-verify after git pull
smithly skill import-openclaw <path>  # Import OpenClaw skill
smithly skill storage <n>             # Show storage usage for a skill
smithly skill create <n> --type instruction  # Scaffold a new instruction skill

# Trust
smithly trust author <pubkey>         # Trust a skill author
smithly trust list                    # Show trusted authors
smithly trust revoke <pubkey>         # Revoke trust

# Network
smithly domain list                   # Show allowlist/denylist
smithly domain allow <domain>         # Pre-approve a domain
smithly domain deny <domain>          # Block a domain
smithly domain log                    # Show recent network activity

# Memory
smithly memory search <query>         # Hybrid vector + keyword search
smithly memory search <query> --agent <id>  # Search specific agent's memory
smithly memory stats                  # Usage by agent + trust level
smithly memory export                 # Export as JSON

# Audit
smithly audit show                    # Recent audit log
smithly audit show --agent <id>       # Filter by agent
smithly audit show --skill weather    # Filter by skill
smithly audit show --domain           # Show domain access history
smithly audit export                  # Export full log

# Channels & Remote
smithly channel list                  # Channel status
smithly channel test <n>              # Send test message
smithly tunnel setup <provider>       # Configure Tailscale/Cloudflare
smithly config edit                   # Open smithly.toml
```

---

## 17. Skill Registry — skills.smithly.dev

### 17.1 Infrastructure (Cloudflare)

The registry runs entirely on Cloudflare to avoid egress costs:

| Component | Service | Cost at scale |
|---|---|---|
| Registry API | **Workers** | Free tier: 100k req/day. Paid: $5/mo unlimited |
| Registry DB | **D1** (SQLite at edge) | Free tier: 5M reads/day. Pennies beyond |
| Skill packages | **R2** | Storage only — **zero egress**. ~$0.015/GB/mo stored |
| Registry frontend | **Pages** | Free |
| Author avatars/assets | **R2** | Same — zero egress |

At 10k users pulling skills daily, monthly cost is roughly **$5-10/mo**. Compare to S3 where the same traffic could be $50-500/mo in egress alone.

### 17.2 Registry Features

```
┌─────────────────────────────────────────────────────────┐
│  weather-skill v1.2.0                                   │
│  by alice (github.com/alice) — verified                 │
│  Type: code skill                                       │
│                                                         │
│  Signed by author                                       │
│  Automated scan: PASS (0 critical, 0 high)              │
│  Author history: 12 skills, 0 violations                │
│                                                         │
│  Capabilities:                                          │
│    Network: api.openweathermap.org                      │
│    Env: OPENWEATHER_API_KEY                             │
│    Storage: 1 public table, 1 private table             │
│                                                         │
│  Install: smithly skill install weather-skill            │
│                                                         │
│  Downloads: 1,234  |  Last scan: 2026-02-24             │
└─────────────────────────────────────────────────────────┘
```

### 17.3 Author Trust & Enforcement

- Authors register via GitHub OAuth
- Author identity tied to all published skills (both types)
- Instruction skills scanned for prompt injection on publish
- Code skills scanned + signature verified on publish
- **Repeat offenders**: if an author publishes skills that keep getting flagged, the registry warns users on their skill pages and can block publishing
- Author violation history is public: "2 of 15 skills flagged, 1 removed"

### 17.4 Publishing Flow

```
smithly skill publish <path>
  → Verify signature (code skills)
  → Run injection scan (instruction skills)
  → Upload to R2
  → Insert metadata into D1
  → Visible on skills.smithly.dev with scan report + author history
```

---

## 18. Pricing Tiers

### 18.1 Three Tiers

| | **Free** (Self-Hosted) | **Pro** (Hosted) | **Enterprise** (Self-Hosted) |
|---|---|---|---|
| **How it runs** | Your machine/VM. We host nothing. | We run it on Fly + Cloudflare | You run it on your infra, we support it |
| **Target** | Hobbyists, solo devs | Individuals, small teams | Companies with compliance/data residency needs |
| **Agents** | Unlimited | Unlimited | Unlimited |
| **Sandbox** | Docker / none | Fly Machines (managed) | Docker / Firecracker on your infra |
| **Skill storage** | SQLite + local files | D1 + R2 (managed) | Your Postgres + your R2/S3 |
| **Channels** | All | All + managed webhooks | All + managed webhooks |
| **Memory** | Local SQLite | Managed + daily backup | Your infra, we provide the tooling |
| **Heartbeat/cron** | Yes | Yes + uptime monitoring | Yes + monitoring integration |
| **Webhooks** | Yes (you manage tunnels) | Managed endpoints | Your infra, your DNS |
| **Skill registry** | Browse + install | Browse + install + **verified publisher** | Browse + install + private registry |
| **Team features** | No | Up to 5 users | Unlimited users, RBAC, SSO/SAML |
| **Audit** | Local SQLite | Managed + 90-day retention | Export to your SIEM, unlimited retention |
| **Support** | Community | Email | Dedicated + SLA |
| **Price** | $0 | $15-30/user/mo | Custom (annual contract) |

### 18.2 Why Self-Hosted Enterprise Is the Highest Tier

Counterintuitive — usually self-hosted = free. But enterprise self-hosted is **more** expensive because:

- They need **support + SLA** — when it breaks at 2am, they need someone to call
- They need **compliance certifications** — SOC2, audit export to Splunk/Datadog, data residency guarantees
- They need **private skill registry** — their proprietary skills never touch the public registry
- They need **SSO/SAML** — their identity provider, their access policies
- They need **RBAC** — who can create agents, who can approve skills, who can see audit logs
- They're running it on their own Kubernetes/VMs — they need deployment guides, Helm charts, Terraform modules

This is the Grafana/GitLab model: open source core, paid enterprise self-hosted with support and compliance features.

### 18.3 Revenue Model

| Stream | Tier | Description |
|---|---|---|
| **Hosted subscription** | Pro | $15-30/user/mo. Managed controller + Fly compute + R2 storage. |
| **Compute overage** | Pro | Fly Machine execution beyond included minutes. Usage-based. |
| **Verified publisher** | Pro/Enterprise | Paid tier on registry. Org badge, priority scanning, download analytics. |
| **Enterprise license** | Enterprise | Annual contract. Support SLA, compliance features, private registry. |
| **Professional services** | Enterprise | Custom skill development, deployment assistance, training. |

### 18.4 Unit Economics

**Pro tier at 1,000 users ($15/mo each):**

| | Monthly |
|---|---|
| Revenue | $15,000 |
| Fly controllers (1k machines) | -$1,940 |
| Fly skill execution | -$34 |
| Cloudflare registry | -$5 |
| **Gross margin** | **$13,021 (87%)** |

**Enterprise tier** is even better — they run the infra, you sell the license + support. Near-zero COGS beyond your time.

### 18.5 Licensing — BSL (Business Source License)

The entire codebase is under the **BSL (Business Source License)**. This means:

- **Free to use** — anyone can self-host, run multi-agent, use every feature, modify the source, for any purpose
- **Free to contribute** — full source is public, PRs welcome
- **One restriction** — you cannot offer Smithly as a hosted/managed service to third parties without a commercial agreement
- **Converts to open source** — after 4 years, each version converts to Apache 2.0

This specifically prevents AWS, Google, Azure from wrapping Smithly in a managed service without paying. But individuals, companies, and teams can self-host freely with zero restrictions.

**Why BSL, not MIT/AGPL:**

- MIT: lets AWS offer "Managed Smithly" on day one. They capture the value, we get nothing.
- AGPL: technically requires service providers to release source, but cloud providers have lawyers and workarounds. Also scares some enterprise adopters.
- BSL: clear, simple, battle-tested (HashiCorp, MariaDB, CockroachDB). One restriction, plainly stated. Converts to full open source after 4 years.
- SSPL: more aggressive than needed. We don't need to force AWS to open-source their entire stack — we just need them to not compete with our hosted offering.

**What's free (everything):**

| Component | Included in self-hosted |
|---|---|
| Go controller binary | Yes |
| Multi-agent + workspaces (soul, identity, memory) | Yes |
| Instruction skills + code skills (signing, scanning) | Yes |
| Skill storage (SQLite + local files) | Yes |
| Docker + none sandbox providers | Yes |
| Network gatekeeper | Yes |
| All channels (Telegram, Discord, Slack, CLI, Web) | Yes |
| Webhooks | Yes |
| Memory + vector search + FTS | Yes |
| Heartbeat/cron | Yes |
| Audit logging (local SQLite) | Yes |
| Skill registry client (browse + install) | Yes |
| Content firewall | Yes |

**What you pay for (hosted service + enterprise support):**

| Component | Why it's paid |
|---|---|
| Hosted Smithly Cloud (we run it) | Managed infrastructure — we operate the controller, compute, storage |
| Fly sandbox provider | Managed remote execution |
| D1 + R2 storage provider | Managed cloud storage |
| Managed webhook endpoints | No tunnel setup needed |
| Team features (multi-user, RBAC) | Pro/Enterprise add-on |
| SSO / SAML | Enterprise add-on |
| Audit export to SIEM | Enterprise add-on |
| Private skill registry | Enterprise — proprietary skills stay private |
| Verified publisher program | Registry monetization |
| Dashboard + monitoring | Operational visibility |
| Deployment tooling (Helm, Terraform) | Enterprise self-hosted support |
| Support SLA | Enterprise requirement |

**Repo structure:**

```
github.com/smithly/smithly          ← BSL licensed, full source public
```

Single repo. The BSL additional use grant specifies: "You may use the Licensed Work for any purpose, including production use, except you may not offer the Licensed Work to third parties as a managed service." Pro/Enterprise features like RBAC, SSO, SIEM export, and private registry are in the same repo but behind a license key check.

---

## 19. Enterprise Wedge Use Cases

These are the use cases that sell Smithly into organizations. They're things teams want to do **right now** but can't because OpenClaw is banned by every security vendor (CrowdStrike, Microsoft, Malwarebytes all published explicit warnings). Smithly's audit trail, signed skills, sandboxed execution, and deny-by-default permissions make these conversations a CISO can actually have.

**The pitch:** "Your developers are going to use AI agents whether you approve them or not. OpenClaw is banned by every security vendor. Give them Smithly instead — same capabilities, audit trail on everything, deny-by-default permissions, data stays on your network."

### 19.1 Code Review Agent

The sharpest wedge. Every engineering team wants automated code review and nobody trusts OpenClaw near their codebase.

- **Trigger:** Webhook from GitHub/GitLab/Bitbucket/Azure DevOps — no polling, the forge tells the agent when a PR is opened
- **Instruction skill** defines the team's coding standards, patterns, anti-patterns, style preferences
- **Code skill** reads the PR diff via forge API (declared domain)
- **Memory learns the codebase** — stores patterns, past review feedback, what the team likes/dislikes in skill storage tables (public read so other agents can reference them)
- Leaves structured review comments, flags issues by severity
- **Gets smarter over time** — when a reviewer overrides the agent's feedback ("this is fine actually"), the agent stores that calibration
- **Enterprise win:** full audit trail of every review, agent can only access declared repos, SOC2-friendly

### 19.2 Production Monitor → Auto-Fix Agent

Self-healing production. The agent doesn't just alert — it diagnoses, fixes, and opens a PR.

```
Heartbeat fires every 10 minutes
    │
    ├── Code skill: pull logs from Datadog/CloudWatch/Loki
    │
    ├── Memory: "have we seen this error before?"
    │   → YES: link to previous fix, check if it regressed
    │   → NO: new issue, proceed
    │
    ├── Search tool: look up error, find relevant docs
    │
    ├── Code skill: create Jira/Linear ticket with context
    │   (error logs, affected endpoint, frequency, severity)
    │
    ├── Code skill: clone repo, checkout branch, apply fix
    │   (runs in sandbox — can't touch prod, only the repo)
    │
    ├── Code skill: run tests in sandbox to verify fix
    │
    ├── Code skill: open PR with:
    │   - Link to ticket
    │   - Error logs that triggered it
    │   - Explanation of the fix
    │   - Test results
    │
    └── Alert via Slack/Telegram:
        "NullPointerException in /api/orders (23 hits in 10 min).
         Created PROJ-1234. Opened PR #567. Tests pass."
```

**Escalation tiers** (defined in instruction skill):
- LOW: ticket + fix + PR + notify in morning digest
- MEDIUM: ticket + fix + PR + notify immediately
- CRITICAL: ticket + fix + PR + **page on-call**
- UNKNOWN: ticket only, don't attempt fix, just alert with context

The agent learns your severity calibration over time. "That wasn't critical, recalibrate" → stored in memory.

**Enterprise win:** on-call engineer gets paged at 3am with a ready-to-review PR instead of spending 45 minutes diagnosing. Full audit trail of every log pull, ticket, and code change. The fix runs in a sandbox — the agent can never touch production directly.

### 19.3 Meeting → Action Agent

- Takes meeting recordings/transcripts (from Zoom/Meet integration or uploaded)
- Extracts action items, decisions, owners, deadlines
- Creates Jira/Linear/GitHub tickets with full context
- Plans the tickets (writes descriptions, acceptance criteria, subtasks)
- Can start work on assigned tasks (spawns sub-agents)
- **Enterprise win:** nothing falls through the cracks, every extraction is auditable, meeting summaries are searchable in memory, decisions are tracked over time

### 19.4 Secure Internal Knowledge Agent

- Agent that knows your internal docs, runbooks, processes, architecture decisions
- Team members ask it questions via Slack/Teams
- Memory is trust-tagged — verified docs are `trusted`, casual conversation is `semi-trusted`
- Learns from corrections ("that's outdated, we switched to X")
- **Enterprise win:** data never leaves your network (local controller + local Ollama for embeddings), audit trail of every question and answer, institutional knowledge doesn't walk out the door when people leave

### 19.5 Compliance & Security Monitor

- Heartbeat agent scans dependencies weekly for vulnerabilities
- Monitors production alerts for anomalies
- Generates compliance reports on schedule
- Cross-references findings against team's remediation history (stored in memory)
- **Enterprise win:** continuous compliance, every finding logged, auditor-friendly export to SIEM

### 19.6 Customer Support Triage

- Agent handles first-line support via configured channels (Slack, email, web chat)
- Instruction skills define escalation rules, tone, boundaries
- Learns resolution patterns over time (memory + skill storage)
- Escalates to humans when uncertain — never makes up answers
- **Enterprise win:** audit trail of every customer interaction, data stays on-prem, deny-by-default means agent can't go rogue

### 19.7 The Enterprise Moat

These use cases aren't just features — they require capabilities OpenClaw structurally cannot provide:

| Enterprise need | OpenClaw | Smithly |
|---|---|---|
| Audit trail | None | Append-only log, every action, exportable to SIEM |
| Auth | Disabled by default | Mandatory, cannot be disabled |
| Skill safety | 20% malware on ClawHub | Signed + scanned + sandboxed + domain-locked |
| Network control | None | Gatekeeper — every outbound request logged and controlled |
| Webhooks | Supported but no access control | Gateway accepts webhooks, routes to bound agents |
| Team use | Single user | Multi-agent with channel bindings, future RBAC |
| SSO | No | Hosted tier |
| Data residency | No control | Local-first, controller runs on your infra |
| Compliance | Can't prove anything | Audit export, permission logs, domain access history |
| Agent learning | Flat files, no trust | Structured memory with trust tagging + vector search |
| Approval workflows | None | Content firewall — untrusted actions require human approval |

---

## 20. Webhooks

The gateway accepts inbound webhooks to trigger agent actions without polling. This is how the code review agent works — GitHub sends a webhook when a PR opens, the gateway routes it to the right agent.

### 20.1 How It Works

```toml
[[webhooks]]
path = "/hooks/github"
secret = "whsec_..."          # HMAC verification
agent = "codebot"
skill = "code-review"         # optional: route directly to a skill
```

```
GitHub PR opened
    │
    POST https://smithly.example.com/hooks/github
    │  (via tunnel — Tailscale/Cloudflare)
    │
    ├── Gateway verifies HMAC signature
    ├── Routes payload to agent "codebot"
    ├── Agent processes webhook as an incoming message
    └── Agent invokes code-review skill with PR data
```

### 20.2 Security

- Each webhook endpoint has its own HMAC secret — forged webhooks are rejected
- Webhook payloads are tagged as `semi-trusted` by the content firewall
- The agent can't be tricked by a crafted webhook payload because the firewall flags injection patterns
- All webhook deliveries are logged in the audit table

---

## 22. Defense in Depth (Network Security)

Three layers. A code skill must beat ALL three to exfiltrate data:

**Layer 1: Static Scanner (Install Time)** — catches obvious fetch/curl calls, obfuscation patterns, eval usage. Necessary but insufficient.

**Layer 2: Docker Network Isolation (Runtime)** — THE KEY LAYER. Container has no direct internet access. All TCP/DNS leaves through one chokepoint: the gatekeeper proxy. Undeclared domains are blocked regardless of HTTP method.

**Layer 3: Behavioral Monitoring (Runtime)** — logs every outbound request. Flags anomalies: new domains, unusual data volume, IP-based requests, high-entropy domain names.

The **search tool** is the only path for open-ended web reads, and it runs in the controller — not in skill sandboxes. Skills cannot use it.

For `none` provider: only layers 1 and 3 apply, plus Go-level proxy hooks. Docker is the default for a reason.

---

## 23. Product Gaps (To Address)

These are known gaps that need to be solved but don't need to block v1.

### 23.1 First-Run Experience / Time-to-Value

`smithly init` needs to get a user from install to working agent in under 5 minutes. Should ask 3 questions (name, LLM provider, API key), create a default workspace with a starter soul, and drop into CLI chat. User talks to their agent in 60 seconds.

### 23.2 Templates / Starter Kits

`smithly init --template code-review` scaffolds the whole workspace + installs the right skills. Templates for each enterprise use case (code review, prod monitor, meeting agent, support triage) are how you get people hooked fast.

### 23.3 LLM Cost Control

Known OpenClaw pain point — agents loop and burn API credits. Smithly needs:
- Per-agent token/spending limits
- Per-heartbeat-tick token budgets
- Alerts when spending spikes
- Auto-pause agent if budget exceeded

### 23.4 Error Handling / Recovery

- LLM API rate limits → exponential backoff + retry
- Skill crash mid-execution → rollback storage writes, log error, notify user
- Heartbeat task fails N times → circuit breaker, disable task, alert user
- Runaway agent → per-agent rate limits on skill invocations and LLM calls

### 23.5 Observability / Debugging

When an agent does something wrong, you need to see:
- Full system prompt that was assembled
- Which memories were loaded and why
- Which instruction skills were triggered
- LLM reasoning chain / tool calls
- `smithly agent logs <id>` should show a conversation-level trace

### 23.6 Skill Development Experience

- `smithly skill dev <path>` — hot-reload mode for developing skills locally
- Test harness — invoke a skill with mock input, inspect output
- `smithly skill test <path>` — run skill's declared test cases
- Without good DX, nobody contributes to the ecosystem

### 23.7 Backup / Restore / Migration

- `smithly backup` → snapshot SQLite DB + workspace files + skill storage to a tarball
- `smithly restore <path>` → restore from backup
- Covers machine migration, disaster recovery
- Enterprise will ask about this day one

### 23.8 OpenClaw Migration — Full Workspace

Beyond `smithly skill import-openclaw`:
- `smithly migrate-from-openclaw <path>` converts the whole workspace
- Maps SOUL.md → SOUL.md (mostly 1:1)
- Maps AGENTS.md → INSTRUCTIONS.md
- Maps USER.md → USER.md
- Maps MEMORY.md + memory/*.md → memory table entries
- Dramatically lowers switching cost

### 23.9 Web UI Scope

Web UI is more than just a chat channel. It should also surface:
- Agent status dashboard (which agents are running, last heartbeat, errors)
- Audit log viewer (filterable by agent, skill, domain)
- Skill manager (installed skills, scan reports, storage usage)
- Memory browser (search, view trust levels, delete entries)
- Domain allowlist manager (approve/deny, see access history)

### 23.10 Notifications vs Conversations

Channels cover two-way chat, but agents also need one-way alerting:
- Push notifications
- Email alerts (SMTP or provider API)
- PagerDuty / OpsGenie integration for critical alerts
- Notification = fire-and-forget, not a conversation thread

### 23.11 Graceful Degradation

What happens under partial failure:
- Ollama down → skip embedding generation, fall back to keyword-only search
- Docker unavailable → warn user, offer "none" provider
- LLM provider down → queue messages, retry when back
- No internet → allow local-only operations, queue outbound
- Agent should degrade capabilities, not crash entirely

### 23.12 Registry Architecture

The skill registry (`skills.smithly.dev`) is a **separate closed-source project**. It is not part of the BSL-licensed Smithly controller. It runs on our Cloudflare account and is our proprietary service.

| Component | License |
|---|---|
| `smithly` controller | BSL (source public) |
| `skills.smithly.dev` registry | Closed source, proprietary |
| Pro/Enterprise features | In BSL repo, behind license key |

The CLI's `smithly skill install` command is just an HTTP client talking to the registry API. If someone forks Smithly, they don't get the registry or the skill ecosystem — that's our moat.

---

## 24. Open Questions

1. **Signing key distribution** — GitHub-linked identity? Keybase-style proofs? Manual trust like SSH?
2. **Skill auto-update policy** — If capabilities unchanged + same trusted author, auto-approve?
3. **Domain wildcards** — Allow `*.openweathermap.org` or exact subdomains only?
4. **Offline mode** — Allow previously-approved domains, block everything new?
5. **Multi-user** — One instance per user, or multi-user support?
6. ~~**License**~~ — **Decided: BSL** with 4-year conversion to Apache 2.0
7. ~~**Hosted tier**~~ — **Decided: Smithly Cloud** on Fly + Cloudflare (Pro tier), Enterprise self-hosted with support
8. **Storage migrations** — How to handle schema changes when a skill updates its table definitions?
9. **Storage quotas** — Per-skill limits on table rows / file storage size?
10. **Soul evolution** — Can agents propose changes to their own SOUL.md? User-approved only, or auto-evolve within boundaries?
11. **Agent-to-agent communication** — Can agents send messages to each other, or only share data via public skill storage?
12. **Search provider** — Self-hosted SearXNG for privacy? Or just Google/DuckDuckGo APIs?
