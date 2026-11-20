// Package compiler transpiles TypeScript async function bodies into ES2020 JavaScript
// with VLQ source-mapped diagnostics and static payload key validation.
package compiler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const wrapperPrefixLines = 2

var (
	importTypeLineRegex    = regexp.MustCompile(`^\s*import\s+type\s+[^;]+;?\s*$`)
	importDefaultShimRegex = regexp.MustCompile(`^\s*import\s+(?:\*\s+as\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+["'](?:node:)?(buffer|path|crypto|url|fs(?:/promises)?)["']\s*;?\s*$`)
	importNamedShimRegex   = regexp.MustCompile(`^\s*import\s*\{([^}]+)\}\s*from\s+["'](?:node:)?(buffer|path|crypto|url|fs(?:/promises)?)["']\s*;?\s*$`)
	payloadDotRegex        = regexp.MustCompile(`\b(?:P|payloads)\.([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	payloadBracketRegex    = regexp.MustCompile(`\b(?:P|payloads)\[\s*["']([^"']+)["']\s*\]`)
	jsStackLocRegex        = regexp.MustCompile(`user_program\.ts:(\d+):(\d+)`)
)

// CompileResult holds the transpiled JavaScript and source mapping metadata.
type CompileResult struct {
	JSCode     string
	SourceMap  []byte
	SourceLine []string
}

// CompileError describes a static syntax or payload preflight error with 1-based user source lines.
type CompileError struct {
	Message string
	Details []string
}

func (e *CompileError) Error() string {
	if len(e.Details) == 0 {
		return e.Message
	}
	return e.Message + "\n" + strings.Join(e.Details, "\n")
}

// Compile validates payload key references and transpiles TypeScript async body code to ES2020 JavaScript.
func Compile(code string, payloads map[string]string) (*CompileResult, error) {
	if strings.TrimSpace(code) == "" {
		return nil, &CompileError{Message: "CompileError: 'code' cannot be empty"}
	}

	normalized := strings.ReplaceAll(code, "\r\n", "\n")
	srcLines := strings.Split(normalized, "\n")

	if err := checkPayloadKeys(normalized, payloads); err != nil {
		return nil, err
	}

	// Preserve exact 1-to-1 line numbering by rewriting single-line `import type` and sandbox builtin imports in-place.
	sanitizedLines := make([]string, len(srcLines))
	for i, line := range srcLines {
		if importTypeLineRegex.MatchString(line) {
			sanitizedLines[i] = "// [type import stripped]"
		} else if rewritten, ok := rewriteBuiltinImportLine(line); ok {
			sanitizedLines[i] = rewritten
		} else {
			sanitizedLines[i] = line
		}
	}

	// Line 1 & 2 are wrapper header -> user line 1 is wrapped line 3 (offset = wrapperPrefixLines = 2).
	wrapped := "(async function __ptc_main(P: Record<string, string>, payloads: Record<string, string>) {\n\"use strict\";\n" +
		strings.Join(sanitizedLines, "\n") +
		"\n})"

	res := api.Transform(wrapped, api.TransformOptions{
		Loader:            api.LoaderTS,
		Target:            api.ES2020,
		Format:            api.FormatDefault,
		Sourcemap:         api.SourceMapExternal,
		Sourcefile:        "user_program.ts",
		SourcesContent:    api.SourcesContentExclude,
		MinifyWhitespace:  false,
		MinifyIdentifiers: false,
		MinifySyntax:      false,
	})

	if len(res.Errors) > 0 {
		var details []string
		for _, msg := range res.Errors {
			if msg.Location != nil {
				userLine := msg.Location.Line - wrapperPrefixLines
				col := msg.Location.Column + 1
				if userLine < 1 {
					userLine = 1
				}
				if userLine > len(srcLines) {
					userLine = len(srcLines)
				}
				linePreview := ""
				if userLine >= 1 && userLine <= len(srcLines) {
					linePreview = strings.TrimRight(srcLines[userLine-1], " \t")
				}
				details = append(details, fmt.Sprintf("  at line %d:%d: %s\n    %d | %s", userLine, col, msg.Text, userLine, linePreview))
			} else {
				details = append(details, "  "+msg.Text)
			}
		}
		return nil, &CompileError{
			Message: "TypeScript syntax check failed (0 side effects executed):",
			Details: details,
		}
	}

	return &CompileResult{
		JSCode:     string(res.Code),
		SourceMap:  res.Map,
		SourceLine: srcLines,
	}, nil
}

func rewriteBuiltinImportLine(line string) (string, bool) {
	if m := importDefaultShimRegex.FindStringSubmatch(line); m != nil {
		ident := m[1]
		mod := m[2]
		switch mod {
		case "buffer":
			if ident == "Buffer" {
				return "// [builtin Buffer active]", true
			}
			return fmt.Sprintf("const %s = { Buffer: (globalThis as any).Buffer };", ident), true
		case "url":
			return fmt.Sprintf("const %s = { URL: (globalThis as any).URL, URLSearchParams: (globalThis as any).URLSearchParams };", ident), true
		case "fs", "fs/promises":
			if ident == "fs" {
				return "// [sandbox fs active]", true
			}
			return fmt.Sprintf("const %s = (globalThis as any).fs;", ident), true
		default:
			if ident == mod {
				return fmt.Sprintf("// [builtin %s active]", mod), true
			}
			return fmt.Sprintf("const %s = (globalThis as any).%s;", ident, mod), true
		}
	}
	if m := importNamedShimRegex.FindStringSubmatch(line); m != nil {
		specList := strings.TrimSpace(m[1])
		mod := m[2]
		var rewrittenSpecs []string
		for _, part := range strings.Split(specList, ",") {
			p := strings.TrimSpace(part)
			if p == "" || strings.HasPrefix(p, "type ") {
				continue
			}
			if idx := strings.Index(p, " as "); idx > 0 {
				orig := strings.TrimSpace(p[:idx])
				alias := strings.TrimSpace(p[idx+4:])
				rewrittenSpecs = append(rewrittenSpecs, orig+": "+alias)
			} else {
				rewrittenSpecs = append(rewrittenSpecs, p)
			}
		}
		if len(rewrittenSpecs) == 0 {
			return "// [type import stripped]", true
		}
		joined := strings.Join(rewrittenSpecs, ", ")
		switch mod {
		case "buffer", "url":
			var filtered []string
			for _, rs := range rewrittenSpecs {
				if rs != "Buffer" && rs != "URL" && rs != "URLSearchParams" {
					filtered = append(filtered, rs)
				}
			}
			if len(filtered) == 0 {
				return fmt.Sprintf("// [builtin %s globals active]", mod), true
			}
			return fmt.Sprintf("const { %s } = globalThis as any;", strings.Join(filtered, ", ")), true
		case "fs", "fs/promises":
			return fmt.Sprintf("const { %s } = (globalThis as any).fs;", joined), true
		default:
			return fmt.Sprintf("const { %s } = (globalThis as any).%s;", joined, mod), true
		}
	}
	return "", false
}

func checkPayloadKeys(code string, payloads map[string]string) error {
	referenced := map[string]bool{}
	for _, m := range payloadDotRegex.FindAllStringSubmatch(code, -1) {
		key := m[1]
		// Ignore Object.prototype methods if called on payloads (e.g. payloads.hasOwnProperty)
		if key == "hasOwnProperty" || key == "toString" || key == "valueOf" {
			continue
		}
		referenced[key] = true
	}
	for _, m := range payloadBracketRegex.FindAllStringSubmatch(code, -1) {
		referenced[m[1]] = true
	}
	if len(referenced) == 0 {
		return nil
	}
	var missing []string
	for k := range referenced {
		if _, ok := payloads[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	var provided []string
	for k := range payloads {
		provided = append(provided, k)
	}
	sort.Strings(provided)
	providedDesc := "(none)"
	if len(provided) > 0 {
		providedDesc = strings.Join(provided, ", ")
	}
	return &CompileError{
		Message: fmt.Sprintf(
			"PayloadError: missing payload key(s) [%s] in P/payloads (0 side effects executed). Provided keys: %s",
			strings.Join(missing, ", "),
			providedDesc,
		),
	}
}

// FormatRuntimeError translates goja stack locations using the esbuild VLQ sourcemap back to 1-based user TypeScript lines.
func FormatRuntimeError(errText string, cr *CompileResult) string {
	if cr == nil {
		return errText
	}
	lineMap := buildGeneratedToUserLineMap(cr.SourceMap)
	rewritten := jsStackLocRegex.ReplaceAllStringFunc(errText, func(match string) string {
		sub := jsStackLocRegex.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		var genLine, genCol int
		fmt.Sscanf(sub[1], "%d", &genLine)
		fmt.Sscanf(sub[2], "%d", &genCol)
		userLine := mapLine(genLine, lineMap, len(cr.SourceLine))
		return fmt.Sprintf("code:%d:%d", userLine, genCol)
	})
	return rewritten
}

type sourceMapJSON struct {
	Mappings string `json:"mappings"`
}

func buildGeneratedToUserLineMap(rawMap []byte) map[int]int {
	out := map[int]int{}
	if len(rawMap) == 0 {
		return out
	}
	var sm sourceMapJSON
	if err := json.Unmarshal(rawMap, &sm); err != nil || sm.Mappings == "" {
		return out
	}
	origLine := 0 // 0-based in VLQ
	genLines := strings.Split(sm.Mappings, ";")
	for genIdx, group := range genLines {
		if group == "" {
			continue
		}
		segments := strings.Split(group, ",")
		recorded := false
		for _, seg := range segments {
			fields := decodeVLQ(seg)
			if len(fields) >= 4 {
				origLine += fields[2]
				if !recorded {
					userLine := (origLine + 1) - wrapperPrefixLines
					if userLine >= 1 {
						out[genIdx+1] = userLine
						recorded = true
					}
				}
			}
		}
	}
	return out
}

func mapLine(genLine int, lineMap map[int]int, totalUserLines int) int {
	if u, ok := lineMap[genLine]; ok {
		if u > totalUserLines && totalUserLines > 0 {
			return totalUserLines
		}
		return u
	}
	approx := genLine - wrapperPrefixLines
	if approx < 1 {
		approx = 1
	}
	if totalUserLines > 0 && approx > totalUserLines {
		approx = totalUserLines
	}
	return approx
}

const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func decodeVLQ(seg string) []int {
	var vals []int
	shift := 0
	value := 0
	for i := 0; i < len(seg); i++ {
		digit := strings.IndexByte(base64Chars, seg[i])
		if digit < 0 {
			break
		}
		cont := (digit & 32) != 0
		digit &= 31
		value += digit << shift
		if cont {
			shift += 5
		} else {
			neg := (value & 1) == 1
			value >>= 1
			if neg {
				value = -value
			}
			vals = append(vals, value)
			value = 0
			shift = 0
		}
	}
	return vals
}
