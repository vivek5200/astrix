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
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
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
		s.tools[name] = toolEntry{Tool: Tool{Name: name, Description: description(name), InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}}, Handler: handler}
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
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}}
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
func description(name string) string {
	switch name {
	case "astrix_get_blast_radius":
		return "Compute the impacted symbols and tests for modified files."
	case "astrix_evaluate_policy":
		return "Evaluate an action intent against Astrix policies."
	case "astrix_run_impacted_tests":
		return "Run tests selected by the current blast-radius analysis."
	default:
		return "Astrix tool."
	}
}
