# AgyPTC (`agy-ptc`)

**Programmatic Tool Calling (Code Mode) Runtime & Plugin for Antigravity CLI (`agy`)**

Single static Go binary (`CGO_ENABLED=0`) · Embedded `esbuild` + `goja` TypeScript sandbox · Atomic `tx` rollback · External MCP bridge · Zero `output.txt` spills

---

## Quick Start

### Build & Install into Antigravity CLI

```bash
go build -o agy-ptc ./cmd/agy-ptc      # Windows: go build -o agy-ptc.exe ./cmd/agy-ptc
./agy-ptc install                      # Copies plugin to ~/.gemini/config/plugins/agy-ptc, enables via agy, runs live doctor
```

### Modes of Operation

1. **Hybrid Mode (Default)**: Enabled automatically across sessions once installed (`agy plugin list`). Small single-file reads stay on allowlisted native `read_file`; multi-step search, AST outlining, multi-file transactional edits, and concurrent checks run in one turn via `mcp_agy-ptc_ptc_exec`.
2. **Strict Code Mode (`pi-fabric` style)**: Launch `agy --agent ptc` (or switch to `ptc` in `/agents`) to run with a focused tool allowlist centered on `mcp_agy-ptc_ptc_exec`.

### Optional Live TUI Status Bar (`~/.gemini/antigravity-cli/settings.json`)

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.gemini/config/plugins/agy-ptc/scripts/agy-ptc statusline",
    "stack_with_default": true
  }
}
```

### CLI Commands

```bash
agy-ptc doctor                         # Live MCP + SessionStart + strict agy plugin validate check
agy-ptc run --code "return await git.status()"
agy-ptc bench --recent 15              # Analyze local Antigravity transcript.jsonl efficiency
```
