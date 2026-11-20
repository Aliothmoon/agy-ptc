package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Decision represents the outcome of evaluating a capability operation.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// PolicyError is returned when an operation is blocked or requires host interactive approval.
type PolicyError struct {
	Decision Decision
	Action   string
	Target   string
	Reason   string
}

func (e *PolicyError) Error() string {
	if e.Decision == DecisionAsk {
		return fmt.Sprintf("APPROVAL_REQUIRED [%s(%s)]: %s (invoke native run_command/edit_file or ask_question so Antigravity CLI can prompt the user)", e.Action, e.Target, e.Reason)
	}
	return fmt.Sprintf("POLICY_DENIED [%s(%s)]: %s", e.Action, e.Target, e.Reason)
}

// IsApprovalRequired reports whether err indicates an interactive approval gate.
func IsApprovalRequired(err error) bool {
	var pe *PolicyError
	return errors.As(err, &pe) && pe.Decision == DecisionAsk
}

type cliSettings struct {
	AllowNonWorkspaceAccess *bool `json:"allowNonWorkspaceAccess"`
	Permissions             struct {
		Allow []string `json:"allow"`
		Ask   []string `json:"ask"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

type parsedRule struct {
	action  string
	target  string
	isRegex bool
	re      *regexp.Regexp
}

// Engine enforces workspace confinement, sensitive file guards, and Antigravity CLI settings.json rules.
type Engine struct {
	workspaceRoots          []string
	allowNonWorkspaceAccess bool
	denyRules               []parsedRule
	askRules                []parsedRule
	allowRules              []parsedRule
	sensitiveDirs           []string
}

// NewEngine creates a policy engine rooted at workspaceDir and aligned with ~/.gemini/antigravity-cli/settings.json.
func NewEngine(workspaceDir string, extraRoots ...string) *Engine {
	e := &Engine{
		// Default to allowing external non-sensitive paths unless explicitly disabled via settings.json (`"allowNonWorkspaceAccess": false`) or `AGY_PTC_STRICT_WORKSPACE=1`.
		allowNonWorkspaceAccess: os.Getenv("AGY_PTC_STRICT_WORKSPACE") != "1",
	}
	seen := map[string]bool{}
	addRoot := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		real, err := evalSymlinksExisting(abs)
		if err == nil {
			abs = real
		}
		key := normalizeComparePath(abs)
		if !seen[key] {
			seen[key] = true
			e.workspaceRoots = append(e.workspaceRoots, abs)
		}
	}
	addRoot(workspaceDir)
	for _, r := range extraRoots {
		addRoot(r)
	}

	home := userHomeDir()
	if home != "" {
		for _, rel := range []string{
			".ssh",
			".gnupg",
			".aws",
			filepath.Join(".gemini", "antigravity", "mcp_oauth_tokens.json"),
			filepath.Join(".gemini", "antigravity-cli", "mcp_oauth_tokens.json"),
		} {
			e.sensitiveDirs = append(e.sensitiveDirs, filepath.Join(home, rel))
		}
		settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
		e.loadSettingsFile(settingsPath)
	}
	return e
}

// LoadSettingsJSON loads CLI settings JSON directly (useful for testing and custom configs).
func (e *Engine) LoadSettingsJSON(raw []byte) {
	var cfg cliSettings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return
	}
	if cfg.AllowNonWorkspaceAccess != nil {
		e.allowNonWorkspaceAccess = *cfg.AllowNonWorkspaceAccess
	}
	e.denyRules = parseRuleList(cfg.Permissions.Deny)
	e.askRules = parseRuleList(cfg.Permissions.Ask)
	e.allowRules = parseRuleList(cfg.Permissions.Allow)
}

func (e *Engine) loadSettingsFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	e.LoadSettingsJSON(raw)
}

// ResolveAndCheckRead resolves path against baseDir and checks read permissions.
func (e *Engine) ResolveAndCheckRead(baseDir, targetPath string) (string, error) {
	abs, realPath, err := resolveWithSymlinks(baseDir, targetPath)
	if err != nil {
		return "", err
	}
	if e.isSensitivePath(realPath) {
		return "", &PolicyError{
			Decision: DecisionDeny,
			Action:   "read_file",
			Target:   targetPath,
			Reason:   "access to sensitive credential directory is blocked",
		}
	}
	if dec, matched := e.evalRules("read_file", realPath); matched {
		if dec != DecisionAllow {
			return "", &PolicyError{Decision: dec, Action: "read_file", Target: targetPath, Reason: "matched CLI permission rule"}
		}
		return abs, nil
	}
	// Write implies read in Antigravity permission rules.
	if dec, matched := e.evalRules("write_file", realPath); matched && dec == DecisionAllow {
		return abs, nil
	}
	if !e.isInsideWorkspace(realPath) && !e.isTempOrScratch(realPath) && !e.allowNonWorkspaceAccess {
		return "", &PolicyError{
			Decision: DecisionAsk,
			Action:   "read_file",
			Target:   targetPath,
			Reason:   "reading files outside active workspace requires host approval",
		}
	}
	return abs, nil
}

// ResolveAndCheckWrite resolves path against baseDir and enforces workspace & .git confinement.
func (e *Engine) ResolveAndCheckWrite(baseDir, targetPath string) (string, error) {
	abs, realPath, err := resolveWithSymlinks(baseDir, targetPath)
	if err != nil {
		return "", err
	}
	if e.isSensitivePath(realPath) || isProtectedGitInternal(realPath) || isProtectedSystemRoot(realPath) {
		return "", &PolicyError{
			Decision: DecisionDeny,
			Action:   "write_file",
			Target:   targetPath,
			Reason:   "writing to protected system directories, .git internals, or credential paths is blocked",
		}
	}
	// Deny read implies deny write.
	if dec, matched := e.evalRules("read_file", realPath); matched && dec == DecisionDeny {
		return "", &PolicyError{Decision: DecisionDeny, Action: "write_file", Target: targetPath, Reason: "read_file deny rule blocks write_file"}
	}
	if dec, matched := e.evalRules("write_file", realPath); matched {
		if dec != DecisionAllow {
			return "", &PolicyError{Decision: dec, Action: "write_file", Target: targetPath, Reason: "matched CLI permission rule"}
		}
		return abs, nil
	}
	if !e.isInsideWorkspace(realPath) && !e.allowNonWorkspaceAccess {
		return "", &PolicyError{
			Decision: DecisionAsk,
			Action:   "write_file",
			Target:   targetPath,
			Reason:   "writing outside active workspace is blocked in PTC sandbox",
		}
	}
	return abs, nil
}

var destructiveCmdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:sudo|su|doas)\b`),
	regexp.MustCompile(`(?i)\brm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+(?:/|~|\.\.|[a-zA-Z]:\\)`),
	regexp.MustCompile(`(?i)\bRemove-Item\b.*-Recurse.*-Force\s+(?:/|~|\$HOME|\$env:USERPROFILE|[a-zA-Z]:\\$)`),
	regexp.MustCompile(`(?i)\bgit\s+(?:push\b.*--force|push\b.*-f\b|reset\s+--hard|clean\s+-[a-zA-Z]*f)`),
	regexp.MustCompile(`(?i)\b(?:mkfs|fdisk|diskpart|shutdown|reboot|halt)\b`),
	regexp.MustCompile(`(?i)\b(?:curl|wget|irm|Invoke-RestMethod|iwr)\b.*\|\s*(?:sh|bash|zsh|pwsh|powershell|iex)\b`),
}

