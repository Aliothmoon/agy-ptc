# AgyPTC (`agy-ptc`)

**Programmatic Tool Calling (Code Mode)** runtime for **Google Antigravity** (App, CLI, and IDE).

Instead of issuing dozens of sequential search, read, edit, and check tool calls across multiple turns, `AgyPTC` exposes a single eager TypeScript sandbox (`mcp_agy-ptc_vm_exec`) powered by a static Go engine (`esbuild` + `goja` + `gotreesitter`). Agents compose search, AST symbol inspection, multi-file atomic edits, fast verification checks, cross-turn state, and external MCP orchestration in **one LLM turn** while keeping intermediate data inside sandbox memory.

---

## Zero-Setup Installation

Installing **AgyPTC** from the Antigravity Marketplace works out of the box on **Windows, macOS, and Linux** (`x64` & `arm64`) — no Go toolchain or manual CLI setup required. On first launch, the bundled Node.js supervisor automatically downloads and SHA-256 verifies the matching release binary into `${PLUGIN_DATA}/bin/`.

```bash
# Install via Antigravity CLI (once published to Partner Marketplace)
agy plugin install agy-ptc@antigravity-plugins-partners

# Or install directly from GitHub
agy plugin install https://github.com/Aliothmoon/agy-ptc/tree/main/plugin
```

---

## What's Bundled

| Component | Name | Role |
| :--- | :--- | :--- |
| **MCP Tool (Eager)** | `mcp_agy-ptc_vm_exec` | Isolated `<5ms` TypeScript VM executing multi-step codebase operations in one turn |
| **Skills** | `/ptc-exec`, `/ptc-refactor` | Sandbox API reference, error recovery patterns, and atomic multi-file refactor + verify workflows |
| **Subagent** | `ptc` | Dedicated Code Mode agent for codebase exploration, AST analysis, multi-file edits, and fast checks |
| **Rule** | `rules/AGENTS.md` | Routing heuristics between `mcp_agy-ptc_vm_exec`, `read_file`, and async `run_command` tasks |
| **Hook** | `SessionStart` | Injects a compact workspace snapshot on session startup |
| **Sidecar UI** | `AgyPTC` (`PTC Panel`) | Live execution monitor, op-level trace inspector, error breakdown, and health check panel |

---

## Sandbox Capabilities at a Glance

- **`search`**: `search.rg(pattern, { path?, glob?, literal?, contextLines?, filesOnly?, count? })`, `search.glob(pattern, { path?, detail? })`
- **`ast`**: Exact Go `go/parser` + Tree-sitter symbol outlines (`ast.outline`, `ast.outlines`, `ast.read(path, "Type::method")`, `ast.enclosingSymbol`) across Go, C/C++, Rust, Python, TypeScript/JS, Java, Kotlin, C#, and Markdown
- **`fs`**: `fs.read` (supports line ranges, `tail`, `.gz`, and UTF-16 BOM logs), `fs.cat`, `fs.write`, `fs.bytes`, `fs.hash`, `fs.stat`, `fs.list`, `fs.tree`
- **`edit` & `tx`**: Low-token symbol & anchor edits (`edit.apply`, `edit.replaceSymbol`, `edit.insertSymbol`, `edit.replace`) plus opt-in transactional rollback (`tx.begin() -> edit -> verify -> commit`)
- **`git` & `sh`**: Structured read-only `git.*` (`status`, `diff`, `log`, `show`, `blame`, `query` incl. `grep`) and process-tree-managed `<15s` checks (`sh.exec`, `sh.run`, `sh.checks`)
- **`state` & `mcp`**: Cross-turn in-memory KV store with automatic output paging (`state.page`) and external MCP client bridging (`tools.search`, `mcp.<server>.<tool>`)

---

## Security & Guardrails

- **Workspace confinement**: Writes outside the active workspace and reads/writes to sensitive credential paths (`~/.ssh`, `~/.aws`, cloud tokens, etc.) are blocked by policy.
- **Explicit escalation (`NeedsApproval`)**: Destructive commands (`rm -rf`, `git push`, `git reset --hard`, `git clean -f`) and agent/MCP configuration edits are never executed inside the sandbox; they surface `NeedsApproval` so native tools can prompt for user confirmation.
- **Process-tree & daemon safety**: Timed-out commands terminate child process trees cleanly while preserving shared host build/device daemons (such as `adb`, `sccache`, and `mspdbsrv`).

---

## Links & Documentation

- **Repository & Full Documentation**: [https://github.com/Aliothmoon/agy-ptc](https://github.com/Aliothmoon/agy-ptc) ([简体中文文档](https://github.com/Aliothmoon/agy-ptc/blob/main/README_ZH.md))
- **Core Engine (`MCPlex`)**: [https://github.com/Aliothmoon/mcplex](https://github.com/Aliothmoon/mcplex)
- **Releases & Checksums**: [https://github.com/Aliothmoon/agy-ptc/releases](https://github.com/Aliothmoon/agy-ptc/releases)
- **License**: MIT
