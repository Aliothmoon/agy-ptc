package capabilities

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

// outlineFromSource extracts symbols from in-memory file content (shared by ast.* and edit/tx `symbol` scoping).
func outlineFromSource(absPath string, raw []byte) ([]SymbolInfo, []string) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".go" {
		if syms, ok := extractGoASTSymbols(absPath, raw, lines); ok {
			return syms, lines
		}
	}
	return extractGenericSymbols(ext, lines), lines
}

// containerKinds can qualify a nested symbol (`Editor::on_escape`, `Engine.Run`, `Cls.method`).
var containerKinds = map[string]bool{
	"impl": true, "trait": true, "struct": true, "enum": true, "mod": true, "class": true,
	"interface": true, "record": true, "type": true, "namespace": true, "object": true,
}

var goRecvName = regexp.MustCompile(`^\(\*?([A-Za-z_][A-Za-z0-9_]*)\)\.(.+)$`)

// symbolParts returns (receiver-or-"", leaf) for a symbol name, stripping generics: "(*Engine).Run" -> ("Engine","Run").
func symbolParts(name string) (string, string) {
	if m := goRecvName.FindStringSubmatch(name); m != nil {
		return m[1], m[2]
	}
	if i := strings.IndexAny(name, "<("); i > 0 {
		name = name[:i]
	}
	return "", name
}

// splitSymbolQuery accepts "Type::method", "Type.method", "(*Type).Method", or a bare name.
func splitSymbolQuery(q string) []string {
	q = strings.TrimSpace(q)
	if m := goRecvName.FindStringSubmatch(q); m != nil {
		return []string{m[1], m[2]}
	}
	q = strings.ReplaceAll(q, "::", ".")
	var parts []string
	for _, p := range strings.Split(q, ".") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func symbolSep(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rs", ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h":
		return "::"
	}
	return "."
}

// innermostContainer returns the smallest container symbol strictly enclosing s.
func innermostContainer(syms []SymbolInfo, s SymbolInfo) *SymbolInfo {
	var best *SymbolInfo
	for i := range syms {
		c := &syms[i]
		if !containerKinds[c.Kind] || (c.StartLine == s.StartLine && c.EndLine == s.EndLine) {
			continue
		}
		if c.StartLine <= s.StartLine && s.EndLine <= c.EndLine && (best == nil || c.EndLine-c.StartLine < best.EndLine-best.StartLine) {
			best = c
		}
	}
	return best
}

// qualifiedName renders a name that resolveSymbol accepts back, e.g. "Editor::on_escape" or "Engine.Run".
func qualifiedName(syms []SymbolInfo, s SymbolInfo, sep string) string {
	recv, leaf := symbolParts(s.Name)
	if recv != "" {
		return recv + sep + leaf
	}
	if c := innermostContainer(syms, s); c != nil {
		_, cl := symbolParts(c.Name)
		return cl + sep + leaf
	}
	return leaf
}

// symbolAt returns the qualified name of the innermost symbol containing line ("" if none).
func symbolAt(syms []SymbolInfo, line int, sep string) string {
	var best *SymbolInfo
	for i := range syms {
		s := &syms[i]
		if line >= s.StartLine && line <= s.EndLine && (best == nil || s.EndLine-s.StartLine < best.EndLine-best.StartLine) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return qualifiedName(syms, *best, sep)
}

// resolveSymbol finds exactly one symbol for query; NoMatch / Ambiguous errors list usable qualified names.
func resolveSymbol(syms []SymbolInfo, query, rel string) (SymbolInfo, error) {
	parts := splitSymbolQuery(query)
	if len(parts) == 0 {
		return SymbolInfo{}, sandbox.NewHostError(sandbox.KindBadArgs, nil, "empty symbol")
	}
	leaf, quals := parts[len(parts)-1], parts[:len(parts)-1]
	sep := symbolSep(rel)
	var hits []SymbolInfo
	for _, s := range syms {
		recv, sl := symbolParts(s.Name)
		if sl != leaf {
			continue
		}
		ok := true
		for _, q := range quals {
			if recv == q {
				continue
			}
			found := false
			for _, c := range syms {
				_, cl := symbolParts(c.Name)
				if containerKinds[c.Kind] && cl == q && c.StartLine <= s.StartLine && s.EndLine <= c.EndLine &&
					!(c.StartLine == s.StartLine && c.EndLine == s.EndLine) {
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var similar []string
		low := strings.ToLower(leaf)
		for _, s := range syms {
			_, sl := symbolParts(s.Name)
			slLow := strings.ToLower(sl)
			if strings.Contains(slLow, low) || (len(sl) > 2 && strings.Contains(low, slLow)) {
				similar = append(similar, qualifiedName(syms, s, sep))
			}
			if len(similar) == 5 {
				break
			}
		}
		msg := fmt.Sprintf("%s: symbol %s not found", rel, query)
		if len(similar) > 0 {
			msg += "; similar: " + strings.Join(similar, ", ")
		}
		return SymbolInfo{}, &sandbox.HostError{Kind: sandbox.KindNoMatch, Msg: msg}
	}
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, fmt.Sprintf("%s L%d", qualifiedName(syms, h, sep), h.StartLine))
	}
	return SymbolInfo{}, sandbox.NewHostError(sandbox.KindAmbiguous, nil, "%s: symbol %s matches %d (%s); qualify it", rel, query, len(hits), joinLimited(names, 6))
}
