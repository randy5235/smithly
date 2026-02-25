# Smithly — Backlog

> BSL-licensed open source controller. Source public at github.com/smithly/smithly

## Phase 1: Core — Get Something Running ✅

### Go Scaffold
- [x] Go module init, project structure
- [x] CLI skeleton (flag-based)
- [x] TOML config loader (`smithly.toml`)
- [x] `smithly init` — first-run wizard (name, LLM provider, API key, Brave Search key)
- [x] `smithly start` / `smithly chat`
- [x] `smithly doctor` — check config + Docker availability

### SQLite
- [x] Database setup (modernc.org/sqlite, pure Go, no CGo)
- [x] Core tables: agents, memory, bindings, domain_allowlist, skills, trusted_authors, audit_log
- [x] Migration runner (embed SQL files, run on startup)
- [x] Store interface abstraction (supports future Postgres/MongoDB backends)
- [x] Shared conformance test suite (storetest.RunAll)

### Gateway
- [x] HTTP server on 127.0.0.1, configurable port
- [x] Bearer token auth (auto-generated on first run, persisted to config)
- [x] Rate limiting (60 req/min per IP, sliding window)

### Agent Loop
- [x] Single agent loop — send messages to LLM, get responses
- [x] OpenAI-compatible API client (works with Anthropic, OpenAI, OpenRouter, Ollama)
- [x] Streaming responses
- [x] System prompt assembly from workspace files (SOUL.md + INSTRUCTIONS.md + USER.md)
- [x] Workspace loader — read Markdown/TOML files from agent workspace directory
- [x] Tool-use support with multi-turn tool calling (up to 20 iterations)
- [x] User approval flow for dangerous tools

### Built-in Tools
- [x] Tool interface + Registry + OpenAI tool format
- [x] search — web search (Brave/DuckDuckGo) + read results, no approval needed
- [x] fetch — arbitrary URL access, needs approval
- [x] bash — shell commands, needs approval
- [x] read_file, write_file, list_files — filesystem access
- [x] claude_code — delegate to Claude Code CLI
- [x] robots.txt compliance (search + fetch respect robots.txt)

### CLI Channel
- [x] Interactive terminal chat with the agent
- [x] Tool call display + approval prompts
- [x] End-to-end working: init → chat → tools → audit

### Audit Logging
- [x] Append-only audit_log table
- [x] Log every LLM call, tool invocation
- [x] `smithly audit show` with --agent and --limit flags

### Tests (120+)
- [x] Agent loop: 12 tests (mock LLM, tool calls, streaming, persistence, audit, errors)
- [x] CLI channel: 8 tests (exit, chat, tools, banner, EOF)
- [x] Gateway: 8 tests (health, auth, chat endpoint, rate limiting, errors)
- [x] Tools: 41 tests (search permissions, robots.txt, fetch, bash, files, schema)
- [x] Config: 6 tests (write/load, defaults, multi-agent, Ollama, token persistence)
- [x] SQLite: 13 conformance tests
- [x] Workspace: 4 tests

---

## Phase 2: Multi-Agent + Soul

### Multi-Agent
- [x] Per-agent LLM model configuration
- [x] Per-agent tool configuration (`tools = ["search", "fetch"]`)
- [x] Agent management CLI (`smithly agent add/remove/list`)
- [ ] Multiple agent loops under one gateway
- [ ] Per-agent workspace isolation (soul, identity, memory, permissions)
- [ ] Per-agent skill bindings

### Channel Bindings
- [ ] Route channels → agents via binding rules
- [ ] Priority-based matching (most specific wins)
- [ ] Default catch-all agent

### Workspace Files
- [x] SOUL.md — behavioral philosophy
- [x] IDENTITY.toml — external presentation (name, emoji, avatar)
- [x] USER.md — user info/preferences
- [x] INSTRUCTIONS.md — operating rules
- [x] HEARTBEAT.md — recurring task checklist (configurable interval + quiet hours)
- [x] BOOT.md — startup checklist (runs on agent start)
- [x] System prompt assembly with context window token estimation + history truncation
- [x] Configurable max context window per agent (`max_context`)

---

## Phase 3: Skills — Both Types

