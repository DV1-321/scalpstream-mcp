package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"sync"
)

// Model Context Protocol over stdio: newline-delimited JSON-RPC 2.0, requests on
// stdin, responses on stdout.
//
// STDOUT IS THE PROTOCOL. Anything written there that is not a JSON-RPC message
// corrupts the stream and the client drops the connection, usually with an
// opaque parse error. Every diagnostic in this program therefore goes to stderr,
// and log's default output is redirected in main for the same reason. This is
// the single easiest way to break an MCP server, so it is worth stating twice.

// protocolVersion is the spec revision this server implements.
//
// Deliberately 2025-06-18 and not the newer 2026-07-28: that revision redefines
// tools/call to return a task handle the client then polls, which is a breaking
// change to the one method that matters here. 2025-06-18 is what deployed
// clients speak, and version negotiation lets a newer client step down to it.
const protocolVersion = "2025-06-18"

// JSON-RPC error codes used here (from the JSON-RPC 2.0 spec).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// server dispatches MCP methods. It is transport-agnostic so the protocol can be
// exercised in tests without spawning a process or touching stdio.
type server struct {
	tools *toolset

	mu          sync.Mutex
	out         *bufio.Writer
	initialized bool
}

func newServer(ts *toolset, w io.Writer) *server {
	return &server{tools: ts, out: bufio.NewWriter(w)}
}

// serve reads newline-delimited JSON-RPC from r until EOF.
//
// A malformed line is answered and the loop continues rather than exiting: one
// bad message from a client must not take down a session that is otherwise fine.
func (s *server) serve(r io.Reader) error {
	sc := bufio.NewScanner(r)
	// MCP messages are small, but a tool result echoed back in a future revision
	// could be large; 8MB is generous and bounded.
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, codeParseError, "invalid JSON", err.Error())
			continue
		}
		s.handle(&req)
	}
	return sc.Err()
}

func (s *server) handle(req *rpcRequest) {
	// A request without an id is a notification: per JSON-RPC it MUST NOT be
	// answered, not even on error. Returning here is the whole handling.
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
	case "ping":
		// Ping is allowed before initialization and answers with an empty object.
		if !isNotification {
			s.writeResult(req.ID, map[string]any{})
		}
	case "tools/list":
		if !isNotification {
			s.writeResult(req.ID, map[string]any{"tools": s.tools.list()})
		}
	case "tools/call":
		if !isNotification {
			s.handleToolCall(req)
		}
	case "notifications/cancelled":
		// Nothing to cancel: every call here is synchronous and short.
	default:
		if !isNotification {
			s.writeError(req.ID, codeMethodNotFound, "unknown method: "+req.Method, nil)
		}
	}
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

func (s *server) handleInitialize(req *rpcRequest) {
	var p initializeParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p) // absent/odd params are not fatal
	}
	// Version negotiation: echo the client's version when we speak it, otherwise
	// answer with ours and let the client decide whether to continue. Echoing an
	// unknown version back would claim conformance we do not have.
	agreed := protocolVersion
	if p.ProtocolVersion == protocolVersion {
		agreed = p.ProtocolVersion
	}
	s.writeResult(req.ID, map[string]any{
		"protocolVersion": agreed,
		"capabilities": map[string]any{
			// Tools only. Declaring resources or prompts we do not implement
			// invites calls we would have to fail.
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "scalpstream",
			"title":   "ScalpStream — pay-per-call data over x402",
			"version": buildVersion,
		},
		"instructions": s.tools.instructions(),
	})
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (s *server) handleToolCall(req *rpcRequest) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(req.ID, codeInvalidRequest, "invalid tools/call params", err.Error())
		return
	}
	text, err := s.tools.call(p.Name, p.Arguments)
	if err != nil {
		// A tool that FAILED is not a protocol error. The spec puts execution
		// failures in the result with isError so the model can see what went
		// wrong and adapt; a JSON-RPC error would hide it from the model and
		// surface as a client-level fault instead.
		s.writeResult(req.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}
	s.writeResult(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

func (s *server) writeResult(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, msg string, data any) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

func (s *server) write(resp rpcResponse) {
	raw, err := json.Marshal(resp)
	if err != nil {
		// Encoding our own response failed: report something valid rather than
		// writing nothing and leaving the client waiting forever.
		raw, _ = json.Marshal(rpcResponse{
			JSONRPC: "2.0", ID: resp.ID,
			Error: &rpcError{Code: codeInternalError, Message: "failed to encode response"},
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(append(raw, '\n')); err != nil {
		log.Printf("write: %v", err)
		return
	}
	// Flush every message: the client is blocked waiting on this line, so a
	// buffered response that never flushes reads to it as a hang.
	if err := s.out.Flush(); err != nil {
		log.Printf("flush: %v", err)
	}
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
