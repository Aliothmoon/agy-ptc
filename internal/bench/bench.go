// Package bench analyzes Antigravity CLI session transcripts to measure turn and spill efficiency.
package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// SessionReport summarizes tool-calling and turn efficiency metrics from an Antigravity transcript.jsonl.
type SessionReport struct {
	SessionID       string `json:"sessionId"`
	PlannerTurns    int    `json:"plannerTurns"`
	TotalToolCalls  int    `json:"totalToolCalls"`
	PTCCalls        int    `json:"ptcCalls"`
	MCPSpillCount   int    `json:"mcpSpillCount"`
	EditFileCalls   int    `json:"editFileCalls"`
	RunCommandCalls int    `json:"runCommandCalls"`
	ReadFileCalls   int    `json:"readFileCalls"`
}

// AnalyzeLocal scans recent Antigravity CLI transcripts and prints an aggregate efficiency table.
func AnalyzeLocal(recent int) (string, error) {
	home := userHome()
	if home == "" {
		return "", fmt.Errorf("cannot locate user home directory")
	}
	roots := []string{
		filepath.Join(home, ".gemini", "antigravity-cli", "brain"),
		filepath.Join(home, ".gemini", "antigravity", "brain"),
	}

	type transcriptEntry struct {
		path    string
		session string
		modTime int64
	}
	var found []transcriptEntry
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			tp := filepath.Join(root, e.Name(), ".system_generated", "logs", "transcript.jsonl")
			if fi, err := os.Stat(tp); err == nil && !fi.IsDir() {
				found = append(found, transcriptEntry{
					path:    tp,
					session: e.Name(),
					modTime: fi.ModTime().UnixNano(),
				})
			}
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].modTime > found[j].modTime })
	if recent > 0 && len(found) > recent {
		found = found[:recent]
	}
	if len(found) == 0 {
		return "No local transcript.jsonl files found.\n", nil
	}

	var agg SessionReport
	analyzed := 0
	for _, item := range found {
		rep, err := AnalyzeFile(item.path)
		if err != nil {
			continue
		}
		analyzed++
		agg.PlannerTurns += rep.PlannerTurns
		agg.TotalToolCalls += rep.TotalToolCalls
		agg.PTCCalls += rep.PTCCalls
		agg.MCPSpillCount += rep.MCPSpillCount
		agg.EditFileCalls += rep.EditFileCalls
		agg.RunCommandCalls += rep.RunCommandCalls
		agg.ReadFileCalls += rep.ReadFileCalls
	}

	var b strings.Builder
	fmt.Fprintf(&b, "AgyPTC Local Transcript Benchmark (%d sessions analyzed)\n", analyzed)
	if skipped := len(found) - analyzed; skipped > 0 {
		fmt.Fprintf(&b, "  - Unreadable (skipped)  : %d\n", skipped)
	}
	fmt.Fprintf(&b, "  - Total LLM turns       : %d\n", agg.PlannerTurns)
	fmt.Fprintf(&b, "  - Total tool calls      : %d\n", agg.TotalToolCalls)
	fmt.Fprintf(&b, "  - PTC exec calls        : %d\n", agg.PTCCalls)
	fmt.Fprintf(&b, "  - Native read_file      : %d\n", agg.ReadFileCalls)
	fmt.Fprintf(&b, "  - Native edit_file      : %d\n", agg.EditFileCalls)
	fmt.Fprintf(&b, "  - Native run_command    : %d\n", agg.RunCommandCalls)
	fmt.Fprintf(&b, "  - MCP output.txt spills : %d\n", agg.MCPSpillCount)
	if agg.PlannerTurns > 0 {
		ratio := float64(agg.TotalToolCalls) / float64(agg.PlannerTurns)
		fmt.Fprintf(&b, "  - Avg tools / LLM turn  : %.2f\n", ratio)
	}
	return b.String(), nil
}

// AnalyzeFile parses a single transcript.jsonl file.
func AnalyzeFile(path string) (SessionReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionReport{}, err
	}
	defer f.Close()

	var rep SessionReport
	rep.SessionID = filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var step struct {
			Type      string `json:"type"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(line, &step) != nil {
			continue
		}
		if step.Type == "PLANNER_RESPONSE" {
			rep.PlannerTurns++
			for _, tc := range step.ToolCalls {
				rep.TotalToolCalls++
				switch {
				case strings.Contains(tc.Name, "ptc") && strings.HasSuffix(tc.Name, "exec"):
					rep.PTCCalls++
				case tc.Name == "edit_file":
					rep.EditFileCalls++
				case tc.Name == "run_command":
					rep.RunCommandCalls++
				case tc.Name == "read_file":
					rep.ReadFileCalls++
				}
			}
		}
		if strings.Contains(step.Content, "The output was large and was saved to:") {
			rep.MCPSpillCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return SessionReport{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return rep, nil
}

func userHome() string {
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
