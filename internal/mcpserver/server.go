// Package mcpserver implements a JSON-RPC 2.0 MCP stdio server supporting both
// newline-delimited JSON (NDJSON) and Content-Length header framing.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"

	ptcruntime "github.com/aliothmoon/agy-ptc/internal/runtime"
)

const (
	ServerName             = "agy-ptc"
	ServerVersion          = "0.1.0"
	DefaultProtocolVersion = "2025-03-26"
)

var supportedProtocols = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
	"2024-11-05": true,
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server speaks MCP JSON-RPC 2.0 over stdio (supporting both NDJSON and Content-Length framing).
type Server struct {
	Engine *ptcruntime.Engine
	wMu    sync.Mutex
}

// NewServer wraps an Engine in an MCP stdio server.
func NewServer(engine *ptcruntime.Engine) *Server {
	return &Server{Engine: engine}
}

// Serve reads JSON-RPC messages from in and writes responses to out until EOF or ctx cancellation.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	var buf []byte
	tmp := make([]byte, 32*1024)
	var inFlight sync.WaitGroup

	writeResp := func(payload any, useContentLength bool) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		s.wMu.Lock()
		defer s.wMu.Unlock()
		if useContentLength {
			header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(raw))
			_, _ = out.Write([]byte(header))
			_, _ = out.Write(raw)
		} else {
			_, _ = out.Write(raw)
			_, _ = out.Write([]byte("\n"))
		}
	}

	dispatch := func(rawJSON []byte, useContentLength bool) {
		trimmed := bytes.TrimSpace(rawJSON)
		if len(trimmed) == 0 {
			return
		}
		inFlight.Add(1)
		go func(data []byte) {
			defer inFlight.Done()
			if data[0] == '[' {
				var batch []jsonRPCRequest
				if err := json.Unmarshal(data, &batch); err != nil || len(batch) == 0 {
					writeResp(jsonRPCResponse{
						JSONRPC: "2.0",
						ID:      json.RawMessage("null"),
						Error:   &rpcError{Code: -32600, Message: "Invalid Request"},
					}, useContentLength)
					return
				}
				var replies []jsonRPCResponse
				for _, req := range batch {
					if resp := s.handleSingle(ctx, req); resp != nil {
						replies = append(replies, *resp)
					}
				}
				if len(replies) > 0 {
					writeResp(replies, useContentLength)
				}
				return
			}

			var req jsonRPCRequest
			if err := json.Unmarshal(data, &req); err != nil {
				writeResp(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &rpcError{Code: -32700, Message: "Parse error"},
				}, useContentLength)
				return
			}
			if resp := s.handleSingle(ctx, req); resp != nil {
				writeResp(*resp, useContentLength)
			}
		}(append([]byte(nil), trimmed...))
	}

	drain := func() {
		for len(buf) > 0 {
			// Skip leading CR/LF
			trimIdx := 0
			for trimIdx < len(buf) && (buf[trimIdx] == '\n' || buf[trimIdx] == '\r') {
				trimIdx++
			}
			if trimIdx > 0 {
				buf = buf[trimIdx:]
				if len(buf) == 0 {
					break
				}
			}

			probeLen := min(len(buf), 16)
			probe := strings.ToLower(string(buf[:probeLen]))
			if strings.HasPrefix(probe, "content-length:") || strings.HasPrefix(probe, "content-type:") {
				sepIdx := bytes.Index(buf, []byte("\r\n\r\n"))
				sepLen := 4
				if sepIdx < 0 {
					sepIdx = bytes.Index(buf, []byte("\n\n"))
					sepLen = 2
				}
				if sepIdx < 0 {
					break
				}
				hdr := string(buf[:sepIdx])
				cLen := parseContentLength(hdr)
				if cLen < 0 {
					buf = buf[sepIdx+sepLen:]
					continue
				}
				bodyStart := sepIdx + sepLen
				if len(buf) < bodyStart+cLen {
					break
				}
				body := buf[bodyStart : bodyStart+cLen]
				buf = buf[bodyStart+cLen:]
				dispatch(body, true)
				continue
			}

			nl := bytes.IndexByte(buf, '\n')
			if nl < 0 {
				break
			}
			line := buf[:nl]
			buf = buf[nl+1:]
			dispatch(line, false)
		}
	}

	for {
		n, err := in.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			drain()
		}
		if err != nil {
			if rem := bytes.TrimSpace(buf); len(rem) > 0 {
				low := strings.ToLower(string(rem[:min(len(rem), 16)]))
				if !strings.HasPrefix(low, "content-length:") && !strings.HasPrefix(low, "content-type:") {
					dispatch(rem, false)
				}
			}
			inFlight.Wait()
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleSingle(ctx context.Context, req jsonRPCRequest) *jsonRPCResponse {
	// Notifications (missing ID) must not emit a response.
	if len(req.ID) == 0 {
		return nil
	}
	if req.Method == "" {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32600, Message: "Invalid Request: missing method"},
		}
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion  string `json:"protocolVersion"`
			RootPath         string `json:"rootPath"`
			RootURI          string `json:"rootUri"`
			WorkspaceFolders []struct {
				URI  string `json:"uri"`
				Name string `json:"name"`
			} `json:"workspaceFolders"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if ws := extractInitWorkspace(params.RootPath, params.RootURI, params.WorkspaceFolders); ws != "" && s.Engine != nil {
			s.Engine.SetClientWorkspace(ws)
		}
		proto := DefaultProtocolVersion
		if supportedProtocols[strings.TrimSpace(params.ProtocolVersion)] {
			proto = strings.TrimSpace(params.ProtocolVersion)
		}
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": proto,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    ServerName,
					"version": ServerVersion,
				},
			},
		}

	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}

	case "tools/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": []any{execToolDefinition()},
			},
		}

	case "resources/list":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": []any{}}}

	case "resources/templates/list":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resourceTemplates": []any{}}}

	case "prompts/list":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"prompts": []any{}}}

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "Invalid params"},
			}
		}
		if callParams.Name != "exec" {
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "Unknown tool: " + callParams.Name},
			}
		}
		var in ptcruntime.ExecInput
		if len(callParams.Arguments) > 0 {
			// Accept either flat object `{ code, payloads, cwd, title, timeoutMs }` or bare string fallback
			if callParams.Arguments[0] == '"' {
				var rawCode string
				if json.Unmarshal(callParams.Arguments, &rawCode) == nil {
					in.Code = rawCode
				}
			} else {
				_ = json.Unmarshal(callParams.Arguments, &in)
			}
		}
		outText, isErr := s.Engine.RunMCPExec(ctx, in)
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": outText},
				},
				"isError": isErr,
			},
		}

	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

func execToolDefinition() map[string]any {
	return map[string]any{
		"name":        "exec",
		"description": "Execute a TypeScript program in an isolated Programmatic Tool Calling (PTC) sandbox to compose search, AST outline, file reads, multi-file atomic edits, transactions (edit -> concurrent verify -> auto-rollback), read-only git, shell checks, session state, and external MCP calls in ONE turn. Intermediate data stays in sandbox memory; return a compact structured summary.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code": map[string]any{
					"type":        "string",
					"description": "TypeScript async function body (top-level `await`, `print()`/`console.log()` stdout, and `return` supported; do not wrap in `async function`). Globals: `fs`, `search`, `ast`, `edit`, `tx`, `git`, `sh`, `state`, `tools`, `mcp`, `P`/`payloads`, plus `Buffer`, `TextEncoder`/`TextDecoder`, `btoa`/`atob`, `path`, `crypto`, `URL`/`URLSearchParams`, `process`/`os`, `setTimeout`, `structuredClone`.",
				},
				"payloads": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "Named multiline text/code blocks passed outside `code` to avoid escaping issues; read inside `code` via `P.key` or `payloads.key`.",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Absolute path of the active workspace root directory (required so relative paths in fs/search/ast/edit/git/sh resolve against your project).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Short 2-6 word summary of what this program does (shown in status bar and logs).",
				},
				"timeoutMs": map[string]any{
					"type":        "integer",
					"minimum":     500,
					"maximum":     120000,
					"description": "Optional execution deadline in milliseconds (default 20000, max 120000). Subprocess trees are automatically killed on timeout. For >15s builds/tests or daemons, use built-in run_command + manage_task.",
				},
				"maxOutputBytes": map[string]any{
					"type":        "integer",
					"minimum":     1000,
					"maximum":     524288,
					"description": "Optional max UTF-8 bytes returned inline (default 9600 to stay below Antigravity CLI's ~10KB output.txt spill cliff; increase up to 524288 when larger raw output is explicitly desired).",
				},
			},
			"required": []string{"code", "cwd"},
		},
	}
}

func extractInitWorkspace(rootPath, rootURI string, folders []struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}) string {
	if strings.TrimSpace(rootPath) != "" {
		return strings.TrimSpace(rootPath)
	}
	if p := fileURIToPath(rootURI); p != "" {
		return p
	}
	for _, f := range folders {
		if p := fileURIToPath(f.URI); p != "" {
			return p
		}
	}
	return ""
}

func fileURIToPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "file://") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p
}

func parseContentLength(hdr string) int {
	for _, line := range strings.Split(hdr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			val := strings.TrimSpace(line[len("content-length:"):])
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				return n
			}
		}
	}
	return -1
}
