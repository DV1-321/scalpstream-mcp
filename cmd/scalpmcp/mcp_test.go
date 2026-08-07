package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	var st map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &st); err != nil {
		t.Fatalf("payment_status did not return JSON: %v", err)
	}
	if st["paid_mode_enabled"] != false {
		t.Error("with no signer, paid_mode_enabled must report false")
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
		_, err := ts.call(c.name, json.RawMessage(c.args))
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
	out, err := ts.call("options_research", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("preview fallback should not error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("fallback was not JSON: %v", err)
	}
	if got["paid"] != false {
		t.Error("fallback must state plainly that nothing was paid")
	}
	if got["preview"] == nil {
		t.Error("fallback must include the free preview")
	}
	price, _ := got["price"].(map[string]any)
	if price["amount_usd"] != "0.0100" {
		t.Errorf("fallback should quote the real price, got %v", price["amount_usd"])
	}
	if previewHits != 1 {
		t.Errorf("expected exactly 1 preview fetch, got %d", previewHits)
	}
}
