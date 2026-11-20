package compiler

import (
	"strings"
	"testing"
)

func TestCompileTypeScriptAndStripImportType(t *testing.T) {
	code := `import type { Foo } from "./bar";
interface User { id: number; name: string }
const u: User = { id: 1, name: P.userName };
return u;`

	res, err := Compile(code, map[string]string{"userName": "Alice"})
	if err != nil {
		t.Fatalf("expected clean compile, got error: %v", err)
	}
	if !strings.Contains(res.JSCode, "__ptc_main") {
		t.Fatalf("expected wrapped __ptc_main in JSCode: %s", res.JSCode)
	}
}

func TestCompileMissingPayloadKeyFailsPreflight(t *testing.T) {
	code := `const content = P.missingBlock;
return content;`

	_, err := Compile(code, map[string]string{"actualBlock": "hello"})
	if err == nil {
		t.Fatal("expected missing payload preflight error, got nil")
	}
	if !strings.Contains(err.Error(), "missingBlock") || !strings.Contains(err.Error(), "actualBlock") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCompileSyntaxErrorReportsExactUserLine(t *testing.T) {
	code := `const a = 1;
const b = 2;
const broken = { unclosed: ;
return a + b;`

	_, err := Compile(code, nil)
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
	if !strings.Contains(err.Error(), "line 3:") {
		t.Fatalf("expected error pointing to user line 3, got: %v", err)
	}
}
