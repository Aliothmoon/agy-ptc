---
name: ptc-exec
description: >-
  [DEFAULT RECOMMENDED: Prefer PTC Code Mode via mcp_agy-ptc_vm_exec] Default to
  using PTC Mode (`mcp_agy-ptc_vm_exec`) instead of sequential `run_command` or
  split `edit_file` calls whenever performing codebase search, AST symbol analysis,
  multi-step file reads/edits, build/test checks, or external MCP orchestration.
  Provides the TypeScript sandbox API reference, edit & check patterns, and error recovery guide.
---

# `mcp_agy-ptc_vm_exec` — Default PTC Code Mode Guide & API Reference

> [!IMPORTANT]
> **Default Recommendation — Always Prefer PTC Mode (`mcp_agy-ptc_vm_exec`)**:
> By default, collapse multi-step exploration (`search.rg` / `search.glob` / `ast.outlines` / `fs.read`), data-dependent filtering, multi-hunk/multi-file edits (`edit.apply`), and concurrent verification (`sh.checks`) into **one `mcp_agy-ptc_vm_exec` TypeScript program** rather than issuing serial shell or single-hunk edit turns. For pure single-tool Code Mode sessions, recommend `agy --agent ptc` (or `/agents` -> `ptc`).

## 1. Execution Model

- `code` is compiled with `esbuild` (`TypeScript -> ES2020`) before any host operation runs. Syntax errors or missing `P.<key>` references fail preflight with **0 side effects** and exact 1-based line numbers.
- Top-level `await`, `print()` / `console.log()` stdout, and `return` work directly. Do **not** wrap `code` in `async function main() {}`. Calling `print()` without `return` streams raw unescaped stdout directly under the full inline budget (auto-spilling to `state.get("__last_result")` if oversized) — ideal for multiline code, diffs, and log summaries.
- `import type { ... } from "..."` (and single-line `node:buffer`/`node:path`/`node:crypto`/`node:url` imports) are handled automatically; built-in globals `Buffer`, `TextEncoder`/`TextDecoder`, `btoa`/`atob`, `path`, `crypto`, and `URL`/`URLSearchParams` are available out-of-the-box alongside host capabilities (`fs`, `search`, `ast`, `edit`, `tx`, `git`, `sh`, `state`, `tools`, `mcp`).
- Read-only `git.*` probes and `sh.exec` (`settle: true`, `15s` default timeout) return structured `{ ok: false, timedOut, ... }` / diagnostic strings instead of crashing `Promise.all`.
- **Timeouts & Process-Tree Protection**: `mcp_agy-ptc_vm_exec` enforces a **`20s` default deadline** (`timeoutMs: 20000`, configurable `500..120000`) and automatically terminates entire child process trees + closes inherited pipes on timeout so runaway commands never hang.
- **Fast Checks vs. Long Background Tasks**: Run `<15s` checks (`go vet`, `tsc --noEmit`, lint, package-scoped unit tests) inside `sh.checks` / `tx.verify`. For `>15s` full test suites, heavy builds, or `IsDaemon: true` servers, hand off to native `run_command(WaitMsBeforeAsync: 500..2000)` + `manage_task` so `/tasks` streams live logs and wakes the session automatically.

## 2. Low-Token Edits (`edit`) & Opt-In Rollback (`tx`)

Always prefer **low-token edit modes** instead of copying 20–50 lines of old code into `target`:

- **Whole function / method / struct rewrite (`0` `target` tokens — recommended)**:
  `await edit.replaceSymbol(path, "Type::method", P.newMethod)` (or `{ symbol: "Type::method", replaceSymbol: true, replacement: P.newMethod }` inside `edit.apply`). Automatically preserves attached doc comments, `@decorator` / `#[attr]`, and `template<...>` headers unless `P.newMethod` includes them.
- **Add a new function, method, or prototype (`0` `target` tokens — no copying neighbor functions!)**:
  `await edit.insertSymbol(path, "Type::fn", "after" | "before" | "inside", P.newFn)` (or `{ symbol, insert: "after" | "before" | "inside", replacement: P.newFn }` inside `edit.apply`; `"inside"` appends a member at the end of a `class` / `struct` / `impl` / `namespace` with automatic member indentation; `"before"` inserts above any attached doc comments/attributes). To append at EOF without reading the file tail first, use `await edit.append(path, P.newFn)` or `{ path, append: P.newFn }`.