### Instruction Skills
- [ ] Markdown loader — read INSTRUCTIONS.md from skill directory
- [ ] Manifest parser (`manifest.toml` with type, triggers, requires)
- [ ] Trigger matching — load instruction skill into context when relevant
- [ ] Dependency declaration — requires code skills, tools, domains
- [ ] Injection scanner — content firewall patterns against Markdown at install time
- [ ] Author identity tracking — tied to author account

### Code Skills
- [ ] Ed25519 key generation (`smithly key generate`)
- [ ] Key management (`smithly key list/export`)
- [ ] Skill signing (`smithly skill sign`)
- [ ] Signature verification on install
- [ ] File hash verification on every invocation
- [ ] AST-based static scanner
- [ ] Scan report generation
- [ ] Install flow: verify → scan → user review → approve
- [ ] `smithly skill add/remove/list/scan/update`

---

## Phase 4: Skill Storage

### StorageProvider Interface
- [ ] Interface definition (table ops + file ops + cross-skill reads)
- [ ] Access control enforcement (private/public read, never public write)

### Local Storage
- [ ] SQLite tables namespaced as `skill_{name}_{table}`
- [ ] Local files under `data/skills/{name}/`
- [ ] Storage manifest in skill TOML (table definitions, file paths, access levels)
- [ ] Cross-skill public reads with access check
- [ ] Audit logging for all storage operations

### Docker Storage Sidecar
- [ ] Small Go binary that runs in container
- [ ] Proxies storage calls back to host over Unix socket
- [ ] JSON protocol for skill ↔ sidecar communication

---

## Phase 5: Network Gatekeeper + Search

### Search Tool
- [ ] Built-in agent tool — read any URL, search any query
- [ ] Configurable search provider (DuckDuckGo, Google, SearXNG)
- [ ] GET-only, never POSTs data
- [ ] Runs in controller, not in skill sandboxes

### Domain Gatekeeper
- [ ] Domain allowlist in SQLite
- [ ] HTTP proxy for outbound requests from code skills
- [ ] Code skill domain declaration in manifest + enforcement
- [ ] First-access prompt flow for undeclared domains
- [ ] Pre-seeded allow/deny lists
- [ ] `smithly domain list/allow/deny/log`

---

## Phase 6: Sandbox Providers

### Interface
- [ ] SandboxProvider interface (Run, Destroy, Available)

### Docker Provider (default)
- [ ] Ephemeral containers (`--rm`)
- [ ] Skill code mounted read-only, workspace read-write
- [ ] Internal Docker network (no direct internet)
- [ ] All traffic through gatekeeper proxy
- [ ] Resource limits (memory, CPU)
- [ ] Storage sidecar integration

### None Provider
- [ ] Raw shell execution
- [ ] Scary warning + explicit opt-in
- [ ] Go-level proxy hooks for fetch/http
- [ ] Command parsing for curl/wget domain extraction

### Fly Provider (stub)
- [ ] Interface implementation
- [ ] Basic Fly Machine API calls (create, ship code, collect result, destroy)

### Diagnostics
- [ ] `smithly doctor` — check Docker, Fly, Ollama, KVM availability

---

## Phase 7: Memory + Search (Heartbeat moved to Phase 2 ✅)

### Memory
- [ ] Vector search (sqlite-vec or pure Go KNN)
- [ ] Local embedding generation via Ollama
- [ ] FTS5 keyword search
- [ ] Hybrid search with trust weighting
- [ ] Per-agent memory partitioning
- [ ] `smithly memory search/stats/export`

---

## Phase 8: Channels

### Channel Adapters
- [ ] Channel interface definition
- [ ] Telegram adapter
- [ ] Discord adapter
- [ ] Slack adapter
- [ ] Web UI channel (chat + agent dashboard)
- [ ] Session management (for web UI)
- [ ] CSRF protection (for web UI)

### Webhooks
- [ ] Inbound webhook handler
- [ ] HMAC signature verification
- [ ] Route webhook → agent/skill via config
- [ ] Payloads tagged `semi-trusted` by firewall

### Advanced
- [ ] Dynamic agent spawning (sub-agents)
- [ ] Browser automation in Docker (headless Chromium, fresh profile per task)
- [ ] OpenClaw skill importer

---

