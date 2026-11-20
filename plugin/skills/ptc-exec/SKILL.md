---
name: ptc-exec
description: >-
  Detailed API reference, error recovery playbook, and external MCP bridging guide
  for mcp_agy-ptc_ptc_exec TypeScript programs. Load when composing advanced transactions,
  cross-turn state paging, or external MCP calls, or after a PTC runtime error.
---

# `mcp_agy-ptc_ptc_exec` — Advanced Reference & Troubleshooting

## 1. Execution Model

- `code` is compiled with `esbuild` (`TypeScript -> ES2020`) before any host operation runs. Syntax errors or missing `P.<key>` references fail preflight with **0 side effects** and exact 1-based line numbers.
- Top-level `await` and `return` work directly. Do **not** wrap `code` in `async function main() {}`.
- `import type { ... } from "..."` is stripped safely; runtime `import`/`require` are disabled for sandbox isolation. Use host capabilities (`fs`, `search`, `ast`, `edit`, `tx`, `git`, `sh`, `state`, `tools`, `mcp`).

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

If a result exceeds `8,800` UTF-8 bytes, `AgyPTC` automatically saves the full object in `state.get("__last_result")` (and a numbered `state.get("__result_N")` key) instead of spilling to `output.txt`. Filter or page it in the next turn:

```ts
const prev = state.get("__last_result") as Array<{ file: string; line: number }>;
return prev.slice(0, 25);
```