- **Interior block rewrite (`target` + `endTarget` — saves 85%+ `target` tokens)**:
  `{ symbol: "Handle", target: "// phase 2: validate", endTarget: "return nil\n\t}", replacement: P.newBlock }` replaces the closed span `[target .. endTarget]` without echoing middle lines (include the preceding statement in `endTarget` rather than a bare `"}"`).
- **1–5 line surgical tweak**:
  `{ symbol: "Handle", target: "timeout := 5", replacement: "timeout := 15" }`.

```ts
await edit.apply({
  edits: [
    {
      path: "internal/service/handler.go",
      hunks: [
        { symbol: "Handler.ServeHTTP", replaceSymbol: true, replacement: P.newServeHTTP },
        { symbol: "Handler.validate", insert: "after", replacement: P.newHelperMethod },
        { symbol: "Handler.validate", target: "// check headers", endTarget: "return ok", replacement: P.newHeaderCheck },
      ],
    },
    // New files can be created atomically in the same batch:
    { path: "internal/service/metrics.go", create: P.metricsGo, overwrite: true },
  ],
}); // Every hunk in every file is validated before any write: one bad hunk throws and writes nothing

const check = await sh.checks([
  { name: "vet", command: "go vet ./..." },
  { name: "unit", command: "go test ./internal/service/...", cwd: "." },
]); // Also accepts string[] and top-level { cwd, concurrency, timeoutSec, compact }

return check.ok ? { ok: true, summary: check.summary } : { ok: false, failures: check.failures };
```

- **If a check fails, keep the edits**: they are already on disk, so next turn send only the hunks that fix `check.failures` — never resend the whole payload. A failed check is often the command's fault (timeout, missing env), not the edit's.
- **Large payloads**: `edit.apply({ edits, bestEffort: true })` writes the files whose hunks all match and returns the rest in `failures: [{ path, error }]` — resend only those files.
- **`tx` (opt-in, all-or-nothing)**: use `const t = await tx.begin()` -> `t.edit([...])` / `t.write` / `t.rm` / `t.rename` -> `t.verify([...])` -> `t.commit()` or `t.rollback()` only when a failed check must leave the tree untouched (e.g. a throwaway experiment). Rollback discards every edit, so a retry resends them all; an uncommitted `tx` also auto-rolls back when the script throws, times out, or ends.

## 3. Bridging External MCP Servers (`tools.*` & `mcp.*`)

Discover cached schemas at zero startup cost, then invoke stdio or static-header external MCP servers inside the same script (for remote OAuth HTTP MCP servers like `sentry` whose OAuth tokens are managed privately by `agy` CLI rather than `mcp_config.json`, call native `call_mcp_tool` directly — no re-login needed):

```ts
const hits = await tools.search("semantic search");
if (hits.length > 0) {
  const schema = await tools.describe(hits[0].ref);
  // First-class proxy syntax (hyphens mapped to underscores automatically):
  // Returns `{ text, data? }` where `res.data` holds auto-parsed JSON or `structuredContent` when the tool outputs JSON.
  const res = await mcp.zvec_grep.zvec_grep_search({ query: "authentication middleware" });
  return { ref: schema.ref, data: res.data ?? res.text.slice(0, 600) };
}
```

## 4. Choosing `print()` vs `return` & Handling Large Datasets (`state.*`)

- **`print(...)` / `console.log(...)` without `return`**: Streams raw unescaped stdout directly under the full inline budget (`<= 9,600 B`, no `Logs:`/`Result: null` wrapper, no JSON `\n`/`\"`/`\\` escaping). Best for multiline code, diffs, tables, and log reports.
- **`return` a `string`**: Outputs raw unescaped text under `Result:`.
- **`return { ... }`**: Outputs compact JSON; if you both `print()` and `return`, the inline budget is shared dynamically between stdout and `Result:`.
- **Auto-Spill to `state`**: If either `print()` stdout or a `return` value exceeds `9,600` UTF-8 bytes, `AgyPTC` saves the intact payload in memory under `state.get("__result_N")` (named on line 1, with `"__last_result"` aliasing the latest spill) instead of spilling to `output.txt`. Long text is shown page by page; continue with `print(state.page('__result_N', 2))` (`-1` = last page; key-bound so concurrent calls never mix). Objects come with their `Shape`; slice them in the next turn:

```ts
const prev = state.get("__last_result") as Array<{ file: string; line: number }>;
return prev.slice(0, 25);
```
