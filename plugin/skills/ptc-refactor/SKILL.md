---
name: ptc-refactor
description: >-
  [DEFAULT RECOMMENDED FOR MULTI-EDIT & REFACTORING] Default to PTC Mode
  (`mcp_agy-ptc_ptc_exec` with `tx.begin()`) whenever editing >=2 code hunks/files
  or pairing code changes with build/lint/test verification, ensuring atomic
  rollback on failure in minimal turns.
---

# Transactional Multi-File Refactoring Workflow (`/ptc-refactor`)

> [!IMPORTANT]
> **Default to PTC Transactional Mode**: Whenever a task modifies multiple locations/files or requires post-edit verification (`go test`, `tsc`, `eslint`, `pytest`), prefer `mcp_agy-ptc_ptc_exec` + `tx.begin()` by default instead of sequential `edit_file` and `run_command` turns.

Follow this 2-turn maximum protocol when refactoring symbols, signatures, or patterns across multiple files:

1. **Turn 1 — Discovery & Impact Map (`mcp_agy-ptc_ptc_exec`)**:
   - Run `search.rg` + `ast.outlines` / `ast.enclosingSymbol` in one script.
   - Read exact symbol windows (`fs.read(file, { startLine, endLine })`) in sandbox memory.
   - Persist the planned target list via `state.set("refactorTargets", targets)` and return a concise impact summary (`file`, `symbol`, `startLine..endLine`, `snippet`).
2. **Turn 2 — Atomic Edit + Verify + Commit (`mcp_agy-ptc_ptc_exec`)**:
   - Load `state.get("refactorTargets")`, pass multiline code blocks via `payloads` (`P.*`), open `const t = await tx.begin()`, apply all edits, and run `await t.verify([...])`.
   - Commit only when every build, typecheck, and test check passes; otherwise roll back and inspect `check.failures` to fix in the next turn.
