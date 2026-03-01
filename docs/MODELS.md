# Supported Models

Smithly works with any LLM that supports structured tool calling (function calling). This page covers which models work, how to configure each provider, and what to expect from local models.

---

## Providers

Smithly routes requests based on the `provider` field in your agent config. Each provider speaks a different API format — this is handled automatically.

| Provider | API Format | Models |
|----------|-----------|--------|
| `openai` (default) | Chat Completions | GPT-4o, GPT-4.1, GPT-5, o3/o4-mini |
| `openai-responses` | Responses API | GPT-5.x-codex (auto-detected) |
| `anthropic` | Messages API | Claude Opus, Sonnet, Haiku |
| `gemini` | Chat Completions (OpenAI-compatible) | Gemini 2.5/3 |
| `ollama` | Chat Completions (OpenAI-compatible) | Qwen, Llama, Mistral, etc. |
| `openrouter` | Chat Completions (OpenAI-compatible) | Any model on OpenRouter |

You do not need to set `provider` for OpenAI models — it defaults to `openai` and auto-detects when the Responses API is needed (codex models).

---

## Cloud Models

### OpenAI

```toml
[[agents]]
id      = "assistant"
model   = "gpt-4o"
api_key = "sk-..."
```

Codex models (`gpt-5.3-codex`, `gpt-5.2-codex`, etc.) are auto-detected and routed to the Responses API. No extra config needed.

```toml
[[agents]]
id      = "coder"
model   = "gpt-5.3-codex"
api_key = "sk-..."
```

### Anthropic

```toml
[[agents]]
id       = "claude"
model    = "claude-sonnet-4-6"
provider = "anthropic"
api_key  = "sk-ant-..."
```

### Gemini

```toml
[[agents]]
id       = "gemini"
model    = "gemini-2.5-flash"
provider = "gemini"
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key  = "AIza..."
```

### OpenRouter

```toml
[[agents]]
id       = "router"
model    = "anthropic/claude-sonnet-4-6"
provider = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key  = "sk-or-..."
```

---

## Local Models (Ollama)

Smithly works with [Ollama](https://ollama.com) via its OpenAI-compatible endpoint at `http://localhost:11434/v1`.

```toml
[[agents]]
id       = "local"
model    = "qwen3:8b"
provider = "ollama"
base_url = "http://localhost:11434/v1"

# Local inference has no cost — zero out pricing to avoid false limits
[agents.pricing]
input_per_million  = 0.0
output_per_million = 0.0
cached_per_million = 0.0
```

### Which models work?

Smithly relies on structured tool calling — the model must emit `tool_calls` in the API response, not describe tools in text. Many models can handle a simple single-tool call, but Smithly's agent loop requires **multi-tool orchestration**: choosing from several tools, generating complex arguments (code, JSON), and sequencing calls across turns.

We tested every major model family available through Ollama. Only the **Qwen family** reliably handles multi-tool agent workflows:

| Model | Size | Result | Speed | Notes |
|-------|------|--------|-------|-------|
| **qwen2.5:7b** | 4.7 GB | **Pass** | ~12s | Fastest but inconsistent — may need retries. |
| **qwen3:8b** | 5.2 GB | **Pass** | ~24s | Recommended. Reliable across runs. |
| **qwen3:30b-a3b** | 18 GB | **Pass** | ~43s | MoE (3B active). Best quality. Needs ~20 GB VRAM. |
| qwen3:14b | 9.3 GB | Fail | >60s | Correct tool calls but too slow — times out. |
| llama3.1:8b | 4.9 GB | Fail | — | Dumps tool calls as text instead of structured API calls. |
| mistral:7b | 4.4 GB | Fail | — | Same as Llama — describes tools in prose. |
| mistral-small (24B) | 14 GB | Fail | — | Returns empty response with complex tool sets. |
| phi4:14b | 9.1 GB | Fail | — | Ollama reports "does not support tools". |

**Recommendation:** Use `qwen3:8b` — it's the best balance of speed, reliability, and VRAM usage. Use `qwen2.5:7b` only if VRAM is very tight (<6 GB), but expect occasional failures. Use `qwen3:30b-a3b` if you have 20+ GB VRAM and want the best local quality.

### Why do non-Qwen models fail?

All tested models can handle a *simple* tool call (e.g., "call the `add` function with a=7, b=8"). The failure happens with complex multi-tool scenarios. When given 3+ tools with nested parameters and a multi-step task:

- **Llama 3.1 / Mistral 7B**: The model "knows" what tools to call but outputs them as JSON text in the response body instead of structured `tool_calls` in the API response. The agent loop never sees them.
- **Mistral Small (24B)**: Returns a completely empty response — the model appears to give up when overwhelmed by the tool schema.
- **Phi-4**: Ollama doesn't support tools for this model at all.

The Qwen models were specifically trained for agentic tool-calling workflows and handle the structured output format reliably even with complex multi-tool scenarios.

### Installing models

```bash
# Install Ollama: https://ollama.com/download

# Pull a recommended model
ollama pull qwen2.5:7b      # 4.7 GB — works on most GPUs
ollama pull qwen3:8b         # 5.2 GB — better quality, slower
ollama pull qwen3:30b-a3b    # 18 GB  — best quality, needs 20+ GB VRAM
```

### VRAM requirements

Ollama automatically offloads layers to GPU. Rough VRAM needs at Q4 quantization:

| Model | Minimum VRAM | Comfortable VRAM |
|-------|-------------|-----------------|
| qwen2.5:7b | 5 GB | 6 GB |
| qwen3:8b | 6 GB | 8 GB |
| qwen3:30b-a3b | 12 GB | 20 GB |

Models that exceed your VRAM will partially run on CPU, which is significantly slower.

---

## Local Models (llama.cpp)

For llama.cpp, vLLM, LM Studio, or any other OpenAI-compatible server, see [docs/local-llm.md](local-llm.md). The same model compatibility notes apply — use Qwen models for reliable tool calling.

---

## Cost Tracking

Smithly tracks estimated spending per model. Known pricing is built in for all cloud models listed above. For local models, set pricing to zero:

```toml
[agents.pricing]
input_per_million  = 0.0
output_per_million = 0.0
cached_per_million = 0.0
```

To add spending limits:

```toml
[[agents.cost_limits]]
dollars = 5.0
window  = "daily"

[[agents.cost_limits]]
dollars = 20.0
window  = "weekly"
```

---

## Context Window

The default context window is 128k tokens. Override it for models with smaller windows:

```toml
[[agents]]
max_context = 8192   # match your model/server's actual limit
```

Smithly trims conversation history to fit within this limit before each request.

---

## Restricting Tools

If a model struggles with too many tools, restrict which ones it sees:

```toml
[[agents]]
tools = ["search", "fetch", "run_code_skill"]
```

Leave `tools` empty or omit it to allow all registered tools.
