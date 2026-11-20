package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alioth/agy-ptc/internal/audit"
	"github.com/alioth/agy-ptc/internal/output"
)

func TestEngineEndToEndSearchASTEditTxRollbackAndCommit(t *testing.T) {
	ws := t.TempDir()
	dataDir := t.TempDir()

	goMod := "module example.com/demo\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately use CRLF line endings to test CRLF preservation!
	srcFile := "package demo\r\n\r\nfunc Add(a, b int) int {\r\n\treturn a - b\r\n}\r\n"
	if err := os.WriteFile(filepath.Join(ws, "math.go"), []byte(srcFile), 0o644); err != nil {
		t.Fatal(err)
	}
	testFile := "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"got %d\", Add(2, 3))\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(ws, "math_test.go"), []byte(testFile), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(dataDir, ws)
	defer eng.Close()

	// 1. Test transaction auto-rollback when verification (`go test`) fails!
	outFail, isErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:   ws,
		Title: "bad fix attempt",
		Payloads: map[string]string{
			"badLine": "return a * b",
		},
		Code: `
			const [hits, outline] = await Promise.all([
				search.rg("return a - b", { glob: "*.go", literal: true }),
				ast.outline("math.go"),
			]);
			const sym = await ast.enclosingSymbol("math.go", hits[0].line);
			const t = await tx.begin();
			await t.edit("math.go", [{
				startLine: sym.startLine,
				endLine: sym.endLine,
				target: "return a - b",
				replacement: P.badLine,
			}]);
			const v = await t.verify(["go test ./..."]);
			if (!v.ok) {
				await t.rollback();
				return { rolledBack: true, symName: sym.name, summary: v.summary };
			}
			await t.commit();
			return { rolledBack: false };
		`,
	})
	if isErr {
		t.Fatalf("expected handled rollback object, got error: %s", outFail)
	}
	if !strings.Contains(outFail, `"rolledBack": true`) || !strings.Contains(outFail, `"symName": "Add"`) {
		t.Fatalf("unexpected rollback output: %s", outFail)
	}
	// Verify math.go was restored byte-for-byte with original `return a - b` and CRLF!
	afterRollback, _ := os.ReadFile(filepath.Join(ws, "math.go"))
	if string(afterRollback) != srcFile {
		t.Fatalf("expected math.go byte-exact rollback, got: %q", string(afterRollback))
	}

	// 2. Test transaction commit when `go test ./...` passes + CRLF preserved!
	outPass, isErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:   ws,
		Title: "good fix commit",
		Payloads: map[string]string{
			"goodLine": "return a + b",
		},
		Code: `
			const t = await tx.begin();
			await t.edit("math.go", [{
				target: "return a - b",
				replacement: P.goodLine,
			}]);
			const v = await t.verify(["go test ./..."]);
			if (!v.ok) {
				await t.rollback();
				throw new Error("test unexpectedly failed: " + JSON.stringify(v.failures));
			}
			await t.commit();
			return { committed: true, summary: v.summary };
		`,
	})
	if isErr {
		t.Fatalf("expected commit success, got error: %s", outPass)
	}
	if !strings.Contains(outPass, `"committed": true`) {
		t.Fatalf("unexpected commit output: %s", outPass)
	}
	afterCommit, _ := os.ReadFile(filepath.Join(ws, "math.go"))
	if !strings.Contains(string(afterCommit), "return a + b\r\n") {
		t.Fatalf("expected committed fix preserving CRLF, got: %q", string(afterCommit))
	}
}

