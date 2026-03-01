# Smithly

Autonomous agent controller. Agents run LLM loops with tool-use, code skill authoring, sandboxed execution, and network gating.

**Status: in progress.** Core phases 1-6.5 are complete (agent loop, multi-agent, skills, sidecar API, network gatekeeper, sandbox providers, agent-authored skills). Memory, channels, and content firewall are ahead. See `backlog.md` for the full roadmap.

Supports OpenAI (Chat Completions + Responses API), Anthropic, Gemini, Ollama, and OpenRouter. Codex models (`gpt-5.x-codex`) are auto-detected and routed through the Responses API. See [INSTALL.md](INSTALL.md) for setup, testing, and the full model list.

## License

MIT — see [LICENSE](LICENSE) for details.
