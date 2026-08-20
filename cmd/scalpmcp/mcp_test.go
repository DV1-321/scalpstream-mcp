package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DV1-321/scalpstream-mcp/client"
)

// drive runs a sequence of JSON-RPC lines through the server and returns the
// responses in order. It exercises the real transport loop, so framing bugs —
// a missing newline, an unflushed buffer — surface here rather than as a hang
// inside a client.
func drive(t *testing.T, ts *toolset, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := newServer(ts, &out)
	if err := s.serve(strings.NewReader(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("response line is not JSON: %q: %v", ln, err)
		}
		got = append(got, m)
	}
	return got
}

func previewOnlyToolset() *toolset {
	return newToolset(&client.Client{}, defaultEndpoints())
}

// The handshake a client performs before anything else. Getting protocolVersion
// or the capabilities shape wrong makes every client disconnect immediately.
func TestInitializeHandshake(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if len(got) != 1 {
		t.Fatalf("expected 1 response, got %d", len(got))
	}
	res, ok := got[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in initialize response: %v", got[0])
	}
	if res["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %s", res["protocolVersion"], protocolVersion)
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, hasTools := caps["tools"]; !hasTools {
		t.Error("server must advertise the tools capability or no tool is ever called")
	}
	// Capabilities we do not implement must NOT be advertised, or the client
	// will call them and get a method-not-found it cannot recover from.
	for _, unimplemented := range []string{"resources", "prompts", "logging"} {
		if _, present := caps[unimplemented]; present {
			t.Errorf("advertised %q but it is not implemented", unimplemented)
		}
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "scalpstream" {
		t.Errorf("serverInfo.name = %v, want scalpstream", info["name"])
	}
}

// A notification carries no id and MUST NOT be answered. Replying to one is a
// protocol violation that desynchronises strict clients.
func TestNotificationsAreNotAnswered(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	if len(got) != 0 {
		t.Fatalf("notifications must produce no response, got %d: %v", len(got), got)
	}
}

// One malformed line must not kill the session: the server answers and carries on.
func TestMalformedLineDoesNotEndSession(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{not json`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if len(got) != 2 {
		t.Fatalf("expected an error then a pong, got %d: %v", len(got), got)
	}
	if got[0]["error"] == nil {
		t.Error("malformed JSON should produce a JSON-RPC error")
	}
	if got[1]["result"] == nil {
		t.Error("session should survive a malformed line and still answer ping")
	}
}

func TestToolsListShape(t *testing.T) {
	got := drive(t, previewOnlyToolset(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) < 8 {
		t.Fatalf("expected at least 8 tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name, _ := tl["name"].(string)
		if name == "" {
			t.Error("a tool has no name")
		}
		seen[name] = true
		if d, _ := tl["description"].(string); d == "" {
			t.Errorf("%s has no description; a model cannot choose a tool it cannot read about", name)
		}
		// inputSchema must be a JSON Schema object. Clients validate arguments
		// against it, and a missing or non-object schema breaks the call path.
		schema, ok := tl["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no inputSchema object", name)
		}
		if schema["type"] != "object" {
			t.Errorf("%s inputSchema.type = %v, want object", name, schema["type"])
		}
		if _, hasProps := schema["properties"]; !hasProps {
			t.Errorf("%s inputSchema has no properties key", name)
		}
	}
	for _, want := range []string{
		"options_research", "municipal_income", "crypto_research", "crypto_yields",
		"cheapest_fuel", "air_quality", "border_crossings", "product_recalls", "payment_status",
	} {
		if !seen[want] {
			t.Errorf("tool %q is missing", want)
		}
	}
}

func TestUnknownToolIsResultLevelError(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	// A failing tool is reported in the RESULT with isError, not as a JSON-RPC
	// error: the model needs to see the failure to adapt to it.
	if got[0]["error"] != nil {
		t.Fatalf("tool failure must not be a JSON-RPC error: %v", got[0]["error"])
	}
	res := got[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError true, got %v", res["isError"])
	}
}

func TestUnknownMethodIsProtocolError(t *testing.T) {
	got := drive(t, previewOnlyToolset(), `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)
	e, ok := got[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("unimplemented method should be a JSON-RPC error, got %v", got[0])
	}
	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Errorf("code = %v, want %d", e["code"], codeMethodNotFound)
	}
}

