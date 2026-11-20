# AgyPTC — Programmatic Tool Calling (Code Mode) Rules

Use **`mcp_agy-ptc_vm_exec`** (eagerly loaded native tool) to collapse multi-step search, AST inspection, data-dependent branching/loops, atomic multi-file edits, and fast verification into **ONE TypeScript program turn**.

## When to Use `mcp_agy-ptc_vm_exec` vs. Built-in Tools

| Scenario | Preferred Tool | Why |
| :--- | :--- | :--- |
| Read 1–3 known small files (`<300` lines) or known line slices | Built-in `read_file` (in parallel) | Locally allowlisted (~7ms, 46 KB/call inline) |
| **Any `>=2`-step or data-dependent chain** (search -> outline -> read -> filter -> edit -> fast verify), loops over files, or **`<15s` fast checks** (`go vet`, `tsc --noEmit`, lint, scoped unit tests) | **`mcp_agy-ptc_vm_exec`** (`{ cwd, code, payloads?, title?, timeoutMs?, maxOutputBytes? }`) | Runs locally in `<5ms` isolated TS VM (`20s` default / `120s` max timeout, process-tree kill); intermediate data stays in sandbox memory |
| Multi-file or multi-hunk refactoring that should roll back if compile/unit checks fail | **`mcp_agy-ptc_vm_exec` with `tx.begin()`** | Snapshots originals in memory, runs concurrent `<15s` `verify`, and auto-rolls back on failure |
| **Long-running tasks (`>15s`)**, full integration/E2E test suites, heavy builds (Gradle/Cargo/Docker), interactive commands, or **daemons (`IsDaemon: true`)** | Built-in **`run_command`** (`WaitMsBeforeAsync: 500..2000`) + **`manage_task`** | Detaches into `/tasks` with live streaming stdout, `send_input`/`kill` control, and automatic Reactive Wakeup without blocking the turn |
| Macro multi-agent fan-out across independent subsystems | Built-in `run_workflow` or `swarm` (instruct workers in `brief` to use `mcp_agy-ptc_vm_exec`) | Parallel LLM reasoning + PTC inside each worker |

## Sandbox API Quick Reference (`code` is an async TypeScript function body)

- **Paths**: Always pass workspace root in `cwd`. Relative paths resolve against `cwd`; **external absolute paths or `../` cross-repo paths** (`D:/OtherRepo/...`, `~/...`) are also supported across `fs.*`, `search.*`, `ast.*`, `edit.*`, `tx.*`, `git.*` (`{ cwd }`), and `sh.*` (`{ cwd }`).
- **Payloads (`P` / `payloads`)**: Put multiline replacement code or templates in top-level `payloads: { newFunc: "..." }` and reference `P.newFunc` inside `code` (avoids nested JSON string escaping).
- **`search`**:
  - `await search.rg(pattern, { path?, glob?, literal?, ignoreCase?, contextLines?, maxResults? })` -> `Array<{ file, line, column, text, match, before?, after? }>`; `{ filesOnly: true }` -> `string[]`, `{ count: true }` -> `{ [file]: n }`
  - `await search.glob(pattern, { path?, maxResults? })` -> `string[]` (use `*` and `{a,b}`, not regex `|`)
- **`ast`**:
  - `await ast.outline(path)` / `await ast.outlines(paths)` -> `{ file, totalLines, symbols: [{ kind, name, signature?, startLine, endLine }] }` (exact `go/parser` AST for Go; TS/JS/Py/Rust/C/C++/Kotlin/Java/MD supported)
  - `await ast.read(path, "Type::method")` -> `{ kind, name, startLine, endLine, text }` (also `Type.method`, bare `name`)
  - `await ast.enclosingSymbol(path, line)` -> `{ kind, name, startLine, endLine } | null`
