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
// Deliberately 2025-06-18 and not the newer 2026-07-28. That revision removes
// the initialize handshake and sessions and replaces them with a mandatory
// server/discover method; tasks (a tools/call that returns a handle the client
// polls) are optional in it, not the default. A 2026-07-28 client therefore
// opens with server/discover, and the right answer from a server that does not
// speak that revision is JSON-RPC -32601 (method not found): the spec names it
// as the signal to fall back to initialize and 2025-06-18 negotiation, which
// this server does speak. Answering server/discover with anything else would
// claim a revision we do not implement. 2025-06-18 is what deployed clients
// speak, and version negotiation lets a newer client step down to it.
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

	mu  sync.Mutex
	out *bufio.Writer

	// slots bounds in-flight tool calls; wg lets serve drain them before it
	// returns, so a closed stdin does not abandon a payment mid-flight.
	slots chan struct{}
	wg    sync.WaitGroup

	// inflight maps a request id to the cancel func for its tool call, so
	// notifications/cancelled can actually stop work rather than being noted
	// and ignored. An id is entered by the READ LOOP, before the call's
	// goroutine exists (see admit): a call still waiting for a slot is as
	// cancellable as one that is running.
	cancelMu sync.Mutex
	inflight map[string]*admitted
}

// admitted is one registered tools/call. It is a pointer in the map so a call
// retires only ITS OWN entry: if a client reuses an id while the earlier call
// with that id is still winding down, the earlier call must not unregister the
// later one.
type admitted struct {
	cancel context.CancelFunc
}

func newServer(ts *toolset, w io.Writer) *server {
	return &server{
		tools:    ts,
		out:      bufio.NewWriter(w),
		slots:    make(chan struct{}, maxConcurrentCalls),
		inflight: make(map[string]*admitted),
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
			// Over the limit: stop accumulating, keep draining so the reader stays in sync.
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
		// Accepted and deliberately not acted on. This server keeps no
		// per-session state that the notification would unlock: tools/list and
		// tools/call are served whether or not it arrived, because refusing
		// them would break a client that skips it and protect nothing (the
		// spending limits live in the client, not in the handshake). A flag
		// was once set here and read nowhere; it is gone rather than left to
		// suggest a gate that does not exist.
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
			//
			// The call is made cancellable HERE, on the read loop, before its
			// goroutine starts. Registration used to happen inside the
			// goroutine, after it had a slot, so with four calls running a
			// fifth sat in the queue unregistered: a notifications/cancelled
			// for it found nothing, was dropped, and the call ran - and paid -
			// when a slot came free. Messages are read in order, so once admit
			// has returned every later cancellation for this id finds it.
			ctx, done := s.admit(req.ID)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer done()
				select {
				case s.slots <- struct{}{}:
				case <-ctx.Done():
					s.dropCancelled()
					return
				}
				defer func() { <-s.slots }()
				// A slot and the cancellation can both be ready, and select
				// picks either: a withdrawn request must not run because the
				// slot happened to win.
				if ctx.Err() != nil {
					s.dropCancelled()
					return
				}
				s.handleToolCall(ctx, req)
			}()
		}
	case "notifications/cancelled":
		s.handleCancelled(req)
	case "server/discover":
		// The 2026-07-28 opening call. Explicitly -32601, not an accident of the
		// default branch: it is the documented fallback signal (see
		// protocolVersion), and a future "handle everything else" branch must
		// not quietly start answering it with something that is not discovery.
		if !isNotification {
			s.writeError(req.ID, codeMethodNotFound,
				"server/discover is not implemented: this server speaks MCP "+protocolVersion+"; call initialize", nil)
		}
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

// handleCancelled stops the tool call for the named request, whether it is
// running or still queued for a slot (a queued call then never starts). A
// cancellation for an unknown or finished id is ignored, which is the correct
// outcome and not an error worth reporting — the notification and the result
// can always race.
func (s *server) handleCancelled(req *rpcRequest) {
	var p cancelledParams
	if err := json.Unmarshal(req.Params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	s.cancelMu.Lock()
	call, ok := s.inflight[string(p.RequestID)]
	s.cancelMu.Unlock()
	if ok {
		call.cancel()
	}
}

// admit registers a tools/call as cancellable and returns its context and the
// func that retires it. It runs on the read loop, so it has returned before
// the next message - which may be the cancellation - is read.
//
// The context carries NO deadline. callTimeout is applied in handleToolCall,
// once the call has a slot: time spent queued behind other calls must not eat
// into the time a paid fetch is given to finish.
func (s *server) admit(id json.RawMessage) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	key, call := string(id), &admitted{cancel: cancel}
	s.cancelMu.Lock()
	s.inflight[key] = call
	s.cancelMu.Unlock()
	return ctx, func() {
		s.cancelMu.Lock()
		if s.inflight[key] == call {
			delete(s.inflight, key)
		}
		s.cancelMu.Unlock()
		cancel()
	}
}

// dropCancelled is the whole handling of a call cancelled before it started:
// it never runs, so no request is made and nothing is paid, and it is not
// answered - the spec asks a receiver not to respond to a cancelled request,
// and the client has stopped waiting for this id. One stderr line records it;
// the request is not parsed, so the line carries nothing of the caller's.
func (s *server) dropCancelled() {
	log.Print("call cancelled while queued: not started, nothing requested or paid")
}

// handleToolCall runs one admitted call. ctx is the call's own (admit): it is
// cancelled by notifications/cancelled for this id.
func (s *server) handleToolCall(ctx context.Context, req *rpcRequest) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(req.ID, codeInvalidRequest, "invalid tools/call params", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

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