// payment_status must never touch the network, so it works before any key or
// connectivity exists.
func TestPaymentStatusIsLocalAndHonest(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"payment_status","arguments":{}}}`)
	res := got[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("payment_status should not error: %v", res)
	}
	content := res["content"].([]any)[0].(map[string]any)
	var env map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &env); err != nil {
		t.Fatalf("payment_status did not return JSON: %v", err)
	}
	// Every tool returns the same envelope. This one is local, so it costs
	// nothing (paid=false) but IS the whole answer (complete=true).
	if env["paid"] != false || env["complete"] != true {
		t.Errorf("payment_status envelope = paid:%v complete:%v, want false/true", env["paid"], env["complete"])
	}
	st, _ := env["data"].(map[string]any)
	if st["paid_mode_enabled"] != false {
		t.Error("with no signer, paid_mode_enabled must report false")
	}
	// A declared outputSchema is only useful if the result actually carries the
	// structured form a client validates against it.
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatal("result carries no structuredContent, but the tool declares an outputSchema")
	}
	if !reflect.DeepEqual(sc, env) {
		t.Error("structuredContent and the text block disagree")
	}
}

// Every tool declares how it behaves and what it returns. Without annotations a
// host cannot tell a read-only lookup from a state-changing one and prompts for
// each call; without an outputSchema a caller cannot know the result shape until
// it has already paid.
func TestToolsDeclareAnnotationsAndOutputSchema(t *testing.T) {
	got := drive(t, previewOnlyToolset(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)

	for _, raw := range tools {
		tl := raw.(map[string]any)
		name := tl["name"].(string)

		ann, ok := tl["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s has no annotations", name)
			continue
		}
		// Nothing here mutates seller state, and saying so is what lets a host
		// call these without confirming every time.
		if ann["readOnlyHint"] != true {
			t.Errorf("%s readOnlyHint = %v, want true", name, ann["readOnlyHint"])
		}
		if ann["destructiveHint"] != false {
			t.Errorf("%s destructiveHint = %v, want false", name, ann["destructiveHint"])
		}

		out, ok := tl["outputSchema"].(map[string]any)
		if !ok {
			t.Errorf("%s declares no outputSchema", name)
			continue
		}
		if out["type"] != "object" {
			t.Errorf("%s outputSchema.type = %v, want object", name, out["type"])
		}
		props, _ := out["properties"].(map[string]any)
		for _, field := range []string{"paid", "complete", "source", "data"} {
			if props[field] == nil {
				t.Errorf("%s outputSchema is missing %q", name, field)
			}
		}
	}

	// A paid tool must NOT claim idempotence: the data is stable for a given
	// query, but each call settles its own payment, so a repeat is not free.
	for _, raw := range tools {
		tl := raw.(map[string]any)
		ann := tl["annotations"].(map[string]any)
		if tl["name"] == "payment_status" {
			if ann["idempotentHint"] != true || ann["openWorldHint"] != false {
				t.Error("payment_status is local and repeatable; it should say so")
			}
			continue
		}
		if ann["idempotentHint"] != false {
			t.Errorf("%s claims idempotence, but repeating it spends money again", tl["name"])
		}
		if ann["openWorldHint"] != true {
			t.Errorf("%s reaches an external service and should declare openWorldHint", tl["name"])
		}
	}
}

// Argument validation happens before any request is made, so a bad coordinate
// costs an error rather than a paid call that returns nothing useful.
func TestArgumentValidationRejectsBadCoordinates(t *testing.T) {
	ts := previewOnlyToolset()
	cases := []struct{ name, args, want string }{
		{"air_quality", `{"lat":91,"lon":0}`, "out of range"},
		{"air_quality", `{"lon":0}`, "required"},
		{"border_crossings", `{"lat":0,"lon":-181}`, "out of range"},
		{"cheapest_fuel", `{}`, "country is required"},
		{"cheapest_fuel", `{"country":"US"}`, "region"},
		{"cheapest_fuel", `{"country":"ES"}`, "lat and lon are required"},
	}
	for _, c := range cases {
		_, _, err := ts.call(context.Background(), c.name, json.RawMessage(c.args))
		if err == nil {
			t.Errorf("%s(%s) should have failed", c.name, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s(%s) error = %q, want it to mention %q", c.name, c.args, err, c.want)
		}
	}
}

// Arguments come from a model, which may send a number as a string. Both forms
// must work rather than one silently failing.
func TestArgumentsAcceptStringOrNumber(t *testing.T) {
	ts := previewOnlyToolset()
	for _, tl := range ts.tools {
		if tl.Name != "air_quality" {
			continue
		}
		for _, args := range []map[string]any{
			{"lat": 47.6588, "lon": -117.4260, "hours": float64(48)},
			{"lat": "47.6588", "lon": "-117.4260", "hours": "48"},
		} {
			u, err := tl.BuildURL(args)
			if err != nil {
				t.Fatalf("BuildURL(%v): %v", args, err)
			}
			if !strings.Contains(u, "lat=47.6588") || !strings.Contains(u, "hours=48") {
				t.Errorf("BuildURL(%v) = %s; expected lat and hours to survive", args, u)
			}
		}
	}
}

// Without a spending key the paid tools must still return something useful: the
// free preview plus the quoted price. Returning only an error would make the
// free tier look like a broken server.
func TestPreviewFallbackWhenNoKey(t *testing.T) {
	var previewHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/preview") {
			previewHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"service":"stub","free":true}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"10000","asset":"0xUSDC","payTo":"0xrecv","extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	defer srv.Close()

	ts := newToolset(&client.Client{}, endpoints{Feed: srv.URL, Fuel: srv.URL, Air: srv.URL, Border: srv.URL})
	out, structured, err := ts.call(context.Background(), "options_research", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("preview fallback should not error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("fallback was not JSON: %v", err)
	}
	// The text block and the structured result are the same value rendered
	// twice; a client reading either must see the same thing.
	if !reflect.DeepEqual(got, structured) {
		t.Error("structuredContent and the text block disagree")
	}
	if got["paid"] != false {
		t.Error("fallback must state plainly that nothing was paid")
	}
	// complete=false is the field an agent branches on: what came back is the
	// preview, not the dataset the tool describes.
	if got["complete"] != false {
		t.Error("a preview fallback must report complete=false")
	}
	if got["data"] == nil {
		t.Error("fallback must include the free preview under data")
	}
	price, _ := got["price"].(map[string]any)
	if price["amount_usd"] != "0.0100" {
		t.Errorf("fallback should quote the real price, got %v", price["amount_usd"])
	}
	if previewHits != 1 {
		t.Errorf("expected exactly 1 preview fetch, got %d", previewHits)
	}
}

// A slow tool call must not stall the rest of the session.
//
// Dispatch used to be serial inside the read loop, so a paid fetch — up to
// callTimeout — blocked every other message including `ping`. A host that treats
// an unanswered keepalive as a dead server would drop the connection mid-payment.
func TestSlowCallDoesNotBlockPing(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer slow.Close()
	defer close(release)

	ts := newToolset(&client.Client{}, endpoints{Feed: slow.URL})
	var out bytes.Buffer
	s := newServer(ts, &out)

	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.serve(in) }()

	// Start a call that will not return until we release it.
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"options_research","arguments":{}}}` + "\n"))
	// Ping behind it must be answered while that call is still in flight.
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"))

	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(out.String(), `"id":2`) {
			break // the ping was answered while the tool call was blocked
		}
		select {
		case <-deadline:
			t.Fatalf("ping was not answered while a tool call was in flight; got: %s", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}

	release <- struct{}{}
	_ = w.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after stdin closed")
	}
}

// An oversized message is refused, and the session survives it. bufio.Scanner
// reported ErrTooLong by STOPPING, which ended the session and contradicted the
// rule that one bad message must not take down a good session.
func TestOversizedMessageDoesNotKillTheSession(t *testing.T) {
	ts := newToolset(&client.Client{}, defaultEndpoints())
	var out bytes.Buffer
	s := newServer(ts, &out)

	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.serve(in) }()

	go func() {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping","pad":"`))
		blob := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 9; i++ { // ~9 MB, past maxLineBytes
			_, _ = w.Write(blob)
		}
		_, _ = w.Write([]byte(`"}` + "\n"))
		// The session must still be alive for this one.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"))
		_ = w.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned an error after an oversized message: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not return")
	}
	if !strings.Contains(out.String(), `"id":2`) {
		t.Errorf("the message after an oversized one was not handled; got: %s", out.String())
	}
}
