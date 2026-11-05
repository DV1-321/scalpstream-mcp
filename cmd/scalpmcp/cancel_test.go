package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DV1-321/scalpstream-mcp/client"
)

// heldSeller answers every /crossings request only when released, and records
// which calls reached it (by their lat, which each test call makes distinct)
// and which of those saw their request cancelled.
type heldSeller struct {
	*httptest.Server
	release chan struct{}
	mu      sync.Mutex
	arrived map[string]bool
	gone    map[string]bool
}

func newHeldSeller(t *testing.T) *heldSeller {
	t.Helper()
	h := &heldSeller{release: make(chan struct{}), arrived: map[string]bool{}, gone: map[string]bool{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lat := r.URL.Query().Get("lat")
		h.mu.Lock()
		h.arrived[lat] = true
		h.mu.Unlock()
		select {
		case <-h.release:
			_, _ = w.Write([]byte(`{"ok":true}`))
		case <-r.Context().Done():
			h.mu.Lock()
			h.gone[lat] = true
			h.mu.Unlock()
		}
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *heldSeller) arrivedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.arrived)
}

func (h *heldSeller) saw(lat string) (arrived, gone bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.arrived[lat], h.gone[lat]
}

// session is a server on a pipe, for tests that must send messages while
// earlier calls are still in flight.
type session struct {
	t    *testing.T
	out  *syncBuffer
	w    *io.PipeWriter
	done chan error
}

func startSession(t *testing.T, ts *toolset) *session {
	t.Helper()
	s := &session{t: t, out: &syncBuffer{}, done: make(chan error, 1)}
	in, w := io.Pipe()
	s.w = w
	srv := newServer(ts, s.out)
	go func() { s.done <- srv.serve(in) }()
	return s
}

func (s *session) send(line string) {
	s.t.Helper()
	if _, err := s.w.Write([]byte(line + "\n")); err != nil {
		s.t.Fatalf("write: %v", err)
	}
}

// waitFor polls until cond holds or five seconds pass.
func (s *session) waitFor(what string, cond func() bool) {
	s.t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			s.t.Fatalf("timed out waiting for %s; output so far: %s", what, s.out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *session) answered(id int) bool {
	return strings.Contains(s.out.String(), fmt.Sprintf(`"id":%d,`, id))
}

// pingThrough sends a ping and waits for its answer. The read loop handles
// messages in order, so once the ping is answered every message sent before
// it has been handled.
func (s *session) pingThrough(id int) {
	s.t.Helper()
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, id))
	s.waitFor(fmt.Sprintf("ping %d", id), func() bool { return s.answered(id) })
}

func (s *session) close() {
	s.t.Helper()
	_ = s.w.Close()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("serve did not return after stdin closed")
	}
}

func crossingsCall(id int) string {
	// lat doubles as the call's marker at the seller: 11..15 for ids 11..15.
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"border_crossings","arguments":{"lat":%d,"lon":-100}}}`, id, id)
}

// Review 2026-11-04, finding 8. A call was only registered as cancellable once
// its goroutine had a slot. With maxConcurrentCalls running, the next call
// waited unregistered: notifications/cancelled for it found nothing, and when
// a slot came free it ran - for a paid tool, it paid - although the client had
// withdrawn it. A queued call that is cancelled must never run.
func TestCallCancelledWhileQueuedNeverRuns(t *testing.T) {
	seller := newHeldSeller(t)
	logs := captureLog(t)
	s := startSession(t, newToolset(&client.Client{}, endpoints{Border: seller.URL}))

	ids := []int{11, 12, 13, 14, 15} // one more than maxConcurrentCalls
	if len(ids) != maxConcurrentCalls+1 {
		t.Fatalf("this test needs maxConcurrentCalls+1 calls; maxConcurrentCalls is %d", maxConcurrentCalls)
	}
	for _, id := range ids {
		s.send(crossingsCall(id))
	}
	// Four reach the seller and block there; the fifth is queued for a slot.
	// Which four is up to the scheduler, so find the one that did not arrive.
	s.waitFor("four calls to reach the seller", func() bool { return seller.arrivedCount() == maxConcurrentCalls })
	queued := 0
	for _, id := range ids {
		if arrived, _ := seller.saw(fmt.Sprint(id)); !arrived {
			queued = id
		}
	}
	if queued == 0 {
		t.Fatal("no call was left queued")
	}

	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":%d,"reason":"user aborted"}}`, queued))
	s.pingThrough(99) // the cancellation has been handled once this returns

	close(seller.release) // the four running calls finish and free their slots
	for _, id := range ids {
		if id != queued {
			id := id
			s.waitFor(fmt.Sprintf("call %d to be answered", id), func() bool { return s.answered(id) })
		}
	}
	s.close() // returns only when every dispatched call has finished

	if arrived, _ := seller.saw(fmt.Sprint(queued)); arrived {
		t.Errorf("call %d was cancelled while queued and still ran: its request reached the seller", queued)
	}
	if n := seller.arrivedCount(); n != maxConcurrentCalls {
		t.Errorf("the seller saw %d requests, want %d", n, maxConcurrentCalls)
	}
	// Not answered: the spec asks a receiver not to respond to a cancelled
	// request, and the client is no longer waiting for this id.
	if s.answered(queued) {
		t.Errorf("the cancelled call %d was answered: %s", queued, s.out.String())
	}
	if !strings.Contains(logs.String(), "cancelled while queued") {
		t.Errorf("no log line for the dropped call; log: %q", logs.String())
	}
}

