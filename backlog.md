# Smithly — Backlog

> BSL-licensed open source controller. Source public at github.com/smithly/smithly

## Phase 1: Core — Get Something Running

### Go Scaffold
- [ ] Go module init, project structure
- [ ] CLI skeleton (cobra or flag)
- [ ] TOML config loader (`smithly.toml`)
- [ ] `smithly init` — first-run wizard (name, LLM provider, API key)
- [ ] `smithly start` / `smithly stop`
- [ ] `smithly doctor` — check dependencies (Docker, Ollama, etc.)

### SQLite
- [ ] Database setup (modernc.org/sqlite)
- [ ] Core tables: agents, memory, bindings, domain_allowlist, skills, trusted_authors, audit_log
- [ ] Migration runner (embed SQL files, run on startup)

### Gateway
- [ ] HTTP server on 127.0.0.1, configurable port
- [ ] Bearer token auth (auto-generated on first run)
- [ ] Session management
- [ ] CSRF protection
- [ ] Rate limiting

### Agent Loop
- [ ] Single agent loop — send messages to LLM, get responses
- [ ] OpenAI-compatible API client (works with Anthropic, OpenAI, OpenRouter, Ollama)
- [ ] Streaming responses
- [ ] System prompt assembly from workspace files (SOUL.md + INSTRUCTIONS.md + USER.md)
- [ ] Workspace loader — read Markdown/TOML files from agent workspace directory

### CLI Channel
- [ ] Interactive terminal chat with the agent
- [ ] This is the first channel — prove the loop works end-to-end

### Audit Logging
- [ ] Append-only audit_log table
- [ ] Log every LLM call, tool invocation, domain access
- [ ] `smithly audit show`

---

## Phase 2: Multi-Agent + Soul

### Multi-Agent
- [ ] Multiple agent loops under one gateway
- [ ] Per-agent workspace isolation (soul, identity, memory, permissions)
- [ ] Per-agent LLM model configuration
- [ ] Per-agent skill bindings
- [ ] Agent management CLI (`smithly agent add/remove/list/edit`)

### Channel Bindings
- [ ] Route channels → agents via binding rules
- [ ] Priority-based matching (most specific wins)
- [ ] Default catch-all agent

### Workspace Files
- [ ] SOUL.md — behavioral philosophy
- [ ] IDENTITY.toml — external presentation (name, emoji, avatar)
- [ ] USER.md — user info/preferences
- [ ] INSTRUCTIONS.md — operating rules
- [ ] HEARTBEAT.md — recurring task checklist
- [ ] BOOT.md — startup checklist
- [ ] System prompt assembly with token limit cap

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

## Phase 7: Heartbeat + Memory

### Heartbeat
- [ ] Per-agent scheduler (goroutine)
- [ ] HEARTBEAT.md loading into context on tick
- [ ] Quiet hours support
- [ ] BOOT.md — run on agent startup

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

## Future

- [ ] Remote storage backend (Postgres + R2) for Fly provider
- [ ] Firecracker sandbox provider
- [ ] WhatsApp, Signal, iMessage channel adapters
- [ ] Agent-to-agent communication
- [ ] Soul evolution — agent proposes changes to SOUL.md, user approves
