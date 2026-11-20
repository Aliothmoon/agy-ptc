# AgyPTC — Programmatic Tool Calling (Code Mode) Rules

Use **`mcp_agy-ptc_ptc_exec`** (eagerly loaded native tool) to collapse multi-step search, AST inspection, data-dependent branching/loops, atomic multi-file edits, and fast verification into **ONE TypeScript program turn**.

## When to Use `mcp_agy-ptc_ptc_exec` vs. Built-in Tools

| Scenario | Preferred Tool | Why |
| :--- | :--- | :--- |
| Read 1–3 known small files (`<300` lines) or known line slices | Built-in `read_file` (in parallel) | Locally allowlisted (~7ms, 46 KB/call inline) |
| **Any `>=2`-step or data-dependent chain** (search -> outline -> read -> filter -> edit -> fast verify), loops over files, or **`<15s` fast checks** (`go vet`, `tsc --noEmit`, lint, scoped unit tests) | **`mcp_agy-ptc_ptc_exec`** (`{ cwd, code, payloads?, title?, timeoutMs?, maxOutputBytes? }`) | Runs locally in `<5ms` isolated TS VM (`20s` default / `120s` max timeout, process-tree kill); intermediate data stays in sandbox memory |
| Multi-file or multi-hunk refactoring that should roll back if compile/unit checks fail | **`mcp_agy-ptc_ptc_exec` with `tx.begin()`** | Snapshots originals in memory, runs concurrent `<15s` `verify`, and auto-rolls back on failure |
| **Long-running tasks (`>15s`)**, full integration/E2E test suites, heavy builds (Gradle/Cargo/Docker), interactive commands, or **daemons (`IsDaemon: true`)** | Built-in **`run_command`** (`WaitMsBeforeAsync: 500..2000`) + **`manage_task`** | Detaches into `/tasks` with live streaming stdout, `send_input`/`kill` control, and automatic Reactive Wakeup without blocking the turn |
| Macro multi-agent fan-out across independent subsystems | Built-in `run_workflow` or `swarm` (instruct workers in `brief` to use `mcp_agy-ptc_ptc_exec`) | Parallel LLM reasoning + PTC inside each worker |

## Sandbox API Quick Reference (`code` is an async TypeScript function body)

- **Paths**: Always pass workspace root in `cwd`. Relative paths resolve against `cwd`; **external absolute paths or `../` cross-repo paths** (`D:/OtherRepo/...`, `~/...`) are also supported across `fs.*`, `search.*`, `ast.*`, `edit.*`, `tx.*`, `git.*` (`{ cwd }`), and `sh.*` (`{ cwd }`).
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
  - `const t = await tx.begin(); await t.edit(path, hunks); await t.write(path, content); const v = await t.verify(["go vet ./...", "go test ./pkg/..."]); if (!v.ok) { await t.rollback(); return v.failures; } await t.commit();` (uncommitted `tx` auto-rolls back on error!)
- **`git` & `sh`**:
  - `await git.status(repoDir?)` -> `{ isRepo, branch, clean, staged, modified, untracked }` · `await git.diff(argsOrOpts?)` · `await git.log(argsOrOpts?)` · `await git.blame(path, { cwd?, startLine?, endLine? })`
  - `await sh.exec(cmd, { cwd?, timeoutSec?, timeoutMs?, compact?: true })` -> `{ ok, exitCode, timedOut, output, durationMs }` (`settle: true`, default `15s` timeout + process-tree kill)
  - `await sh.checks(["go vet ./...", "go test ./pkg/..."], { concurrency?, timeoutSec?, compact?: true })` -> `{ ok, summary, results, failures? }`
- **`state` & External `mcp` / `tools`**:
  - `state.set(key, val)` / `state.get(key)` / `state.keys()` — persists across turns; oversized `return` values auto-save to `state.get("__last_result")`.
  - `await tools.search("query")` -> `await tools.describe(ref)` -> `await mcp.<server>.<tool>(args)` (or `await mcp.call("server/tool", args)`).

## Hygiene

1. **Fast PTC + Async `/tasks` Relay**: When making code changes followed by a heavy `>15s` test/build suite, run edits + fast compile/vet inside `mcp_agy-ptc_ptc_exec` first, then launch the heavy suite via built-in `run_command(WaitMsBeforeAsync: 500..2000)` so `/tasks` streams live logs and wakes you when done. Never use `run_command` for `rg`/`grep`/`find`/`git status` or `<15s` checks.
2. **Return compact summaries (`<= 9.5 KB`)**: Filter, count, and project inside TypeScript; return concise structured objects (`{ updatedFiles, testSummary, findings }`), or pass `maxOutputBytes` only when larger raw text is explicitly needed.
3. **Parallelize independent calls**: Use `await Promise.all([search.rg(...), git.status()])` inside `code`.
4. **Consult `/ptc-exec` on edge cases**: Load the `ptc-exec` skill only if you hit an unfamiliar capability error or complex external MCP bridge workflow.