// Cancelling a queued call must not leak its slot or its registration: the
// server keeps serving calls afterwards, including one reusing the same id.
func TestServerKeepsServingAfterAQueuedCancel(t *testing.T) {
	seller := newHeldSeller(t)
	srvToolset := newToolset(&client.Client{}, endpoints{Border: seller.URL})
	out := &syncBuffer{}
	srv := newServer(srvToolset, out)
	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- srv.serve(in) }()
	s := &session{t: t, out: out, w: w, done: done}

	for id := 11; id <= 15; id++ {
		s.send(crossingsCall(id))
	}
	s.waitFor("four calls to reach the seller", func() bool { return seller.arrivedCount() == maxConcurrentCalls })
	queued := 0
	for id := 11; id <= 15; id++ {
		if arrived, _ := seller.saw(fmt.Sprint(id)); !arrived {
			queued = id
		}
	}
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":%d}}`, queued))
	s.pingThrough(98)
	close(seller.release)
	for id := 11; id <= 15; id++ {
		if id != queued {
			id := id
			s.waitFor(fmt.Sprintf("call %d", id), func() bool { return s.answered(id) })
		}
	}

	// Every slot is free again and nothing is registered.
	s.waitFor("the registry to empty", func() bool {
		srv.cancelMu.Lock()
		defer srv.cancelMu.Unlock()
		return len(srv.inflight) == 0
	})
	// A later call - here reusing the cancelled id - runs normally.
	s.send(crossingsCall(queued))
	s.waitFor("the reused id to be answered", func() bool { return s.answered(queued) })
	if arrived, _ := seller.saw(fmt.Sprint(queued)); !arrived {
		t.Error("a new call with the cancelled id did not run")
	}
	s.close()
	if len(srv.slots) != 0 {
		t.Errorf("%d slots still held after every call finished", len(srv.slots))
	}
}

// Moving registration to the read loop must not change what cancelling a
// RUNNING call does: its HTTP request is abandoned and the call is answered
// with a tool error.
func TestCancellingARunningCallStillStopsIt(t *testing.T) {
	seller := newHeldSeller(t)
	s := startSession(t, newToolset(&client.Client{}, endpoints{Border: seller.URL}))

	s.send(crossingsCall(21))
	s.waitFor("the call to reach the seller", func() bool { return seller.arrivedCount() == 1 })
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":21}}`)
	s.waitFor("the cancelled call to be answered", func() bool { return s.answered(21) })
	s.waitFor("the seller to see the request abandoned", func() bool { _, gone := seller.saw("21"); return gone })
	s.close()

	if !strings.Contains(s.out.String(), `"isError":true`) {
		t.Errorf("a cancelled running call should be answered as a tool error; got: %s", s.out.String())
	}
}

// A cancellation that names some other request leaves a queued call alone.
func TestCancellingAnotherIdDoesNotDropAQueuedCall(t *testing.T) {
	seller := newHeldSeller(t)
	s := startSession(t, newToolset(&client.Client{}, endpoints{Border: seller.URL}))
	for id := 11; id <= 15; id++ {
		s.send(crossingsCall(id))
	}
	s.waitFor("four calls to reach the seller", func() bool { return seller.arrivedCount() == maxConcurrentCalls })
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":777}}`)
	s.pingThrough(97)
	close(seller.release)
	for id := 11; id <= 15; id++ {
		id := id
		s.waitFor(fmt.Sprintf("call %d", id), func() bool { return s.answered(id) })
	}
	s.close()
	if n := seller.arrivedCount(); n != 5 {
		t.Errorf("the seller saw %d requests, want all 5", n)
	}
}

// A call retires only its own registration. If a client reuses an id while the
// earlier call with that id is still winding down, the earlier call finishing
// must not leave the later one uncancellable.
func TestACallRetiresOnlyItsOwnRegistration(t *testing.T) {
	s := newServer(previewOnlyToolset(), io.Discard)
	_, retireFirst := s.admit(json.RawMessage(`7`))
	second, retireSecond := s.admit(json.RawMessage(`7`))

	retireFirst()
	s.handleCancelled(&rpcRequest{Method: "notifications/cancelled", Params: json.RawMessage(`{"requestId":7}`)})
	if second.Err() == nil {
		t.Error("the later call with a reused id could not be cancelled after the earlier one retired")
	}
	retireSecond()
	if n := len(s.inflight); n != 0 {
		t.Errorf("%d registrations left after both calls retired", n)
	}
}

// server.initialized was set by notifications/initialized and read nowhere. It
// is gone, and what the server actually does is pinned instead: it serves
// tools/list and tools/call whether or not the client sent the notification,
// and never answers the notification itself.
func TestServerDoesNotGateOnTheInitializedNotification(t *testing.T) {
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"payment_status","arguments":{}}}`
	check := func(name string, got []map[string]any, want int) {
		t.Helper()
		if len(got) != want {
			t.Fatalf("%s: %d responses, want %d: %v", name, len(got), want, got)
		}
		for _, m := range got {
			if m["error"] != nil {
				t.Errorf("%s: request %v was refused: %v", name, m["id"], m["error"])
			}
		}
	}
	check("without the notification", drive(t, previewOnlyToolset(), list, status), 2)
	check("with it", drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+protocolVersion+`"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, list, status), 3)
}
