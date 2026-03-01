# Testing

## Unit tests

Run all unit tests (no API keys needed):

```bash
go test ./...
```

All tests use mock HTTP servers — no external services, no API costs. Covers the agent loop, tools, CLI channel, gateway, SQLite store, sandbox providers, gatekeeper, skills, and more.

Run a specific package:

```bash
go test ./internal/agent/ -v
go test ./internal/tools/ -v
go test ./internal/gateway/ -v
```

## LLM integration tests

These tests hit real LLM APIs and run the full agent loop: the LLM writes a bash skill, executes it, and verifies the output. Each test is **opt-in** — skipped when the required API key is absent.

### Gemini

```bash
GEMINI_API_KEY=your-key go test ./internal/agent/ -run TestLLMGemini -v
```

Default model: `gemini-2.5-flash`. Override:

```bash
GEMINI_API_KEY=your-key GEMINI_MODEL=gemini-2.5-pro go test ./internal/agent/ -run TestLLMGemini -v
```

### OpenAI

```bash
OPENAI_API_KEY=your-key go test ./internal/agent/ -run TestLLMOpenAI -v
```

Default model: `gpt-5.3-codex`. Override:

```bash
OPENAI_API_KEY=your-key OPENAI_MODEL=gpt-4.1 go test ./internal/agent/ -run TestLLMOpenAI -v
```

Codex models (`*-codex`) are automatically routed through the Responses API (`/v1/responses`). All other OpenAI models use Chat Completions (`/v1/chat/completions`).

### What the LLM integration test does

1. Creates a real agent with `write_skill` and `run_code_skill` tools
2. Sends: "Write a bash skill that adds two numbers from JSON stdin, run it with a=7 and b=8"
3. The LLM figures out the tool sequence on its own (write the skill, then execute it)
4. Asserts: both tools were called, a skill was registered, and the output contains "15"

The test has a 60-second timeout. It typically completes in 3-10 seconds depending on the model.

### Environment variables

| Variable | Required for | Default |
|----------|-------------|---------|
| `GEMINI_API_KEY` | Gemini test | (skipped if absent) |
| `GEMINI_MODEL` | Gemini test | `gemini-2.5-flash` |
| `OPENAI_API_KEY` | OpenAI test | (skipped if absent) |
| `OPENAI_MODEL` | OpenAI test | `gpt-5.3-codex` |

## Provider routing

The `provider` field in `smithly.toml` determines which API wire format is used:

| Provider | API endpoint | Used by |
|----------|-------------|---------|
| `openai` (default) | `/v1/chat/completions` | GPT-4o, GPT-4.1, GPT-5, o3, o4-mini |
| `openai` + codex model | `/v1/responses` (auto-detected) | gpt-5.3-codex, gpt-5.2-codex, gpt-5.1-codex |
| `openai-responses` | `/v1/responses` (forced) | Any OpenAI model via Responses API |
| `gemini` | `/v1beta/openai/chat/completions` | Gemini models via OpenAI-compatible endpoint |
| `anthropic` | `/v1/chat/completions` | Claude models via OpenAI-compatible endpoint |
| `ollama` | `/v1/chat/completions` | Local models |
| `openrouter` | `/v1/chat/completions` | Any model via OpenRouter |

## Supported models (with built-in pricing)

Cost tracking works out of the box for these models. Unknown models fall back to Claude Sonnet 4.6 pricing.

### OpenAI

| Model | Input $/1M | Output $/1M | Cached $/1M | API |
|-------|-----------|------------|------------|-----|
| `gpt-5.3-codex` | $1.75 | $14.00 | $0.175 | Responses |
| `gpt-5.2-codex` | $1.75 | $14.00 | $0.175 | Responses |
| `gpt-5.2` | $1.75 | $14.00 | $0.175 | Both |
| `gpt-5.1-codex` | $1.25 | $10.00 | $0.125 | Responses |
| `gpt-5.1` | $1.25 | $10.00 | $0.125 | Both |
| `gpt-5` | $1.25 | $10.00 | $0.125 | Both |
| `gpt-5-mini` | $0.25 | $2.00 | $0.025 | Both |
| `gpt-5-nano` | $0.05 | $0.40 | $0.005 | Both |
| `gpt-4.1` | $2.00 | $8.00 | $0.50 | Chat Completions |
| `gpt-4.1-mini` | $0.40 | $1.60 | $0.10 | Chat Completions |
| `gpt-4.1-nano` | $0.10 | $0.40 | $0.025 | Chat Completions |
| `gpt-4o` | $2.50 | $10.00 | $1.25 | Chat Completions |
| `gpt-4o-mini` | $0.15 | $0.60 | $0.075 | Chat Completions |
| `o3` | $2.00 | $8.00 | $0.50 | Both |
| `o3-mini` | $1.10 | $4.40 | $0.55 | Both |
| `o4-mini` | $1.10 | $4.40 | $0.275 | Both |

### Anthropic

| Model | Input $/1M | Output $/1M | Cached $/1M |
|-------|-----------|------------|------------|
| `claude-opus-4-6` | $15.00 | $75.00 | $1.50 |
| `claude-sonnet-4-6` | $3.00 | $15.00 | $0.30 |
| `claude-haiku-4-5-20251001` | $0.80 | $4.00 | $0.08 |

### Gemini

| Model | Input $/1M | Output $/1M | Cached $/1M |
|-------|-----------|------------|------------|
| `gemini-3.1-pro-preview` | $2.00 | $12.00 | $0.20 |
| `gemini-3-pro-preview` | $2.00 | $12.00 | $0.20 |
| `gemini-3-flash-preview` | $0.50 | $3.00 | $0.05 |
| `gemini-2.5-pro` | $1.25 | $10.00 | $0.125 |
| `gemini-2.5-flash` | $0.30 | $2.50 | $0.03 |
| `gemini-2.5-flash-lite` | $0.10 | $0.40 | $0.01 |

## Running all checks

```bash
go build ./...              # compile
go vet ./...                # static analysis
go test ./... -count=1      # all unit tests (no API keys)
```
