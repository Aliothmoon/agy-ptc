// Package runtime coordinates workspace capability bindings, sandbox execution,
// inline-safe response budgeting, live TUI status updates, and audit logging.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aliothmoon/agy-ptc/internal/audit"
	"github.com/aliothmoon/agy-ptc/internal/capabilities"
	"github.com/aliothmoon/agy-ptc/internal/output"
	"github.com/aliothmoon/agy-ptc/internal/policy"
	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

// LiveStatus is written atomically to `${PLUGIN_DATA}/live-status.json` for `agy-ptc statusline`.
type LiveStatus struct {
	Active       bool   `json:"active"`
	Title        string `json:"title,omitempty"`
	CurrentOp    string `json:"currentOp,omitempty"`
	TotalRuns    int64  `json:"totalRuns"`
	TotalHostOps int64  `json:"totalHostOps"`
	LastDurMs    int64  `json:"lastDurationMs"`
	LastStatus   string `json:"lastStatus"`
	UpdatedAt    string `json:"updatedAt"`
}

// Engine manages workspace-scoped capability instances, state stores, audit logs, and PTC program execution.
type Engine struct {
	dataDir         string
	defaultCwd      string
	clientWorkspace string
	auditor         *audit.Logger
	mu              sync.Mutex
	stores          map[string]*capabilities.StateStore
	bridges         map[string]*capabilities.MCPBridge
	totalRuns       atomic.Int64
	totalHostOps    atomic.Int64
}

// NewEngine creates a PTC runtime engine.
func NewEngine(dataDir, defaultCwd string) *Engine {
	if defaultCwd == "" {
		defaultCwd, _ = os.Getwd()
	}
	if dataDir == "" {
		if envDir := os.Getenv("PLUGIN_DATA"); envDir != "" {
			dataDir = envDir
		}
	}
	if dataDir != "" {
		_ = os.MkdirAll(dataDir, 0o755)
	}
	return &Engine{
		dataDir:    dataDir,
		defaultCwd: defaultCwd,
		auditor:    audit.NewLogger(dataDir),
		stores:     map[string]*capabilities.StateStore{},
		bridges:    map[string]*capabilities.MCPBridge{},
	}
}

// Close shuts down pooled external MCP processes.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, br := range e.bridges {
		br.Close()
	}
}

