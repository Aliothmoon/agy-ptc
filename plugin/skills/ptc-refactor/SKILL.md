---
name: ptc-refactor
description: >-
  [DEFAULT RECOMMENDED FOR MULTI-EDIT & REFACTORING] Default to PTC Mode
  (`mcp_agy-ptc_vm_exec` with one `edit.apply` + `sh.checks`) whenever editing
  >=2 code hunks/files or pairing code changes with build/lint/test verification;
  failed checks keep the edits, so only the fix is resent.
---

# Multi-File Refactoring Workflow (`/ptc-refactor`)

> [!IMPORTANT]
> **Default to PTC Mode**: Whenever a task modifies multiple locations/files or requires post-edit verification (`go test`, `tsc`, `eslint`, `pytest`), prefer one `mcp_agy-ptc_vm_exec` program (`edit.apply` + `sh.checks`) instead of sequential `edit_file` and `run_command` turns.

Follow this 2-turn protocol (plus a small fix turn if checks fail) when refactoring symbols, signatures, or patterns across multiple files:

1. **Turn 1 — Discovery & Impact Map (`mcp_agy-ptc_vm_exec`)**:
   - Run `search.rg` + `ast.outlines` / `ast.enclosingSymbol` in one script.
   - Read exact symbol windows (`fs.read(file, { startLine, endLine })`) in sandbox memory.
   - Persist the planned target list via `state.set("refactorTargets", targets)` and output a concise impact summary (`file`, `symbol`, `startLine..endLine`, `snippet`) via `print(...)` (preferred for readable multiline snippets) or `return`.
2. **Turn 2 — Low-Token Edit + Fast Checks (`mcp_agy-ptc_vm_exec`)**:
   - Load `state.get("refactorTargets")`, pass multiline replacement code via `payloads` (`P.*`), and pick the lowest-token hunk per target:
     - **Whole symbol rewrite**: `{ symbol: "Type::method", replaceSymbol: true, replacement: P.newFn }` (0 `target` tokens).
     - **10–120 line block**: `{ symbol, target: "<start line>", endTarget: "<end line>", replacement: P.newBlock }`.
     - **Small 1–5 line tweak**: `{ symbol, target, replacement }`.
   - Pass all files (including any new files via `{ path, create: P.newFile, overwrite: true }`) to one `await edit.apply({ edits: [...] })` so every hunk across all files is validated before any write (one bad hunk writes nothing; `bestEffort: true` writes the files that match and lists the rest in `failures`), then run fast `<15s` compile/scoped-test checks via `await sh.checks([...])`.
   - **If checks fail, keep the edits**: they are on disk, so return `check.failures` and send only the fixing hunks next turn — never resend the whole payload. Open `tx.begin()` (`t.edit` -> `t.verify` -> `t.commit()` / `t.rollback()`) only when a failed check must leave the tree untouched.
   - If a heavy `>15s` full integration/E2E suite is also required, launch it via native `run_command(WaitMsBeforeAsync: 500..2000)` so `/tasks` tracks live output in the background.