## Phase 9: Content Firewall

- [ ] Trust level tagging on all inbound content
- [ ] Injection pattern detection (instruction overrides, role injection, authority claims, encoded payloads)
- [ ] Auto human-approval gate for flagged content triggering tools
- [ ] Trust weighting in memory search results

---

## Phase 10: Polish + DX

### First-Run Experience
- [ ] `smithly init` — 3 questions, working agent in 60 seconds
- [ ] Templates: `smithly init --template code-review`
- [ ] Starter templates for enterprise use cases

### LLM Cost Control
- [ ] Per-agent token/spending limits
- [ ] Per-heartbeat-tick token budgets
- [ ] Alerts when spending spikes
- [ ] Auto-pause agent if budget exceeded

### Error Handling
- [ ] LLM API rate limits → exponential backoff + retry
- [ ] Skill crash → rollback storage writes, log error, notify
- [ ] Heartbeat circuit breaker — disable after N failures
- [ ] Per-agent rate limits on skill invocations

### Observability
- [ ] `smithly agent logs <id>` — conversation-level trace
- [ ] Show full assembled system prompt
- [ ] Show which memories/skills were loaded and why
- [ ] LLM reasoning chain / tool call log

### Skill Development
- [ ] `smithly skill dev <path>` — hot-reload dev mode
- [ ] Test harness — invoke with mock input, inspect output
- [ ] `smithly skill test <path>` — run declared test cases
- [ ] `smithly skill create <name> --type instruction` — scaffold

### Backup / Restore
- [ ] `smithly backup` → tarball of DB + workspaces + skill storage
- [ ] `smithly restore <path>`

### Migration
- [ ] `smithly migrate-from-openclaw <path>` — full workspace conversion
- [ ] Map SOUL.md, AGENTS.md, USER.md, MEMORY.md to Smithly equivalents

### Graceful Degradation
- [ ] Ollama down → keyword-only search (skip embeddings)
- [ ] Docker unavailable → warn, offer "none"
- [ ] LLM down → queue messages, retry
- [ ] No internet → local-only mode

### Notifications
- [ ] One-way alert channel (vs two-way conversation)
- [ ] Email alerts (SMTP)
- [ ] PagerDuty / OpsGenie integration
- [ ] Notification severity routing

---

## Phase 11: Desktop Application Support

> Let the agent control desktop apps — clicking, typing, reading screens.
> Docker can't do GUI. This runs either locally (`none` sandbox) or on cloud VM providers.

### Local Desktop (none sandbox)
- [ ] Desktop automation tool — Playwright or similar for native GUI
- [ ] Screen capture + OCR for reading app state
- [ ] Mouse/keyboard input simulation
- [ ] Window management (focus, resize, list open apps)
- [ ] Approval flow — user confirms before agent clicks/types
- [ ] macOS, Linux (X11/Wayland), Windows support

### Cloud Desktop Providers
- [ ] CloudDesktopProvider interface (provision, connect, execute, destroy)
- [ ] AWS WorkSpaces provider — full Windows/Linux VMs
- [ ] Azure Virtual Desktop provider
- [ ] MacStadium / AWS EC2 Mac provider — macOS VMs for Mac-only apps
- [ ] VNC/RDP connection for screen streaming to agent
- [ ] Session recording for audit trail

### Desktop Tool
- [ ] `desktop` tool — agent can launch apps, interact with GUI
- [ ] NeedsApproval: true (always, every action)
- [ ] Screenshot → LLM vision for understanding app state
- [ ] Coordinate system mapping (screen coords ↔ UI elements)
- [ ] Accessibility API integration (read UI tree without OCR where possible)

### Safety
- [ ] Per-app allowlist (agent can only interact with approved apps)
- [ ] Keystroke sanitization (no credential entry without explicit approval)
- [ ] Session isolation — cloud desktops are fresh per task
- [ ] Full audit log of every click, keystroke, screenshot

---

## Future

- [ ] Remote storage backend (Postgres + R2) for Fly provider
- [ ] Firecracker sandbox provider
- [ ] WhatsApp, Signal, iMessage channel adapters
- [ ] Agent-to-agent communication
- [ ] Soul evolution — agent proposes changes to SOUL.md, user approves
