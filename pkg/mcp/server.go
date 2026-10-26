// Package mcp provides a small JSON-RPC 2.0 MCP server over stdio.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

const ProtocolVersion = "2026-07-28"
const LegacyProtocolVersion = "2025-11-25"

// Tool describes one exposed MCP operation.
type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
}

// Handler implements one tool call.
type Handler func(context.Context, map[string]any) (any, error)

type toolEntry struct {
	Tool
	Handler Handler
}

// Server dispatches MCP JSON-RPC methods and named tool handlers.
type Server struct{ tools map[string]toolEntry }

// NewServer registers and validates handlers.
func NewServer(handlers map[string]Handler) *Server {
	s := &Server{tools: map[string]toolEntry{}}
	for name, handler := range handlers {
		if handler == nil {
			continue
		}
		tool := toolDefinition(name)
		s.tools[name] = toolEntry{Tool: tool, Handler: handler}
	}
	return s
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads and writes one JSON-RPC message per line until EOF or cancellation.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	writer := bufio.NewWriter(output)
	defer writer.Flush()
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if writeErr := s.write(writer, response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}); writeErr != nil {
				return writeErr
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			continue
		}
		resp, notify := s.handle(ctx, req)
		if !notify {
			if err := s.write(writer, resp); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, req request) (response, bool) {
	resp := response{JSONRPC: "2.0", ID: req.ID}
	if len(req.ID) == 0 {
		return response{}, true
	}
	if req.JSONRPC != "2.0" {
		resp.Error = &rpcError{Code: -32600, Message: "invalid request"}
		return resp, false
	}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{"protocolVersion": LegacyProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "astrix", "version": "0.1.0"}}
	case "server/discover":
		resp.Result = map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "astrix", "version": "0.1.0"}, "tools": s.toolList(), "ttlMs": 5000, "cacheScope": "global"}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolList(), "ttlMs": 5000, "cacheScope": "global"}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid tools/call parameters"}
			break
		}
		entry, ok := s.tools[params.Name]
		if !ok {
			resp.Error = &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
			break
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		value, err := entry.Handler(ctx, params.Arguments)
		if err != nil {
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
			break
		}
		text, err := json.Marshal(value)
		if err != nil {
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint(value)}}}
		} else {
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": value}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, false
}

func (s *Server) toolList() []Tool {
	entries := make([]Tool, 0, len(s.tools))
	for _, entry := range s.tools {
		entries = append(entries, entry.Tool)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
func (s *Server) write(w *bufio.Writer, resp response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err = w.Write(data); err != nil {
		return err
	}
	return w.WriteByte('\n')
}
func toolDefinition(name string) Tool {
	tool := Tool{
		Name:        name,
		Description: "Astrix tool. The server operates on the repository opened by the MCP client.",
		InputSchema: objectSchema(map[string]any{}),
	}
	switch name {
	case "astrix_get_blast_radius":
		tool.Title = "Analyze Repository Blast Radius"
		tool.Description = "Analyze the current staged and unstaged Git diff against HEAD in the opened repository and return changed symbols, their reverse-dependency impact, selected tests, and changed-file ranges. Use this before astrix_run_impacted_tests to inspect what the diff affects; this tool only reads repository state and does not run tests. It accepts no arguments."
		tool.Annotations = map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
		tool.OutputSchema = objectSchema(map[string]any{
			"impact": objectSchema(map[string]any{
				"changed_symbols":  stringArraySchema("Symbols whose source lines overlap the current diff."),
				"impacted_symbols": stringArraySchema("Symbols in the reverse-reference closure of changed symbols."),
				"tests":            stringArraySchema("Test symbols selected by the blast-radius analysis."),
			}),
			"changes": arraySchema(objectSchema(map[string]any{
				"path":     stringSchema("Changed repository-relative path."),
				"old_path": stringSchema("Previous path for a rename, when present."),
				"new_file": map[string]any{"type": "boolean", "description": "Whether this diff adds the file."},
				"deleted":  map[string]any{"type": "boolean", "description": "Whether this diff deletes the file."},
				"ranges": arraySchema(objectSchema(map[string]any{
					"start": map[string]any{"type": "integer", "description": "First changed line in the new file."},
					"end":   map[string]any{"type": "integer", "description": "Last changed line in the new file."},
				}, "start", "end")),
			}, "path", "ranges")),
		}, "impact", "changes")
	case "astrix_evaluate_policy":
		tool.Title = "Evaluate Proposed Action Policy"
		tool.Description = "Evaluate a proposed ActionIntent against the Astrixfile.ax policies in the opened repository and return allowed, denied, and requires_approval booleans with an optional reason. Use this before carrying out a proposed action; it evaluates policy but does not execute the action. For changed-file impact use astrix_get_blast_radius, and for selected test execution use astrix_run_impacted_tests. The required intent object contains actor.type, target.env, modified_files, and diff.modifies_tests (the string \"true\" or \"false\"); set approved=true only when approval has already been recorded."
		tool.InputSchema = objectSchema(map[string]any{
			"intent": objectSchema(map[string]any{
				"actor": objectSchema(map[string]any{
					"type": stringSchema("Actor category evaluated by policy, for example autonomous_llm or human."),
					"id":   stringSchema("Optional actor identifier."),
				}, "type"),
				"target": objectSchema(map[string]any{
					"env": stringSchema("Target environment, for example staging or production."),
				}, "env"),
				"modified_files": stringArraySchema("Repository-relative paths the proposed action would modify."),
				"diff": objectSchema(map[string]any{
					"modifies_tests": map[string]any{"type": "string", "enum": []string{"true", "false"}, "description": "Whether the proposed diff modifies test files, represented as the string true or false."},
				}, "modifies_tests"),
				"approved": map[string]any{"type": "boolean", "description": "Whether the required human approval has already been recorded."},
			}, "actor", "target", "modified_files", "diff"),
		}, "intent")
		tool.Annotations = map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
		tool.OutputSchema = objectSchema(map[string]any{
			"allowed":           map[string]any{"type": "boolean", "description": "Whether policy permits the proposed action, including any approval requirement."},
			"denied":            map[string]any{"type": "boolean", "description": "Whether an explicit policy rule denies the action."},
			"requires_approval": map[string]any{"type": "boolean", "description": "Whether policy requires approval before the action may proceed."},
			"reason":            stringSchema("Explanation of the policy decision, when available."),
		}, "allowed", "denied", "requires_approval")
	case "astrix_run_impacted_tests":
		tool.Title = "Run Impacted Go Tests"
		tool.Description = "Recompute the blast radius from the current staged and unstaged Git diff against HEAD in the opened repository, then execute the selected Go test packages (or the full Go suite if no test symbols map to packages). Use this to verify changed code after reviewing impact with astrix_get_blast_radius; it recomputes from the workspace and does not reuse a prior tool call's state. Test code runs locally with the current process permissions and may access the network or create or modify files. Returns selected test symbols on success; test failures are returned as tool errors. It accepts no arguments."
		tool.Annotations = map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true}
		tool.OutputSchema = objectSchema(map[string]any{
			"tests":  stringArraySchema("Test symbols selected and executed from the current diff's blast radius."),
			"passed": map[string]any{"type": "boolean", "description": "True when all selected test packages pass."},
		}, "tests", "passed")
	}
	return tool
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func stringArraySchema(description string) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": map[string]any{"type": "string"}}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
