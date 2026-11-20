---
name: ptc-refactor
description: >-
  [DEFAULT RECOMMENDED FOR MULTI-EDIT & REFACTORING] Default to PTC Mode
  (`mcp_agy-ptc_vm_exec` with `tx.begin()`) whenever editing >=2 code hunks/files
  or pairing code changes with build/lint/test verification, ensuring atomic
  rollback on failure in minimal turns.
---

# Transactional Multi-File Refactoring Workflow (`/ptc-refactor`)

> [!IMPORTANT]
> **Default to PTC Transactional Mode**: Whenever a task modifies multiple locations/files or requires post-edit verification (`go test`, `tsc`, `eslint`, `pytest`), prefer `mcp_agy-ptc_vm_exec` + `tx.begin()` by default instead of sequential `edit_file` and `run_command` turns.

Follow this 2-turn maximum protocol when refactoring symbols, signatures, or patterns across multiple files:

1. **Turn 1 — Discovery & Impact Map (`mcp_agy-ptc_vm_exec`)**:
   - Run `search.rg` + `ast.outlines` / `ast.enclosingSymbol` in one script.
   - Read exact symbol windows (`fs.read(file, { startLine, endLine })`) in sandbox memory.
   - Persist the planned target list via `state.set("refactorTargets", targets)` and return a concise impact summary (`file`, `symbol`, `startLine..endLine`, `snippet`).
2. **Turn 2 — Atomic Edit + Verify + Commit (`mcp_agy-ptc_vm_exec`)**:
   - Load `state.get("refactorTargets")`, pass multiline code blocks via `payloads` (`P.*`), open `const t = await tx.begin()`, apply all edits, and run fast `<15s` compile/scoped-test checks via `await t.verify([...])`.
   - Commit only when fast checks pass; otherwise roll back and inspect `check.failures` to fix. If a heavy `>15s` full integration/E2E suite is also required after commit, launch it via native `run_command(WaitMsBeforeAsync: 500..2000)` so `/tasks` tracks live output in the background.