func TestEngineUncaughtErrorAutoRollsBackOpenTxAndTimeoutInterruptsLoop(t *testing.T) {
	ws := t.TempDir()
	orig := "hello original\n"
	_ = os.WriteFile(filepath.Join(ws, "note.txt"), []byte(orig), 0o644)

	eng := NewEngine(t.TempDir(), ws)
	defer eng.Close()

	// Mutate inside `tx`, then throw before `commit()` -> cleanup hook MUST restore `note.txt`!
	_, isErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd: ws,
		Code: `
			const t = await tx.begin();
			await t.write("note.txt", "corrupted partial state\n");
			await t.write("temp_created.txt", "should be deleted on rollback\n");
			throw new Error("mid-flight crash");
		`,
	})
	if !isErr {
		t.Fatal("expected execution error on uncaught throw")
	}
	got, _ := os.ReadFile(filepath.Join(ws, "note.txt"))
	if string(got) != orig {
		t.Fatalf("expected auto-rollback on uncaught throw, got: %q", string(got))
	}
	if _, statErr := os.Stat(filepath.Join(ws, "temp_created.txt")); !os.IsNotExist(statErr) {
		t.Fatal("expected newly created file in aborted tx to be removed")
	}

	// Infinite synchronous CPU loop should be interrupted cleanly by watchdog timeout!
	outTimeout, isTimeoutErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:       ws,
		TimeoutMs: 250,
		Code:      `while (true) {}`,
	})
	if !isTimeoutErr || !strings.Contains(strings.ToLower(outTimeout), "timeout") && !strings.Contains(strings.ToLower(outTimeout), "deadline") {
		t.Fatalf("expected timeout error on infinite loop, got isErr=%v: %s", isTimeoutErr, outTimeout)
	}
}

func TestEngineHard8800ByteInlineCapAndStateAutoSpill(t *testing.T) {
	ws := t.TempDir()
	eng := NewEngine(t.TempDir(), ws)
	defer eng.Close()

	// Return a huge 40 KB array of CJK + ASCII objects
	out, isErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd: ws,
		Code: `
			const items = [];
			for (let i = 0; i < 600; i++) {
				items.push({ idx: i, note: "超长输出测试数据条目-" + i + "-abcdefghijklmnopqrstuvwxyz" });
			}
			return items;
		`,
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if len(out) > output.DefaultInlineMaxBytes {
		t.Fatalf("output UTF-8 byte length (%d) exceeded hard inline cap (%d)!", len(out), output.DefaultInlineMaxBytes)
	}
	if !strings.Contains(out, "__last_result") {
		t.Fatalf("expected auto-spill state hint in truncated output, got: %s", out)
	}

	// Turn 2: Slice the auto-spilled array from `state.get("__last_result")` in-memory!
	out2, isErr2 := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd: ws,
		Code: `
			const all = state.get("__last_result") as Array<{ idx: number }>;
			return { totalCount: all.length, sampleIdx: all[599].idx };
		`,
	})
	if isErr2 {
		t.Fatalf("unexpected error reading state in turn 2: %s", out2)
	}
	if !strings.Contains(out2, `"totalCount": 600`) || !strings.Contains(out2, `"sampleIdx": 599`) {
		t.Fatalf("expected full 600 items preserved in state, got: %s", out2)
	}
}

func TestExternalMCPStreamableHTTPProxyBridge(t *testing.T) {
	// Spin up a mock external Streamable HTTP MCP server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		reply := map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `{"hits":["auth.go:42","jwt.go:18"],"query":"` + rpc.Params.Arguments["query"].(string) + `"}`},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer srv.Close()

	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, ".agents"), 0o755)
	mcpCfg := `{"mcpServers":{"mock-search":{"serverUrl":"` + srv.URL + `"}}}`
	_ = os.WriteFile(filepath.Join(ws, ".agents", "mcp_config.json"), []byte(mcpCfg), 0o644)

	eng := NewEngine(t.TempDir(), ws)
	defer eng.Close()

	// Call `mcp.mock_search.find_code(...)` using pi-fabric dynamic Proxy syntax!
	out, isErr := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd: ws,
		Code: `
			const servers = await mcp.servers();
			const res = await mcp.mock_search.find_code({ query: "VerifyToken" });
			return { servers, hits: res.data.hits, echoed: res.data.query };
		`,
	})
	if isErr {
		t.Fatalf("expected external MCP bridge call to succeed, got: %s", out)
	}
	if !strings.Contains(out, `"auth.go:42"`) || !strings.Contains(out, `"VerifyToken"`) {
		t.Fatalf("unexpected external MCP result: %s", out)
	}
}