// CheckCommand validates a shell command against destructive guards and CLI permission rules.
func (e *Engine) CheckCommand(cmdLine string) error {
	trimmed := strings.TrimSpace(cmdLine)
	if trimmed == "" {
		return errors.New("command cannot be empty")
	}
	for _, re := range destructiveCmdPatterns {
		if re.MatchString(trimmed) {
			return &PolicyError{
				Decision: DecisionAsk,
				Action:   "command",
				Target:   trimmed,
				Reason:   "destructive, privilege-escalating, or remote-pipe shell command must go through native interactive approval",
			}
		}
	}
	if dec, matched := e.evalRules("command", trimmed); matched {
		if dec != DecisionAllow {
			return &PolicyError{
				Decision: dec,
				Action:   "command",
				Target:   trimmed,
				Reason:   "matched CLI command permission rule",
			}
		}
	}
	return nil
}

// CheckMCP validates external MCP bridge invocations against mcp(server/tool) rules.
func (e *Engine) CheckMCP(server, tool string) error {
	target := server + "/" + tool
	if dec, matched := e.evalRules("mcp", target); matched && dec == DecisionDeny {
		return &PolicyError{
			Decision: DecisionDeny,
			Action:   "mcp",
			Target:   target,
			Reason:   "matched CLI mcp deny rule",
		}
	}
	return nil
}

func (e *Engine) evalRules(action, target string) (Decision, bool) {
	if matchRuleSet(e.denyRules, action, target, e.workspaceRoots) {
		return DecisionDeny, true
	}
	if matchRuleSet(e.askRules, action, target, e.workspaceRoots) {
		return DecisionAsk, true
	}
	if matchRuleSet(e.allowRules, action, target, e.workspaceRoots) {
		return DecisionAllow, true
	}
	return "", false
}