- **`fs`**:
  - `await fs.read(path, { startLine?, endLine?, tail?, lineNumbers? })` -> `string` (up to 2 MB; `tail: N` = last N lines of any size file)
  - `await fs.cat(paths, opts?)` -> `{ [path]: text | null }` (parallel multi-read; unreadable -> `null` + Warning)
  - `await fs.write(path, content, { eol?: "lf" | "crlf" })` (default keeps the file's style; new `.bat`/`.cmd` -> CRLF) · `fs.exists(path)` (sync boolean) · `await fs.bytes(path, { offset?, length? })` -> `Buffer` (negative offset = from end) · `await fs.stat(path)` · `await fs.list(path)` · `await fs.tree(path, depth?)`
  - `await fs.mkdir(path)` (-p) · `await fs.rm(path, { force? })` (file/empty dir) · `await fs.rename(from, to)` (`mv`) · `await fs.cp(from, to, { recursive? })` — immediate, not transactional
- **`edit` & `tx` (Atomic Transactions)**:
  - `await edit.apply({ edits: [{ path, hunks: [{ target, replacement, symbol?, nth?, startLine?, endLine?, replaceAll? }], create?, overwrite? }], dryRun?, bestEffort?, verify? })` (`symbol: "Type::method"` limits matching to that symbol; `nth`: Nth match; `dryRun` -> `.diff`)
  - `const t = await tx.begin(); await t.edit(path, hunks); await t.write(path, content); const v = await t.verify(["go vet ./...", "go test ./pkg/..."]); if (!v.ok) { await t.rollback(); return v.failures; } await t.commit();` (uncommitted `tx` auto-rolls back; also `t.rm(path)`, `t.rename(from, to)`, `await t.diff()`; pass all files to one `t.edit([{ path, hunks }, ...])` so every hunk is validated before anything is written)
- **`git` & `sh`**:
  - `await git.status(repoDir?)` -> `{ isRepo, branch, clean, staged, modified, untracked }` · `await git.diff({ staged?, stat?, nameOnly?, ref?, path? })` · `await git.log({ n?, grep?, since?, author?, format?, path? })` (string args also work) · `await git.blame(path, { cwd?, startLine?, endLine? })`
  - `await sh.exec(cmd, { cwd?, env?, shell?: "pwsh" | "cmd" | "bash" | "sh", timeoutMs?, compact?: true })` -> `{ ok, exitCode, timedOut, output, durationMs }` (`settle: true`, default `15s` timeout + process-tree kill; `sh.shell` = default dialect)
  - `await sh.run(file, args?, { cwd?, env?, stdin?, timeoutMs? })` -> `{ ok, exitCode, timedOut, stdout, stderr, durationMs }` (no shell: no quoting issues; use for parseable output)
  - `await sh.checks(["go vet ./...", "go test ./pkg/..."], { concurrency?, timeoutSec?, compact?: true })` -> `{ ok, summary, results, failures? }`
- **Standard Utility Globals**: `Buffer` (`utf8`/`base64`/`base64url`/`hex`/`utf16le`), `btoa`/`atob`, `TextEncoder`/`TextDecoder`, `path` (`join`/`resolve`/`dirname`/`basename`/`extname`), `crypto` (`randomUUID`/`createHash`), `URL`/`URLSearchParams`, `setTimeout`/`clearTimeout`, `structuredClone`, read-only `process` (`platform`, `arch`, `env` with secret-looking values redacted, `cwd()`) and `os` (`homedir()`, `tmpdir()`, `EOL`) are available globally without `import`.
- **`state` & External `mcp` / `tools`**:
  - `state.set(key, val)` / `state.get(key)` / `state.keys()` — persists across turns; oversized `return` values auto-save to `state.get("__last_result")`.
  - `await tools.search("query")` -> `await tools.describe(ref)` -> `await mcp.<server>.<tool>(args)` (or `await mcp.call("server/tool", args)`).
- **Errors**: `e.name` is `NotFound` | `NoMatch` | `Ambiguous` | `NeedsApproval` | `Denied` | `Timeout` | `BadArgs` | `CmdFailed` | `Error`; `e.message` = `"op: detail"`; `e.data` holds details (e.g. `Ambiguous` -> `{ lines, symbols }`). Capped results (`search.rg`, `fs.list`, ...) are reported under `Warnings:`.

## Hygiene

1. **Fast PTC + Async `/tasks` Relay**: When making code changes followed by a heavy `>15s` test/build suite, run edits + fast compile/vet inside `mcp_agy-ptc_vm_exec` first, then launch the heavy suite via built-in `run_command(WaitMsBeforeAsync: 500..2000)` so `/tasks` streams live logs and wakes you when done. Never use `run_command` for `rg`/`grep`/`find`/`git status` or `<15s` checks.
2. **Return compact, unescaped output (`<= 9.6 KB`)**: Filter, count, and project inside TypeScript. For multiline code snippets, diffs, logs, or reports, prefer `print(...)` / `console.log(...)` (without `return`) or returning a plain formatted `string` to avoid JSON escaping overhead (`\n`, `\"`, `\\`). Use `return { ... }` for compact structured metadata or objects you want saved in `state.get("__last_result")`; pass `maxOutputBytes` only when larger raw text is explicitly needed.
3. **Parallelize independent calls**: Use `await Promise.all([search.rg(...), git.status()])` inside `code`.
4. **Consult `/ptc-exec` on edge cases**: Load the `ptc-exec` skill only if you hit an unfamiliar capability error or complex external MCP bridge workflow.
5. **`NeedsApproval` means escalate, not retry**: Recursive force deletes (`rm -rf`, `Remove-Item -Recurse -Force`), `git push` / `reset --hard` / `clean -f` / `checkout -- .`, publish commands, credential paths, and writes to agent/MCP configs or shell startup files are never run in the sandbox. Do not rephrase the command to slip past the guard; re-issue it via built-in `run_command` / `edit_file` so the user can confirm.
6. **Always `await t.commit()`**: A `tx` that is not committed is auto-rolled back when the program ends, and the response shows it under `Warnings:`. If a warning says `ROLLBACK FAILED`, inspect the listed files before doing anything else.
