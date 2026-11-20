package capabilities

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func requireStringArg(args []any, idx int, name string) (string, error) {
	if len(args) <= idx || args[idx] == nil {
		return "", fmt.Errorf("missing required string argument '%s'", name)
	}
	if s, ok := args[idx].(string); ok && strings.TrimSpace(s) != "" {
		return s, nil
	}
	if m, ok := args[idx].(map[string]any); ok {
		if s := firstString(m, name, "path", "file", "key", "pattern", "ref", "query"); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("argument '%s' must be a non-empty string", name)
}

func parsePathAndOpts(args []any) (string, map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return "", nil, fmt.Errorf("missing path argument")
	}
	if s, ok := args[0].(string); ok {
		var opts map[string]any
		if len(args) > 1 {
			opts, _ = args[1].(map[string]any)
		}
		return s, opts, nil
	}
	if m, ok := args[0].(map[string]any); ok {
		p := firstString(m, "path", "file", "file_path", "dir", "directory")
		return p, m, nil
	}
	return "", nil, fmt.Errorf("invalid path argument")
}

func parsePatternAndOpts(args []any) (string, map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return "", nil, fmt.Errorf("missing pattern argument")
	}
	if s, ok := args[0].(string); ok {
		var opts map[string]any
		if len(args) > 1 {
			opts, _ = args[1].(map[string]any)
		}
		return s, opts, nil
	}
	if m, ok := args[0].(map[string]any); ok {
		pat := firstString(m, "pattern", "glob", "query", "name")
		if pat == "" {
			return "", nil, fmt.Errorf("pattern cannot be empty")
		}
		return pat, m, nil
	}
	return "", nil, fmt.Errorf("invalid pattern argument")
}

func firstString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, isStr := v.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

func optInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if n, ok := toInt(v); ok {
		return n
	}
	return def
}

func optBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func extractStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, str)
			}
		}
		return out
	case string:
		if strings.TrimSpace(s) != "" {
			return []string{s}
		}
	}
	return nil
}

func relDisplayPath(cwd, absPath string) string {
	if cwd == "" {
		return filepath.ToSlash(absPath)
	}
	rel, err := filepath.Rel(cwd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}

func writeFilePreserveMode(absPath string, data []byte) error {
	mode := os.FileMode(0o644)
	writeTarget := absPath
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if resolved, rErr := filepath.EvalSymlinks(absPath); rErr == nil {
				writeTarget = resolved
				if tInfo, sErr := os.Stat(resolved); sErr == nil {
					mode = tInfo.Mode().Perm()
				}
			}
		} else {
			mode = info.Mode().Perm()
		}
	}
	if err := os.MkdirAll(filepath.Dir(writeTarget), 0o755); err != nil {
		return err
	}
	return os.WriteFile(writeTarget, data, mode)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