func TestAuditLoggingErrorSerializationAliasesAndNonGitStatus(t *testing.T) {
	ws := t.TempDir() // Not a git repo!
	dataDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello alias"), 0o644)

	eng := NewEngine(dataDir, ws)
	defer eng.Close()

	// 1. Non-git `git.status()` returns `{ isRepo: false }` without rejecting Promise.all, AND `fs.readFile` alias works!
	out1, isErr1 := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:   ws,
		Title: "alias and non-git status",
		Code: `
			const [txt, gs] = await Promise.all([
				(fs as any).readFile("hello.txt"),
				git.status(),
			]);
			return { txt, isRepo: gs.isRepo };
		`,
	})
	if isErr1 {
		t.Fatalf("expected non-git status & readFile alias to succeed, got: %s", out1)
	}
	if !strings.Contains(out1, `"isRepo": false`) || !strings.Contains(out1, `"hello alias"`) {
		t.Fatalf("unexpected output: %s", out1)
	}

	// 2. Caught async host error must serialize cleanly via BOTH `String(e)` AND `JSON.stringify(e)` (never `[object Object]` or `{"value":{}}`), AND be logged as `partial_op_error`!
	out2, isErr2 := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:   ws,
		Title: "caught host error serialization",
		Code: `
			try {
				await fs.read("does_not_exist.txt");
				return { ok: true };
			} catch (e: any) {
				return {
					asString: String(e),
					asJSON: JSON.stringify(e),
					msg: e.message,
				};
			}
		`,
	})
	if isErr2 {
		t.Fatalf("expected caught error to return normally, got: %s", out2)
	}
	if strings.Contains(out2, "[object Object]") || strings.Contains(out2, `{"value":{}}`) || !strings.Contains(out2, "does_not_exist.txt") {
		t.Fatalf("host error serialized poorly: %s", out2)
	}

	// 3. Uncaught unknown method throws clear `UnknownMethodError` listing valid methods, and records in `errors.jsonl`!
	out3, isErr3 := eng.RunMCPExec(context.Background(), ExecInput{
		Cwd:   ws,
		Title: "unknown method check",
		Code:  `return await (fs as any).totallyMadeUpMethod("x");`,
	})
	if !isErr3 || !strings.Contains(out3, "UnknownMethodError") || !strings.Contains(out3, "Available methods on 'fs'") {
		t.Fatalf("expected UnknownMethodError with available methods, got: %s", out3)
	}

	// 4. Verify audit logs (`executions.jsonl` has 3 runs; `errors.jsonl` has 2 problem runs: 1 caught op error + 1 uncaught error)
	allEntries, usedFiles, err := audit.LoadEntries(dataDir, false)
	if err != nil || len(allEntries) != 3 {
		t.Fatalf("expected 3 execution log entries, got %d (err=%v)", len(allEntries), err)
	}
	errEntries, _, err := audit.LoadEntries(dataDir, true)
	if err != nil || len(errEntries) != 2 {
		t.Fatalf("expected 2 error log entries (caught + uncaught), got %d (err=%v)", len(errEntries), err)
	}
	statsText := audit.FormatStats(allEntries)
	logsText := audit.FormatLogs(errEntries, usedFiles, 10, "")
	detailText := audit.FormatLogs(allEntries, usedFiles, 10, errEntries[1].ID)
	if !strings.Contains(statsText, "unknown_method_or_type") || !strings.Contains(logsText, "caught op error") || !strings.Contains(detailText, "totallyMadeUpMethod") {
		t.Fatalf("unexpected audit reports:\nSTATS:\n%s\nLOGS:\n%s\nDETAIL:\n%s", statsText, logsText, detailText)
	}
}
