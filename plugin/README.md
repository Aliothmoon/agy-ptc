# PTC Code Mode (`agy-ptc`)

Programmatic Tool Calling (Code Mode) plugin for Antigravity CLI (`agy`). Exposes a single eager flat tool (`mcp_agy-ptc_ptc_exec`) backed by an isolated TypeScript sandbox (`esbuild` + `goja`), atomic file transactions (`tx.begin/edit/verify/commit/rollback`), exact Go AST & multi-language symbol outlines, session state, and external MCP bridging.
