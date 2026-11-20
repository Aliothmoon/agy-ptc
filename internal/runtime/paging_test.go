package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/aliothmoon/agy-ptc/internal/output"
)

var footerRe = regexp.MustCompile(`\[page (\d+)/(\d+) · lines (\d+)-(\d+) of (\d+)`)

func TestLongPrintIsPagedNotCutInTheMiddle(t *testing.T) {
	eng, ws := newErrWorkspace(t)
	var sb strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&sb, "fn line_%04d() { body(); }\n", i)
	}
	writeFiles(t, ws, map[string]string{"src/huge.rs": sb.String()})

	out, isErr := run(t, eng, ws, `print(await fs.read("src/huge.rs"));`)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if len(out) > output.DefaultInlineMaxBytes {
		t.Fatalf("response %d bytes exceeds the inline budget", len(out))
	}
	if strings.Contains(out, "[omitted ") {
		t.Fatalf("long print must be paged, not cut in the middle:\n%s", out[len(out)-300:])
	}
	m := footerRe.FindStringSubmatch(out)
	if m == nil || m[1] != "1" || m[3] != "1" || m[5] != "2000" || !strings.Contains(out, "next: print(state.page(2)) · last: state.page(-1)") {
		t.Fatalf("missing page-1 footer:\n%s", out[len(out)-300:])
	}
	lastOnPage1 := m[4]
	mustContainAll(t, out, "fn line_0001()", "Largest results: fs.read(src/huge.rs)")

	out2, _ := run(t, eng, ws, `print(state.page(2));`)
	m2 := footerRe.FindStringSubmatch(out2)
	if m2 == nil || m2[1] != "2" {
		t.Fatalf("page 2 footer missing:\n%s", out2)
	}
	var first, prevLast int
	fmt.Sscan(m2[3], &first)
	fmt.Sscan(lastOnPage1, &prevLast)
	if first != prevLast+1 {
		t.Fatalf("page 2 starts at line %d, page 1 ended at %d", first, prevLast)
	}
	mustContainAll(t, out2, fmt.Sprintf("fn line_%04d()", first))

	out3, _ := run(t, eng, ws, `print(state.page(-1));`)
	mustContainAll(t, out3, "fn line_2000()", "of 2000]")
	if strings.Contains(out3, "next:") {
		t.Fatalf("last page must not offer a next page:\n%s", out3)
	}

	out4, _ := run(t, eng, ws, `try { state.page(99); } catch (e) { return e.name + ": " + e.message; }`)
	mustContainAll(t, out4, "BadArgs: state.page: page out of range (1..")
}

func TestLongStringResultIsPagedAndObjectsPageAsJSON(t *testing.T) {
	eng, ws := newErrWorkspace(t)
	out, _ := run(t, eng, ws, `return Array.from({ length: 1500 }, (_, i) => "row " + i).join("\n");`)
	mustContainAll(t, out, "Result:\nrow 0\nrow 1\n", "[page 1/", "next: print(state.page(2))")

	_, _ = run(t, eng, ws, `return { items: Array.from({ length: 800 }, (_, i) => ({ id: i, name: "item-" + i })) };`)
	out, _ = run(t, eng, ws, `print(state.page(1));`)
	mustContainAll(t, out, "{\n  \"items\": [\n    {\n      \"id\": 0,", "[page 1/")
}

func TestCapWarningsReportTotals(t *testing.T) {
	eng, ws := newErrWorkspace(t)
	files := map[string]string{}
	for i := 0; i < 30; i++ {
		files[fmt.Sprintf("data/f%02d.txt", i)] = "x\n"
	}
	writeFiles(t, ws, files)
	out, isErr := run(t, eng, ws, `
const g = await search.glob("**/*.txt", { path: "data", maxResults: 10 });
const l = await fs.list("data", { limit: 5 });
return [g.length, l.length];`)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	mustContainAll(t, out, "search.glob: capped at 10 of 30 files", "fs.list: capped at 5 of 30 entries")
}