// ExecInput represents the flat MCP arguments passed to `exec`.
type ExecInput struct {
	Code       string            `json:"code"`
	Payloads   map[string]string `json:"payloads,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Title      string            `json:"title,omitempty"`
	TimeoutMs  int               `json:"timeoutMs,omitempty"`
	TimeoutSec int               `json:"timeoutSec,omitempty"`
}

// SetClientWorkspace records a workspace root reported by the MCP client during `initialize`.
func (e *Engine) SetClientWorkspace(ws string) {
	ws = strings.TrimSpace(ws)
	if ws == "" {
		return
	}
	e.mu.Lock()
	e.clientWorkspace = ws
	e.mu.Unlock()
}

// RunMCPExec executes a PTC TypeScript program and returns an inline-safe (`<= 8,800 B` UTF-8) formatted response.
func (e *Engine) RunMCPExec(ctx context.Context, in ExecInput) (text string, isError bool) {
	cwd, cwdErr := e.resolveEffectiveCwd(in.Cwd)
	if cwdErr != nil {
		msg := "[ptc workspace error]\n" + cwdErr.Error()
		e.auditor.Record(audit.Entry{
			Title:         in.Title,
			Status:        "runtime_error",
			ErrorCategory: "missing_workspace_cwd",
			ErrorMessage:  cwdErr.Error(),
			Code:          in.Code,
		})
		return msg, true
	}

	pol := policy.NewEngine(cwd)
	store, bridge := e.workspaceResources(cwd, pol)

	fsSearch := &capabilities.FSSearchBinder{Policy: pol}
	astBinder := &capabilities.ASTBinder{Policy: pol}
	shellGit := &capabilities.ShellGitBinder{Policy: pol}
	editTx := &capabilities.EditTxBinder{Policy: pol, ShellRunner: shellGit}
	stateBinder := &capabilities.StateBinder{Store: store}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "ptc_exec"
	}

	timeout := sandbox.EffectiveDefaultTimeout()
	if in.TimeoutMs > 0 {
		timeout = time.Duration(in.TimeoutMs) * time.Millisecond
	} else if in.TimeoutSec > 0 {
		timeout = time.Duration(in.TimeoutSec) * time.Second
	}

	e.writeLiveStatus(LiveStatus{
		Active:       true,
		Title:        title,
		CurrentOp:    "compiling",
		TotalRuns:    e.totalRuns.Load(),
		TotalHostOps: e.totalHostOps.Load(),
		LastStatus:   "running",
	})

	req := sandbox.ExecRequest{
		Title:    title,
		Code:     in.Code,
		Payloads: in.Payloads,
		Cwd:      cwd,
		Timeout:  timeout,
		OnOpStart: func(capability, target string, seq int) {
			cur := capability
			if target != "" {
				cur += "(" + target + ")"
			}
			e.writeLiveStatus(LiveStatus{
				Active:       true,
				Title:        title,
				CurrentOp:    fmt.Sprintf("#%d %s", seq, cur),
				TotalRuns:    e.totalRuns.Load(),
				TotalHostOps: e.totalHostOps.Load() + int64(seq),
				LastStatus:   "running",
			})
		},
	}

	res, execErr := sandbox.Execute(ctx, req, fsSearch, astBinder, shellGit, editTx, stateBinder, bridge)
	runs := e.totalRuns.Add(1)

	if execErr != nil {
		var ee *sandbox.ExecutionError
		if errors.As(execErr, &ee) {
			ops := e.totalHostOps.Add(int64(len(ee.Traces)))
			failedOps := countFailedTraces(ee.Traces)
			formatted := formatErrorEnvelope(ee)
			statusTag := ee.Phase + "_error"
			if ee.Phase == "timeout" {
				statusTag = "timeout"
			}
			e.auditor.Record(audit.Entry{
				Cwd:           cwd,
				Title:         title,
				Status:        statusTag,
				ErrorCategory: audit.ClassifyError(ee.Phase, ee.Message, ee.Traces),
				ErrorMessage:  ee.Message,
				DurationMs:    ee.DurationMs,
				HostOps:       len(ee.Traces),
				FailedOps:     failedOps,
				ResponseBytes: len(formatted),
				PayloadKeys:   audit.PayloadKeyLengths(in.Payloads),
				Traces:        ee.Traces,
				Logs:          ee.Logs,
				Code:          in.Code,
			})
			e.writeLiveStatus(LiveStatus{
				Active:       false,
				Title:        title,
				TotalRuns:    runs,
				TotalHostOps: ops,
				LastDurMs:    ee.DurationMs,
				LastStatus:   "error",
			})
			return formatted, true
		}
		formatted := output.TruncateWithByteNotice("Error: "+execErr.Error(), output.DefaultInlineMaxBytes)
		e.auditor.Record(audit.Entry{
			Cwd:           cwd,
			Title:         title,
			Status:        "runtime_error",
			ErrorCategory: audit.ClassifyError("runtime", execErr.Error(), nil),
			ErrorMessage:  execErr.Error(),
			ResponseBytes: len(formatted),
			PayloadKeys:   audit.PayloadKeyLengths(in.Payloads),
			Code:          in.Code,
		})
		e.writeLiveStatus(LiveStatus{
			Active:       false,
			Title:        title,
			TotalRuns:    runs,
			TotalHostOps: e.totalHostOps.Load(),
			LastStatus:   "error",
		})
		return formatted, true
	}

	ops := e.totalHostOps.Add(int64(len(res.Traces)))
	failedOps := countFailedTraces(res.Traces)
	formatted, autoSpilled := formatSuccessEnvelope(res, store)

	statusTag := "ok"
	errCat := ""
	if failedOps > 0 {
		// Even if guest JS caught the error via try/catch, record it as partial_op_error so friction is visible!
		statusTag = "partial_op_error"
		errCat = audit.ClassifyError("", "", res.Traces)
	}
	e.auditor.Record(audit.Entry{
		Cwd:           cwd,
		Title:         title,
		Status:        statusTag,
		ErrorCategory: errCat,
		DurationMs:    res.DurationMs,
		HostOps:       len(res.Traces),
		FailedOps:     failedOps,
		ResponseBytes: len(formatted),
		AutoSpilled:   autoSpilled,
		PayloadKeys:   audit.PayloadKeyLengths(in.Payloads),
		Traces:        res.Traces,
		Logs:          res.Logs,
		Code:          in.Code,
		ResultPreview: formatted,
	})

	e.writeLiveStatus(LiveStatus{
		Active:       false,
		Title:        title,
		TotalRuns:    runs,
		TotalHostOps: ops,
		LastDurMs:    res.DurationMs,
		LastStatus:   "ok",
	})

	return formatted, false
}

func countFailedTraces(traces []sandbox.OpTrace) int {
	n := 0
	for _, t := range traces {
		if t.Status == "error" {
			n++
		}
	}
	return n
}

// resolveEffectiveCwd ensures relative paths (`"go.mod"`, `"internal/..."`) NEVER accidentally resolve against
// `~/.gemini/config/plugins/agy-ptc` (which is the OS working directory when Antigravity spawns a plugin MCP server).
func (e *Engine) resolveEffectiveCwd(explicitCwd string) (string, error) {
	if c := strings.TrimSpace(explicitCwd); c != "" {
		if abs, err := filepath.Abs(c); err == nil {
			return abs, nil
		}
		return c, nil
	}

	e.mu.Lock()
	clientWs := e.clientWorkspace
	e.mu.Unlock()
	if clientWs != "" {
		if abs, err := filepath.Abs(clientWs); err == nil {
			return abs, nil
		}
		return clientWs, nil
	}

	// If defaultCwd is NOT the plugin installation directory (e.g. standalone `agy-ptc run` in a repo), use it directly.
	if e.defaultCwd != "" && !isPluginInstallDir(e.defaultCwd) {
		if abs, err := filepath.Abs(e.defaultCwd); err == nil {
			return abs, nil
		}
		return e.defaultCwd, nil
	}

	// Fallback to the active workspace recorded by the SessionStart hook (`active-workspace.json`).
	if hinted := e.readWorkspaceHint(); hinted != "" {
		return hinted, nil
	}

	return "", errors.New("WorkspaceError: 'cwd' parameter was omitted and MCP process working directory is the plugin folder (~/.gemini/config/plugins/agy-ptc). Pass your active workspace path in 'cwd' (e.g. cwd: 'F:/Project/...') so relative paths resolve against your project.")
}

func (e *Engine) readWorkspaceHint() string {
	var dirs []string
	if e.dataDir != "" {
		dirs = append(dirs, e.dataDir)
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		dirs = append(dirs,
			filepath.Join(h, ".gemini", "antigravity-cli", "plugin_data", "agy-ptc"),
			filepath.Join(h, ".gemini", "antigravity", "plugin_data", "agy-ptc"),
		)
	}
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(d, "active-workspace.json"))
		if err != nil {
			continue
		}
		var parsed struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(raw, &parsed) == nil && strings.TrimSpace(parsed.Cwd) != "" {
			if fi, stErr := os.Stat(parsed.Cwd); stErr == nil && fi.IsDir() && !isPluginInstallDir(parsed.Cwd) {
				return parsed.Cwd
			}
		}
	}
	return ""
}

func isPluginInstallDir(p string) bool {
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
	if pr := strings.TrimSpace(os.Getenv("PLUGIN_ROOT")); pr != "" {
		normPR := strings.ToLower(filepath.ToSlash(filepath.Clean(pr)))
		if norm == normPR || strings.HasPrefix(norm, normPR+"/") {
			return true
		}
	}
	return strings.Contains(norm, "/.gemini/config/plugins/agy-ptc") || strings.Contains(norm, "/.agents/plugins/agy-ptc")
}

func (e *Engine) workspaceResources(cwd string, pol *policy.Engine) (*capabilities.StateStore, *capabilities.MCPBridge) {
	key := strings.ToLower(filepath.ToSlash(filepath.Clean(cwd)))
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.stores[key]
	if !ok {
		st = capabilities.NewStateStore(e.dataDir, cwd)
		e.stores[key] = st
	}
	br, ok := e.bridges[key]
	if !ok {
		br = capabilities.NewMCPBridge(pol, cwd)
		e.bridges[key] = br
	} else {
		br.Policy = pol
	}
	return st, br
}

func (e *Engine) writeLiveStatus(st LiveStatus) {
	if e.dataDir == "" {
		return
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	p := filepath.Join(e.dataDir, "live-status.json")
	tmp := fmt.Sprintf("%s.tmp.%d", p, os.Getpid())
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func formatSuccessEnvelope(res *sandbox.ExecResult, store *capabilities.StateStore) (string, bool) {
	header := fmt.Sprintf("[ptc ok | %d host ops | %dms]", len(res.Traces), res.DurationMs)
	var sections []string
	sections = append(sections, header)

	if len(res.Logs) > 0 {
		logText := "Logs:\n" + strings.Join(res.Logs, "\n")
		sections = append(sections, output.TruncateWithByteNotice(logText, 1800))
	}

	serialized := output.MarshalResultCompact(res.Value)
	currentUsed := 0
	for _, s := range sections {
		currentUsed += len(s) + 2
	}
	availForResult := output.DefaultInlineMaxBytes - currentUsed - 40
	if availForResult < 800 {
		availForResult = 800
	}

	autoSpilled := false
	if len(serialized) <= availForResult {
		sections = append(sections, "Result:\n"+serialized)
	} else {
		autoSpilled = true
		// Auto-save full value in session state so model can slice/filter in next turn with 0 output.txt disk spills!
		spillKey := store.SaveAutoSpill(res.Value)
		hint := ""
		if spillKey != "" {
			hint = fmt.Sprintf("; full object saved in state.get('%s') and state.get('__last_result') — filter/slice in code instead of dumping raw data", spillKey)
		}
		preview := output.TruncateHeadTailBytes("Result:\n"+serialized, availForResult, hint)
		sections = append(sections, preview)
	}

	joined := strings.Join(sections, "\n\n")
	if len(joined) <= output.DefaultInlineMaxBytes {
		return joined, autoSpilled
	}
	return output.TruncateWithByteNotice(joined, output.DefaultInlineMaxBytes), autoSpilled
}

func formatErrorEnvelope(ee *sandbox.ExecutionError) string {
	header := fmt.Sprintf("[ptc %s error | %d ops run | %dms]\n%s", ee.Phase, len(ee.Traces), ee.DurationMs, ee.Message)
	var sections []string
	sections = append(sections, output.TruncateWithByteNotice(header, 4800))

	if ee.Phase == "timeout" {
		var culprits []string
		for _, op := range ee.TimedOutOps {
			culprits = append(culprits, "  - "+op)
		}
		for _, t := range ee.Traces {
			if t.Status == "error" && (strings.Contains(strings.ToUpper(t.Error), "TIMED OUT") || strings.Contains(strings.ToLower(t.Error), "deadline") || strings.Contains(strings.ToLower(t.Error), "canceled")) {
				item := fmt.Sprintf("#%d %s", t.Seq, t.Capability)
				if t.Target != "" {
					item += "(" + t.Target + ")"
				}
				errPreview := output.TruncateWithByteNotice(t.Error, 900)
				culprits = append(culprits, fmt.Sprintf("  - %s [%dms]: %s", item, t.DurationMs, errPreview))
			}
		}
		if len(culprits) > 0 {
			sections = append(sections, "Interrupted / timed-out operations (child process trees terminated):\n"+strings.Join(culprits, "\n"))
		}
		sections = append(sections, "Timeout recovery hint:\n  Keep ptc_exec fast (<15s). Pass a larger `timeoutMs` (up to 120000) for moderate checks, or use built-in `run_command` (`WaitMsBeforeAsync: 500..2000` / `IsDaemon: true`) + `manage_task` for >15s builds, full test suites, and dev servers.")
	}

	// Like pi-fabric: list completed host calls before failure so the model doesn't blindly repeat non-idempotent side effects!
	if len(ee.Traces) > 0 {
		var completed []string
		for _, t := range ee.Traces {
			if t.Status == "ok" {
				item := t.Capability
				if t.Target != "" {
					item += "(" + t.Target + ")"
				}
				completed = append(completed, item)
			}
		}
		if len(completed) > 0 {
			if len(completed) > 20 {
				extra := len(completed) - 20
				completed = append(completed[:20], fmt.Sprintf("...+%d more", extra))
			}
			sections = append(sections, "Completed ops before error (note: uncommitted tx.* changes were auto-rolled back):\n  "+strings.Join(completed, ", "))
		}
	}

	if len(ee.Logs) > 0 {
		sections = append(sections, output.TruncateWithByteNotice("Logs:\n"+strings.Join(ee.Logs, "\n"), 1400))
	}

	return output.FitSectionsToByteBudget(sections, output.DefaultInlineMaxBytes, "\n\n")
}
