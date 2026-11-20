package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PageBytes is the default page size for long text: one page plus the response header stays under the
// 9.6KB inline budget (Antigravity spills larger MCP outputs to output.txt).
const PageBytes = 8000

// Page is one line-aligned slice of a long text.
type Page struct {
	Text      string
	FirstLine int // 1-based
	LastLine  int
}

// Paginate splits text into pages of at most size bytes, breaking only between lines
// (a single line longer than size is split at a UTF-8 boundary).
func Paginate(text string, size int) []Page {
	if size < 200 {
		size = 200
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var pages []Page
	var cur strings.Builder
	first := 1
	flush := func(last int) {
		pages = append(pages, Page{Text: cur.String(), FirstLine: first, LastLine: last})
		cur.Reset()
		first = last + 1
	}
	for i, line := range lines {
		n := i + 1
		for len(line) > size { // pathological long line: hard split
			if cur.Len() > 0 {
				flush(n - 1)
			}
			cut := size
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			cur.WriteString(line[:cut])
			pages = append(pages, Page{Text: cur.String(), FirstLine: n, LastLine: n})
			cur.Reset()
			first = n
			line = line[cut:]
		}
		extra := len(line)
		if cur.Len() > 0 {
			extra++ // newline separator
		}
		if cur.Len()+extra > size && cur.Len() > 0 {
			flush(n - 1)
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 || len(pages) == 0 {
		flush(len(lines))
	}
	return pages
}

// PageFooter tells the model where it is and how to continue, e.g.
// "[page 1/4 · lines 1-212 of 890 · next: print(state.page(2)) · last: state.page(-1)]".
func PageFooter(p Page, index, total, totalLines, size int) string {
	sizeArg := ""
	if size != PageBytes {
		sizeArg = fmt.Sprintf(", %d", size)
	}
	f := fmt.Sprintf("[page %d/%d · lines %d-%d of %d", index, total, p.FirstLine, p.LastLine, totalLines)
	if index < total {
		f += fmt.Sprintf(" · next: print(state.page(%d%s))", index+1, sizeArg)
		if index+1 < total {
			f += fmt.Sprintf(" · last: state.page(-1%s)", sizeArg)
		}
	}
	return f + "]"
}

// MarshalPretty renders a value as indented JSON (no HTML escaping) so it pages line by line.
func MarshalPretty(val any) string {
	if s, ok := val.(string); ok {
		return s
	}
	out, err := marshalNoEscapeHTML(val, true)
	if err != nil {
		return fmt.Sprintf("%v", val)
	}
	return out
}
