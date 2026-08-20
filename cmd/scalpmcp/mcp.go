package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// maxConcurrentCalls bounds how many tool calls run at once.
//
// Dispatch used to be serial, inside the read loop, which meant a paid fetch —
// up to callTimeout — blocked every other message including `ping`. A host that
// treats an unanswered keepalive as a dead server would drop the connection
// mid-payment. A small pool fixes that without letting a model open unbounded
// concurrent purchases: the buyer's budget is the real limit, and it is enforced
// by reservation in client.Fetch, not by this number.
const maxConcurrentCalls = 4

// server dispatches MCP methods. It is transport-agnostic so the protocol can be
// exercised in tests without spawning a process or touching stdio.
type server struct {
	tools *toolset

	mu          sync.Mutex
	out         *bufio.Writer
	initialized bool

	// slots bounds in-flight tool calls; wg lets serve drain them before it
	// returns, so a closed stdin does not abandon a payment mid-flight.
	slots chan struct{}
	wg    sync.WaitGroup

	// inflight maps a request id to the cancel func for its tool call, so
	// notifications/cancelled can actually stop work rather than being noted
	// and ignored.
	cancelMu sync.Mutex
	inflight map[string]context.CancelFunc
}

func newServer(ts *toolset, w io.Writer) *server {
	return &server{
		tools:    ts,
		out:      bufio.NewWriter(w),
		slots:    make(chan struct{}, maxConcurrentCalls),
		inflight: make(map[string]context.CancelFunc),
	}
}

// maxLineBytes caps one JSON-RPC message. MCP messages are small; this is
// generous headroom that still bounds what a single line can allocate.
const maxLineBytes = 8 << 20

// serve reads newline-delimited JSON-RPC from r until EOF.
//
// A malformed line is answered and the loop continues rather than exiting: one
// bad message from a client must not take down a session that is otherwise fine.
// That now holds for an OVERSIZED line too — bufio.Scanner reports ErrTooLong by
// stopping, which ended the session and contradicted the rule above, so the
// reader is a bufio.Reader and a long line is drained and refused instead.
//
// serve returns only once every dispatched call has finished, so a closed stdin
// cannot abandon a payment already in flight.
func (s *server) serve(r io.Reader) error {
	defer s.wg.Wait()

	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readLine(br, maxLineBytes)
		if errors.Is(err, errLineTooLong) {
			s.writeError(nil, codeInvalidRequest, "message exceeds the size limit", maxLineBytes)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
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
}

// errLineTooLong marks a message past maxLineBytes. The line is consumed to the
// newline before it is reported, so the next read starts at a message boundary
// rather than in the middle of the oversized one.
var errLineTooLong = errors.New("scalpmcp: message too long")

func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(buf)+len(chunk) <= limit {
			buf = append(buf, chunk...)
		} else {
			buf = buf[:0] // over the limit: stop accumulating, keep draining
			for isPrefix {
				if _, isPrefix, err = br.ReadLine(); err != nil {
					return nil, err
				}
			}
			return nil, errLineTooLong
		}
		if !isPrefix {
			return buf, nil
		}
	}
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
			// Dispatched off the read loop so a slow paid fetch cannot stall
			// ping, tools/list, or a cancellation aimed at itself.
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.slots <- struct{}{}
				defer func() { <-s.slots }()
				s.handleToolCall(req)
			}()
		}
	case "notifications/cancelled":
		s.handleCancelled(req)
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

// cancelledParams is the notifications/cancelled body: the request to stop.
type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason,omitempty"`
}

// handleCancelled stops the tool call for the named request, if it is still
// running. A cancellation for an unknown or finished id is ignored, which is
// the correct outcome and not an error worth reporting — the notification and
// the result can always race.
func (s *server) handleCancelled(req *rpcRequest) {
	var p cancelledParams
	if err := json.Unmarshal(req.Params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	s.cancelMu.Lock()
	cancel, ok := s.inflight[string(p.RequestID)]
	s.cancelMu.Unlock()
	if ok {
		cancel()
	}
}

func (s *server) handleToolCall(req *rpcRequest) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(req.ID, codeInvalidRequest, "invalid tools/call params", err.Error())
		return
	}

	// Register the cancel func BEFORE the call starts, so a cancellation that
	// arrives immediately still finds it.
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	key := string(req.ID)
	s.cancelMu.Lock()
	s.inflight[key] = cancel
	s.cancelMu.Unlock()
	defer func() {
		s.cancelMu.Lock()
		delete(s.inflight, key)
		s.cancelMu.Unlock()
	}()

	text, structured, err := s.tools.call(ctx, p.Name, p.Arguments)
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
	// Both forms, always. structuredContent is what a client validates against
	// the tool's outputSchema; the text block is the same value, and is what a
	// client that predates structured results still reads.
	s.writeResult(req.ID, map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": structured,
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
