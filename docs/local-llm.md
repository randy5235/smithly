# Local LLM Provider (llama.cpp)

Smithly communicates with LLMs via the **OpenAI Chat Completions API** (`POST /chat/completions`). Any server that implements this API — including [llama.cpp's built-in HTTP server](https://github.com/ggerganov/llama.cpp/tree/master/examples/server) — works as a drop-in provider, whether running on `localhost` or anywhere else on your network.

---

## 1. How It Works

The agent (`internal/agent/agent.go`) sends JSON to `{base_url}/chat/completions` with bearer-token auth and SSE streaming. No provider-specific code paths exist: `provider` in the config is a display label only. The only fields that control API communication are `base_url` and `api_key`.

This means llama.cpp, vllm, LM Studio, Jan, and any other OpenAI-compatible server are all configured the same way.

---

## 2. Start the llama.cpp Server

### Local machine

```bash
# Minimal — binds to localhost only
llama-server \
  -m /path/to/model.gguf \
  --port 8080 \
  --ctx-size 8192 \
  --n-gpu-layers 35
```

### Accessible from other machines on the network

```bash
# Binds to all interfaces — visible on your LAN
llama-server \
  -m /path/to/model.gguf \
  --host 0.0.0.0 \
  --port 8080 \
  --ctx-size 8192 \
  --n-gpu-layers 35
```

The server exposes an OpenAI-compatible API at `http://<host>:8080/v1`.

> **Security note:** `--host 0.0.0.0` makes the server reachable by anyone on your network. Add `--api-key <secret>` if you want to require a bearer token, and consider firewall rules on untrusted networks.

### Useful flags

| Flag | Purpose |
|------|---------|
| `-m <file>` | Path to the GGUF model file |
| `--host <addr>` | Bind address (`127.0.0.1` or `0.0.0.0`) |
| `--port <n>` | TCP port (default `8080`) |
| `--ctx-size <n>` / `-c <n>` | Context window in tokens — **must match `max_context` in Smithly** |
| `--n-gpu-layers <n>` | Layers to offload to GPU (`-1` = all) |
| `--threads <n>` | CPU threads for inference |
| `--api-key <secret>` | Require this bearer token on all requests |
| `--parallel <n>` | Simultaneous request slots (default 1) |

---

## 3. Configure Smithly

### Manual edit (`smithly.toml`)

Add an `[[agents]]` section. At minimum you need `id`, `model`, `workspace`, and `base_url`.

```toml
[[agents]]
id        = "local"
model     = "llama-3.2-3b"   # display label — server uses whatever model was loaded
workspace = "workspaces/local"
provider  = "llamacpp"        # display label only; does not affect API calls
base_url  = "http://192.168.1.50:8080/v1"   # adjust to your server's address
# api_key = "secret"          # omit unless you started llama-server with --api-key

# Match the --ctx-size value used when starting llama-server.
# Smithly uses this to trim history before it overflows the context window.
max_context = 8192

# Local inference has no per-token cost.
# Setting pricing to $0 prevents false cost-limit triggers.
[agents.pricing]
input_per_million  = 0.0
output_per_million = 0.0
cached_per_million = 0.0
```

For a server running on the same machine:

```toml
base_url = "http://localhost:8080/v1"
```

### Interactive wizard (`smithly agent add`)

The interactive wizard does not have a llama.cpp option, but you can use it and then edit the file:

1. Run `smithly agent add`.
2. When prompted for a provider, choose **4 (Ollama)** — this skips the API-key prompt and sets a localhost base URL.
3. Accept the defaults, then open `smithly.toml` and update `base_url`, `model`, and `provider` to match your llama.cpp server.

---

## 4. Create the Workspace

If you are creating a new agent entry manually (not via the wizard), create the workspace directory and required files:

```bash
mkdir -p workspaces/local
echo "You are a helpful assistant." > workspaces/local/SOUL.md
printf 'name = "local"\nemoji = "🦙"\n' > workspaces/local/IDENTITY.toml
```

---

## 5. Verify

```bash
# Quick connectivity check
curl http://192.168.1.50:8080/v1/models

# Start Smithly and confirm the agent registers
smithly start
# Expected log line:
#   registered agent: local (model: llama-3.2-3b)

# Chat with it
smithly chat local
```

---

## 6. Context Window (`max_context`)

This is the most common misconfiguration. Smithly defaults to 128 000 tokens when `max_context` is 0. If llama-server was started with `--ctx-size 8192` and Smithly tries to send more tokens than that, the server will return an error.

- Set `max_context` to the **same value** as `--ctx-size` (or lower).
- Smithly uses this value to trim conversation history before sending each request (`internal/agent/context.go`).

---

## 7. Tool Use / Function Calling

llama.cpp supports the OpenAI tool-calling format (`tools` array in the request), but results vary by model. Models explicitly fine-tuned for tool/function calling (e.g., Llama 3.1 Instruct, Mistral Instruct, Hermes variants) work best.

If the model does not reliably emit tool calls, consider restricting which tools are available to reduce noise:

```toml
[[agents]]
id    = "local"
tools = ["search", "fetch"]   # only expose these tools to this agent
```

Leave `tools` empty (or omit it) to allow all built-in tools.

---

## 8. Cost Limits

The built-in cost tracking (`internal/agent/tokenlimit.go`) estimates spend based on token counts and per-model pricing. For local models there is no real monetary cost, so:

- Either omit `[[agents.cost_limits]]` entirely (no spend limits applied).
- Or explicitly zero out pricing (shown in §3) so any spend limits you do set are never triggered.

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `llm returned 400` or `context length exceeded` | `max_context` in Smithly exceeds `--ctx-size` on the server | Lower `max_context` to match the server's `--ctx-size` |
| `llm request: dial tcp ...: connection refused` | Server not running or wrong address | Confirm `llama-server` is running; check `base_url` |
| `llm returned 401` | Server started with `--api-key` | Add `api_key = "<secret>"` to the agent config |
| `llm returned no choices` | Model returned an empty response | Check server logs; model may have run out of context |
| Tool calls never execute | Model doesn't support function calling | Try a model fine-tuned for tool use, or restrict `tools = []` |
| Slow responses | Too few GPU layers offloaded | Increase `--n-gpu-layers`; check GPU VRAM |
| Agent visible on unwanted machines | Server bound to `0.0.0.0` | Use a firewall rule, or add `--api-key` to the server |