func parseRuleList(rawList []string) []parsedRule {
	var out []parsedRule
	for _, item := range rawList {
		item = strings.TrimSpace(item)
		openIdx := strings.IndexByte(item, '(')
		if openIdx <= 0 || !strings.HasSuffix(item, ")") {
			continue
		}
		action := strings.TrimSpace(item[:openIdx])
		target := strings.TrimSpace(item[openIdx+1 : len(item)-1])
		if action == "" || target == "" {
			continue
		}
		r := parsedRule{action: action, target: target}
		if strings.HasPrefix(target, "regex:") {
			pat := strings.TrimPrefix(target, "regex:")
			if compiled, err := regexp.Compile("^(?:" + pat + ")$"); err == nil {
				r.isRegex = true
				r.re = compiled
			} else {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func matchRuleSet(rules []parsedRule, action, target string, roots []string) bool {
	for _, r := range rules {
		if r.action != action {
			continue
		}
		if r.target == "*" {
			return true
		}
		switch action {
		case "command":
			if r.isRegex {
				if r.re.MatchString(target) {
					return true
				}
				continue
			}
			if target == r.target || strings.HasPrefix(target, r.target+" ") {
				return true
			}
		case "mcp":
			if r.target == target {
				return true
			}
			if strings.HasSuffix(r.target, "/*") {
				prefix := strings.TrimSuffix(r.target, "*")
				if strings.HasPrefix(target, prefix) {
					return true
				}
			}
		case "read_file", "write_file":
			normTarget := normalizeRulePath(target)
			rulePath := r.target
			if !strings.HasPrefix(rulePath, "/") && len(roots) > 0 {
				rulePath = filepath.Join(roots[0], filepath.FromSlash(rulePath))
			}
			normRule := strings.TrimSuffix(normalizeRulePath(rulePath), "/")
			if normTarget == normRule || strings.HasPrefix(normTarget, normRule+"/") {
				return true
			}
		}
	}
	return false
}

func (e *Engine) isInsideWorkspace(realPath string) bool {
	norm := normalizeComparePath(realPath)
	for _, root := range e.workspaceRoots {
		normRoot := strings.TrimSuffix(normalizeComparePath(root), "/")
		if norm == normRoot || strings.HasPrefix(norm, normRoot+"/") {
			return true
		}
	}
	return false
}

func (e *Engine) isTempOrScratch(realPath string) bool {
	norm := normalizeComparePath(realPath)
	tmp := strings.TrimSuffix(normalizeComparePath(os.TempDir()), "/")
	if tmp != "" && (norm == tmp || strings.HasPrefix(norm, tmp+"/")) {
		return true
	}
	home := userHomeDir()
	if home != "" {
		brain := strings.TrimSuffix(normalizeComparePath(filepath.Join(home, ".gemini")), "/")
		if norm == brain || strings.HasPrefix(norm, brain+"/") {
			return true
		}
	}
	return false
}

func (e *Engine) isSensitivePath(realPath string) bool {
	norm := normalizeComparePath(realPath)
	for _, s := range e.sensitiveDirs {
		ns := strings.TrimSuffix(normalizeComparePath(s), "/")
		if norm == ns || strings.HasPrefix(norm, ns+"/") {
			return true
		}
	}
	return false
}

func isProtectedGitInternal(realPath string) bool {
	norm := "/" + strings.Trim(normalizeRulePath(realPath), "/") + "/"
	if strings.Contains(norm, "/.git/hooks/") || strings.HasSuffix(norm, "/.git/config/") || strings.Contains(norm, "/.git/objects/") {
		return true
	}
	return false
}

func isProtectedSystemRoot(realPath string) bool {
	norm := strings.TrimSuffix(normalizeRulePath(realPath), "/")
	for _, sys := range []string{"/windows", "/program files", "/program files (x86)", "/etc", "/usr", "/bin", "/sbin", "/boot", "/system"} {
		if norm == sys || strings.HasPrefix(norm, sys+"/") {
			return true
		}
	}
	return false
}

func resolveWithSymlinks(baseDir, p string) (absPath string, realPath string, err error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", errors.New("path cannot be empty")
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) || p == "~" {
		if h := userHomeDir(); h != "" {
			if len(p) == 1 {
				p = h
			} else {
				p = filepath.Join(h, p[2:])
			}
		}
	}
	if filepath.IsAbs(p) {
		absPath = filepath.Clean(p)
	} else {
		if baseDir == "" {
			baseDir, _ = os.Getwd()
		}
		absPath = filepath.Clean(filepath.Join(baseDir, p))
	}
	realPath, err = evalSymlinksExisting(absPath)
	if err != nil {
		return absPath, absPath, nil
	}
	return absPath, realPath, nil
}

// evalSymlinksExisting walks up to the deepest existing ancestor, resolves symlinks, and re-joins the non-existent tail.
func evalSymlinksExisting(absPath string) (string, error) {
	cur := absPath
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return absPath, err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func normalizeComparePath(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return s
}

// normalizeRulePath strips Windows drive letters and converts backslashes to forward slashes per Antigravity CLI docs.
func normalizeRulePath(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	if len(s) >= 2 && s[1] == ':' {
		s = s[2:]
	}
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

func userHomeDir() string {
	if runtime.GOOS == "windows" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			return up
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}
