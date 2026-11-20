# AgyPTC — Programmatic Tool Calling (Code Mode) Rules

Use **`mcp_agy-ptc_ptc_exec`** (eagerly loaded native tool) to collapse multi-step search, AST inspection, data-dependent branching/loops, atomic multi-file edits, and test verification into **ONE TypeScript program turn**.

## When to Use `mcp_agy-ptc_ptc_exec` vs. Built-in Tools

| Scenario | Preferred Tool | Why |
| :--- | :--- | :--- |
| Read 1–3 known small files (`<300` lines) or known line slices | Built-in `read_file` (in parallel) | Locally allowlisted (~7ms, 46 KB/call inline) |
| **Any `>=2`-step or data-dependent chain** (search -> outline -> read -> filter -> edit -> verify), loops over files, or `>=2` shell checks | **`mcp_agy-ptc_ptc_exec`** (`{ code, payloads?, cwd?, title? }`) | Runs locally in `<5ms` isolated TS VM; intermediate data stays in sandbox memory (0 context bloat, 0 `output.txt` spills) |
| Multi-file or multi-hunk refactoring that should roll back if build/tests fail | **`mcp_agy-ptc_ptc_exec` with `tx.begin()`** | Snapshots originals in memory, runs concurrent `verify`, and auto-rolls back on test/compile failure |
| Macro multi-agent fan-out across independent subsystems | Built-in `run_workflow` or `swarm` (instruct workers in `brief` to use `mcp_agy-ptc_ptc_exec`) | Parallel LLM reasoning + PTC inside each worker |

## Sandbox API Quick Reference (`code` is an async TypeScript function body)

- **Payloads (`P` / `payloads`)**: Put multiline replacement code or templates in top-level `payloads: { newFunc: "..." }` and reference `P.newFunc` inside `code` (avoids nested JSON string escaping).
- **`search`**:
  - `await search.rg(pattern, { path?, glob?, literal?, ignoreCase?, contextLines?, maxResults? })` -> `Array<{ file, line, column, text, match }>`
  - `await search.glob(pattern, { path?, maxResults? })` -> `string[]` (use `*` and `{a,b}`, not regex `|`)
- **`ast`**:
  - `await ast.outline(path)` / `await ast.outlines(paths)` -> `{ file, totalLines, symbols: [{ kind, name, signature?, startLine, endLine }] }` (exact `go/parser` AST for Go; TS/JS/Py/Rust/Java/MD supported)
  - `await ast.enclosingSymbol(path, line)` -> `{ kind, name, startLine, endLine } | null`
- **`fs`**:
  - `await fs.read(path, { startLine?, endLine?, lineNumbers? })` -> `string` (up to 2 MB inside sandbox memory)
  - `await fs.write(path, content)` · `await fs.exists(path)` · `await fs.stat(path)` · `await fs.list(path)` · `await fs.tree(path, depth?)`
- **`edit` & `tx` (Atomic Transactions)**:
  - `await edit.apply({ edits: [{ path, hunks: [{ target, replacement, startLine?, endLine?, replaceAll? }], create?, overwrite? }], dryRun?, bestEffort?, verify? })`
  - `const t = await tx.begin(); await t.edit(path, hunks); await t.write(path, content); const v = await t.verify(["go vet ./...", "go test ./..."]); if (!v.ok) { await t.rollback(); return v.failures; } await t.commit();` (uncommitted `tx` auto-rolls back on error!)
- **`git` & `sh`**:
  - `await git.status()` -> `{ branch, clean, staged, modified, untracked }` · `await git.diff(args?)` · `await git.log(args?)` · `await git.blame(path, { startLine?, endLine? })`
  - `await sh.exec(cmd, { cwd?, timeoutSec?, settle?: true, compact?: true })` -> `{ ok, exitCode, output, durationMs }`
  - `await sh.checks(["go vet ./...", "go test ./..."], { concurrency?, compact?: true })` -> `{ ok, summary, results, failures? }`
- **`state` & External `mcp` / `tools`**:
  - `state.set(key, val)` / `state.get(key)` / `state.keys()` — persists across turns; oversized `return` values auto-save to `state.get("__last_result")`.
  - `await tools.search("query")` -> `await tools.describe(ref)` -> `await mcp.<server>.<tool>(args)` (or `await mcp.call("server/tool", args)`).

## Hygiene

1. **Return compact summaries (`<= 8.5 KB`)**: Filter, count, and project inside TypeScript; return concise structured objects (`{ updatedFiles, testSummary, findings }`), never raw whole-file dumps.
2. **Parallelize independent calls**: Use `await Promise.all([search.rg(...), git.status()])` inside `code`.
3. **Consult `/ptc-exec` on edge cases**: Load the `ptc-exec` skill only if you hit an unfamiliar capability error or complex external MCP bridge workflow.
