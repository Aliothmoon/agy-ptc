---
name: ptc-exec
description: >-
  [DEFAULT RECOMMENDED: Prefer PTC Code Mode via mcp_agy-ptc_ptc_exec] Default to
  using PTC Mode (`mcp_agy-ptc_ptc_exec`) instead of sequential `run_command` or
  split `edit_file` calls whenever performing codebase search, AST symbol analysis,
  multi-step file reads/edits, build/test checks, or external MCP orchestration.
  Provides the TypeScript sandbox API reference, transactional patterns, and error recovery guide.
---

# `mcp_agy-ptc_ptc_exec` — Default PTC Code Mode Guide & API Reference

> [!IMPORTANT]
> **Default Recommendation — Always Prefer PTC Mode (`mcp_agy-ptc_ptc_exec`)**:
> By default, collapse multi-step exploration (`search.rg` / `search.glob` / `ast.outlines` / `fs.read`), data-dependent filtering, multi-hunk/multi-file edits (`edit.apply` / `tx.begin`), and concurrent verification (`sh.checks` / `tx.verify`) into **one `mcp_agy-ptc_ptc_exec` TypeScript program** rather than issuing serial shell or single-hunk edit turns. For pure single-tool Code Mode sessions, recommend `agy --agent ptc` (or `/agents` -> `ptc`).

## 1. Execution Model

- `code` is compiled with `esbuild` (`TypeScript -> ES2020`) before any host operation runs. Syntax errors or missing `P.<key>` references fail preflight with **0 side effects** and exact 1-based line numbers.
- Top-level `await` and `return` work directly. Do **not** wrap `code` in `async function main() {}`.
- `import type { ... } from "..."` (and single-line `node:buffer`/`node:path`/`node:crypto`/`node:url` imports) are handled automatically; built-in globals `Buffer`, `TextEncoder`/`TextDecoder`, `btoa`/`atob`, `path`, `crypto`, and `URL`/`URLSearchParams` are available out-of-the-box alongside host capabilities (`fs`, `search`, `ast`, `edit`, `tx`, `git`, `sh`, `state`, `tools`, `mcp`).
- Read-only `git.*` probes and `sh.exec` (`settle: true`, `15s` default timeout) return structured `{ ok: false, timedOut, ... }` / diagnostic strings instead of crashing `Promise.all`.
- **Timeouts & Process-Tree Protection**: `mcp_agy-ptc_ptc_exec` enforces a **`20s` default deadline** (`timeoutMs: 20000`, configurable `500..120000`) and automatically terminates entire child process trees + closes inherited pipes on timeout so runaway commands never hang.
- **Fast Checks vs. Long Background Tasks**: Run `<15s` checks (`go vet`, `tsc --noEmit`, lint, package-scoped unit tests) inside `sh.checks` / `tx.verify`. For `>15s` full test suites, heavy builds, or `IsDaemon: true` servers, hand off to native `run_command(WaitMsBeforeAsync: 500..2000)` + `manage_task` so `/tasks` streams live logs and wakes the session automatically.

## 2. Transactional Refactoring (`tx`)

Uncommitted transactions **automatically roll back** if your script throws an exception or times out.

```ts
const t = await tx.begin();
await t.edit("internal/service/handler.go", [
  {
    startLine: 40,
    endLine: 75,
    target: P.before,
    replacement: P.after,
  },
]);

const check = await t.verify([
  { name: "vet", command: "go vet ./..." },
  { name: "unit", command: "go test ./internal/service/..." },
]);

if (!check.ok) {
  await t.rollback();
  return { ok: false, failures: check.failures };
}

await t.commit();
return { ok: true, summary: check.summary };
```

## 3. Bridging External MCP Servers (`tools.*` & `mcp.*`)

Discover cached schemas at zero startup cost, then invoke external MCP servers inside the same script:

```ts
const hits = await tools.search("semantic search");
if (hits.length > 0) {
  const schema = await tools.describe(hits[0].ref);
  // First-class proxy syntax (hyphens mapped to underscores automatically):
  const res = await mcp.zvec_grep.zvec_grep_search({ query: "authentication middleware" });
  return { ref: schema.ref, preview: res.text.slice(0, 600) };
}
```

## 4. Handling Large Datasets Across Turns (`state.*`)

If a result exceeds `9,600` UTF-8 bytes (configurable via `maxOutputBytes`), `AgyPTC` automatically switches to compact JSON first and saves oversized objects in `state.get("__last_result")` (and a numbered `state.get("__result_N")` key) instead of spilling to `output.txt`. Filter or page it in the next turn:

```ts
const prev = state.get("__last_result") as Array<{ file: string; line: number }>;
return prev.slice(0, 25);
```
