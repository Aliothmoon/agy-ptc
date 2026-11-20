# PTC Code Mode (`agy-ptc`)

Programmatic Tool Calling (Code Mode) plugin for **Antigravity CLI (`agy`)**.

Exposes an eager TypeScript sandbox tool (`mcp_agy-ptc_ptc_exec`) powered by a single static Go binary (`esbuild` + `goja`), enabling agents to compose ripgrep search, exact Go AST & multi-language symbol outlines, multi-file atomic transactions (`tx.begin -> edit -> verify -> commit / auto-rollback`), fast `<15s` shell checks, cross-turn session state, and external MCP calls in a single LLM turn.

See the root [README.md](../README.md) ([简体中文](../README_ZH.md)) for full installation, configuration, and API documentation.
