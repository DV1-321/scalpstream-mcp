package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DV1-321/scalpstream-mcp/client"
	"github.com/DV1-321/scalpstream-mcp/pay"
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
	// Exact, not "at least": a withdrawn tool creeping back in — the research
	// tools in 0.1.10, whose host answers 410 — would be a dead tool, and an unlisted new one would be unreachable. Both
	// change this number.
	if len(tools) != expectedToolCount {
		t.Fatalf("expected exactly %d tools, got %d", expectedToolCount, len(tools))
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
	for _, want := range expectedTools {
		if !seen[want] {
			t.Errorf("tool %q is missing", want)
		}
	}
	for _, gone := range withdrawnTools {
		if seen[gone] {
			t.Errorf("tool %q was withdrawn (see CHANGELOG.md) and must not be listed", gone)
		}
	}
}

// The catalogue as of 0.1.10. The research tools have been withdrawn and their
// host answers 410 Gone on every path; listing one would offer a dead tool.
// air_quality is back, served from AirScout's self-hosted data. The count is a
// literal, not len(expectedTools), so that editing the list alone cannot move it.
var (
	expectedTools     = []string{"cheapest_fuel", "air_quality", "border_crossings", "product_recalls", "payment_status"}
	expectedToolCount = 5
	withdrawnTools    = []string{"options_research", "municipal_income", "crypto_research", "crypto_yields"}
)

// GROWTH 10-21 MCP-2. The initialize instructions are the catalogue in prose,
// and they had drifted from it both ways: they named "staking yields" (a tool
// being withdrawn) and never mentioned recalls (a tool that shipped in 0.1.8).
// A model reads this text to decide what the server is for, so each remaining
// domain must be named and no withdrawn one may be.
func TestInstructionsNameEveryToolDomainAndNothingElse(t *testing.T) {
	text := strings.ToLower(previewOnlyToolset().instructions())
	for _, domain := range []string{"fuel", "air quality", "border", "recalls"} {
		if !strings.Contains(text, domain) {
			t.Errorf("instructions do not mention %q: %q", domain, text)
		}
	}
	for _, gone := range []string{"yield", "staking", "crypto", "options", "municipal"} {
		if strings.Contains(text, gone) {
			t.Errorf("instructions still mention %q, a tool that is not offered: %q", gone, text)
		}
	}
	// Every listed network tool previews from and buys from a live service
	// origin; the research feed origin is gone from endpoints altogether.
	eps := endpoints{Fuel: "http://fuel.test", Air: "http://air.test", Border: "http://border.test", Recall: "http://recall.test"}
	onLive := func(u string) bool {
		for _, origin := range []string{eps.Fuel, eps.Air, eps.Border, eps.Recall} {
			if strings.HasPrefix(u, origin+"/") {
				return true
			}
		}
		return false
	}
	args := map[string]any{"country": "US", "region": "CA", "product": "x", "lat": 1.0, "lon": 1.0}
	for _, tl := range newToolset(&client.Client{}, eps).tools {
		if tl.Local {
			continue
		}
		if !onLive(tl.Preview) {
			t.Errorf("%s previews from %s, not a live service origin", tl.Name, tl.Preview)
		}
		if u, err := tl.BuildURL(args); err != nil || !onLive(u) {
			t.Errorf("%s buys from %q (err %v), not a live service origin", tl.Name, u, err)
		}
	}
}

// The research tools have been withdrawn. A client holding an old tool list may
// still call one by name; it is told so, as a tool error, before any request is
// made. air_quality, temporarily unavailable on the paused branch, is offered
// again and builds its request from its arguments.
func TestToolsNotOfferedAreRefusedBeforeAnyRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	eps := endpoints{Fuel: srv.URL, Air: srv.URL, Border: srv.URL, Recall: srv.URL}
	ts := newToolset(&client.Client{}, eps)

	cases := map[string]string{}
	for _, name := range withdrawnTools {
		cases[name] = "has been withdrawn"
	}
	for name, want := range cases {
		got := drive(t, ts, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{"lat":47.6588,"lon":-117.426}}}`)
		res, _ := got[0]["result"].(map[string]any)
		if res == nil || res["isError"] != true {
			t.Errorf("%s: want a tool error, got %v", name, got[0])
			continue
		}
		text, _ := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.HasPrefix(text, name+" "+want) {
			t.Errorf("%s: error = %q, want it to say %q", name, text, name+" "+want)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("a tool that is not offered reached a service %d times", n)
	}

	if _, refused := ts.notOffered["air_quality"]; refused {
		t.Error("air_quality is refused by name; it is offered again")
	}
	air, ok := ts.byName["air_quality"]
	if !ok {
		t.Fatal("air_quality is not in the catalogue")
	}
	u, err := air.BuildURL(map[string]any{"lat": "47.6588", "lon": -117.426, "hours": 48.0})
	if err != nil || !strings.HasPrefix(u, srv.URL+"/air?") || !strings.Contains(u, "hours=48") {
		t.Errorf("air_quality BuildURL = %q (err %v), want %s/air?...hours=48", u, err, srv.URL)
	}
	if _, err := air.BuildURL(map[string]any{"lat": 91.0, "lon": 0.0}); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("air_quality BuildURL accepted lat 91 (err %v)", err)
	}
}

// GROWTH 10-21 MCP-2. MCP 2026-07-28 opens with server/discover instead of
// initialize. This server speaks 2025-06-18, and the documented signal for a
// newer client to fall back to initialize is -32601 on server/discover: a
// JSON-RPC error with exactly that code, carrying the request's id, and the
// session must go on to answer initialize afterwards.
func TestServerDiscoverIsMethodNotFoundSoClientsFallBack(t *testing.T) {
	got := drive(t, previewOnlyToolset(),
		`{"jsonrpc":"2.0","id":"disc-1","method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if len(got) != 2 {
		t.Fatalf("expected 2 responses, got %d: %v", len(got), got)
	}
	e, ok := got[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("server/discover must be a JSON-RPC error, got %v", got[0])
	}
	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Errorf("server/discover code = %v, want %d", e["code"], codeMethodNotFound)
	}
	if got[0]["id"] != "disc-1" {
		t.Errorf("error carries id %v, want the request's id", got[0]["id"])
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "initialize") {
		t.Errorf("message %q should point the client at initialize", msg)
	}
	// The fallback: initialize from a client that asked for the newer revision
	// is answered with the one we speak, not echoed back.
	res, ok := got[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize after server/discover must still succeed: %v", got[1])
	}
	if res["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %s", res["protocolVersion"], protocolVersion)
	}
	// A server/discover NOTIFICATION (no id) gets no answer at all.
	if n := drive(t, previewOnlyToolset(), `{"jsonrpc":"2.0","method":"server/discover"}`); len(n) != 0 {
		t.Errorf("a server/discover notification must not be answered: %v", n)
	}
}

// GROWTH 10-21 MCP-2. Every service origin has an env override, so a running
// binary — not only a test toolset — can be pointed at a stub. The recalls
// service shipped in 0.1.8 without one.
func TestEveryServiceOriginHasAnEnvOverride(t *testing.T) {
	t.Setenv("SCALPMCP_FUEL_URL", "http://fuel.test")
	t.Setenv("SCALPMCP_AIR_URL", "http://air.test/")
	t.Setenv("SCALPMCP_BORDER_URL", "http://border.test")
	t.Setenv("SCALPMCP_RECALL_URL", "http://recall.test/")
	got := endpointsFromEnv()
	want := endpoints{Fuel: "http://fuel.test", Air: "http://air.test", Border: "http://border.test", Recall: "http://recall.test"}
	if got != want {
		t.Errorf("endpointsFromEnv() = %+v, want %+v", got, want)
	}
	// And the override is what the recalls tool actually buys from.
	ts := newToolset(&client.Client{}, got)
	u, err := ts.byName["product_recalls"].BuildURL(map[string]any{"product": "widget"})
	if err != nil || !strings.HasPrefix(u, "http://recall.test/recalls?") {
		t.Errorf("product_recalls URL = %q (err %v), want it under the override", u, err)
	}
	// With nothing set, the defaults stand.
	for _, name := range []string{"SCALPMCP_FUEL_URL", "SCALPMCP_AIR_URL", "SCALPMCP_BORDER_URL", "SCALPMCP_RECALL_URL"} {
		t.Setenv(name, "")
	}
	if d := endpointsFromEnv(); d != defaultEndpoints() {
		t.Errorf("with no overrides endpointsFromEnv() = %+v, want the defaults", d)
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
		{"border_crossings", `{"lat":91,"lon":0}`, "out of range"},
		{"border_crossings", `{"lon":0}`, "required"},
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
	tl, ok := ts.byName["border_crossings"]
	if !ok {
		t.Fatal("border_crossings is not in the catalogue")
	}
	for _, args := range []map[string]any{
		{"lat": 47.6588, "lon": -117.4260, "limit": float64(7)},
		{"lat": "47.6588", "lon": "-117.4260", "limit": "7"},
	} {
		u, err := tl.BuildURL(args)
		if err != nil {
			t.Fatalf("BuildURL(%v): %v", args, err)
		}
		if !strings.Contains(u, "lat=47.6588") || !strings.Contains(u, "limit=7") {
			t.Errorf("BuildURL(%v) = %s; expected lat and limit to survive", args, u)
		}
	}
}

// HUNT 10-20 M2. Boolean arguments arrive as JSON booleans, as strings, and
// occasionally as 1/0. `args["smart"].(bool)` dropped every non-bool form
// silently, so the wallet paid for the non-smart ranking the model had not
// asked for. Every spelling of true must set the flag, every spelling of false
// must not, and junk must be an error raised before any request.
func TestBooleanArgumentsAcceptStringsAndRejectJunk(t *testing.T) {
	ts := previewOnlyToolset()
	fuelTool, recallTool := ts.byName["cheapest_fuel"], ts.byName["product_recalls"]
	base := map[string]any{"country": "ES", "lat": 40.4, "lon": -3.7}

	for _, v := range []any{true, "true", "TRUE", " True ", "1", "yes", "Yes", float64(1)} {
		args := map[string]any{"country": "ES", "lat": 40.4, "lon": -3.7, "smart": v}
		u, err := fuelTool.BuildURL(args)
		if err != nil {
			t.Fatalf("smart=%#v: %v", v, err)
		}
		if !strings.Contains(u, "smart=1") {
			t.Errorf("smart=%#v (%T) was dropped: %s", v, v, u)
		}
		rargs := map[string]any{"product": "widget", "include_ended": v}
		u, err = recallTool.BuildURL(rargs)
		if err != nil {
			t.Fatalf("include_ended=%#v: %v", v, err)
		}
		if !strings.Contains(u, "include_ended=1") {
			t.Errorf("include_ended=%#v (%T) was dropped: %s", v, v, u)
		}
	}
	for _, v := range []any{false, "false", "FALSE", "0", "no", "No", float64(0), "", nil} {
		args := map[string]any{"country": "ES", "lat": 40.4, "lon": -3.7, "smart": v}
		u, err := fuelTool.BuildURL(args)
		if err != nil {
			t.Fatalf("smart=%#v: %v", v, err)
		}
		if strings.Contains(u, "smart=") {
			t.Errorf("smart=%#v (%T) should not set the flag: %s", v, v, u)
		}
	}
	if u, err := fuelTool.BuildURL(base); err != nil || strings.Contains(u, "smart=") {
		t.Errorf("absent smart: url=%s err=%v", u, err)
	}

	// Junk is an error that names the argument, and it is raised by BuildURL,
	// so no request — paid or preview — is ever made for it.
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	paidTS := newToolset(&client.Client{}, endpoints{Fuel: srv.URL, Recall: srv.URL})
	for _, c := range []struct{ tool, args, key string }{
		{"cheapest_fuel", `{"country":"ES","lat":40.4,"lon":-3.7,"smart":"maybe"}`, "smart"},
		{"cheapest_fuel", `{"country":"ES","lat":40.4,"lon":-3.7,"smart":2}`, "smart"},
		{"cheapest_fuel", `{"country":"ES","lat":40.4,"lon":-3.7,"smart":{"on":true}}`, "smart"},
		{"product_recalls", `{"product":"widget","include_ended":"sure"}`, "include_ended"},
		{"product_recalls", `{"product":"widget","include_ended":[true]}`, "include_ended"},
	} {
		_, _, err := paidTS.call(context.Background(), c.tool, json.RawMessage(c.args))
		if err == nil {
			t.Errorf("%s(%s) should have been refused", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), "boolean") {
			t.Errorf("%s(%s) error = %q, want it to name %s and say boolean", c.tool, c.args, err, c.key)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("a bad boolean reached the server %d times; it must be refused before any request", n)
	}
}

// HUNT 10-20 L1. A 200 on the unpaid request is a free resource — a mirror, a
// CDN cache, a feed with its gate down — and the envelope used to say
// paid:true for it. The model then reports a charge that payment_status
// cannot show. The envelope must say what actually happened.
func TestFreeResourceIsRenderedAsUnpaid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"recalls":[]}`)) // 200, no challenge
	}))
	defer srv.Close()

	ts := newToolset(&client.Client{}, endpoints{Recall: srv.URL})
	out, structured, err := ts.call(context.Background(), "product_recalls", json.RawMessage(`{"product":"widget"}`))
	if err != nil {
		t.Fatalf("a free 200 is a success: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, structured) {
		t.Error("structuredContent and the text block disagree")
	}
	if got["paid"] != false {
		t.Errorf("paid = %v for a resource that was never challenged; want false", got["paid"])
	}
	if got["complete"] != true {
		t.Errorf("complete = %v; the server returned the resource itself, not a preview", got["complete"])
	}
	if note, _ := got["note"].(string); !strings.Contains(note, "without a payment challenge") {
		t.Errorf("note = %q, want it to explain why paid is false", note)
	}
	if got["source"] != srv.URL+"/recalls?product=widget" {
		t.Errorf("source = %v", got["source"])
	}
}

// The text a model reads when payment is not possible must send the key to the
// server's environment, never into the conversation.
func TestHowToEnableNeverAsksForTheKeyInChat(t *testing.T) {
	for name, text := range map[string]string{
		"howToEnable":  howToEnable,
		"instructions": previewOnlyToolset().instructions(),
	} {
		low := strings.ToLower(text)
		if !strings.Contains(text, "EVM_BASE_PRIVATE_KEY") {
			t.Errorf("%s does not name the variable", name)
		}
		if !strings.Contains(low, "environment") {
			t.Errorf("%s does not say the key goes in the server's environment: %q", name, text)
		}
		if !strings.Contains(low, "chat") || !strings.Contains(low, "never") {
			t.Errorf("%s does not forbid asking for the key in chat: %q", name, text)
		}
	}
	// And the payment_status envelope carries the same wording.
	st := previewOnlyToolset().status()
	if data := st["data"].(map[string]any); data["how_to_enable"] != howToEnable {
		t.Errorf("payment_status.how_to_enable = %v", data["how_to_enable"])
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

	ts := newToolset(&client.Client{}, endpoints{Fuel: srv.URL, Air: srv.URL, Border: srv.URL, Recall: srv.URL})
	out, structured, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
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

	ts := newToolset(&client.Client{}, endpoints{Border: slow.URL})
	// syncBuffer, not bytes.Buffer: this test READS the output while the server
	// is still writing to it, and bytes.Buffer is not safe for that. The server
	// guards its own writes correctly; the race was on the test's side of the
	// same buffer, and `go test -race` is the only thing that says so.
	out := &syncBuffer{}
	s := newServer(ts, out)

	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.serve(in) }()

	// Start a call that will not return until we release it.
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"border_crossings","arguments":{"lat":32.7157,"lon":-117.1611}}}` + "\n"))
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
	out := &syncBuffer{} // safe here too: reads happen after serve returns, but do not rely on that
	s := newServer(ts, out)

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

// captureLog routes the standard logger into a buffer for one test. main()
// points it at stderr; tests need to read it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	return &buf
}

// callLine is the exact shape of the per-call log line. Anything that does not
// match is either a format drift or something leaking into the log.
var callLine = regexp.MustCompile(`^call tool=([A-Za-z_<>]+) paid=(true|false) complete=(true|false) status=(\d+) ms=(\d+) error=(true|false)$`)

// GROWTH 10-21 MCP-1. One stderr line per tool call — tool, paid, complete, HTTP
// status, milliseconds — and NEVER an argument value, the key, or the payment
// payload. stderr is captured by MCP hosts into files nobody reviews, so a
// user's coordinates, a product name, or a signed transfer must not land there.
func TestEveryToolCallLogsOneLineWithoutArgumentValues(t *testing.T) {
	// testKey was generated for the client package's tests and has never been
	// used on any network. It must never be funded.
	const testKey = "0xcc20f897f3181022d04694efab0c83f1697b2a1e0da3359ec25f27cce9c1ccd2"
	signer, err := pay.NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/preview") {
			_, _ = w.Write([]byte(`{"free":true}`))
			return
		}
		if p := r.Header.Get("PAYMENT-SIGNATURE"); p != "" {
			payloads = append(payloads, p)
			_, _ = w.Write([]byte(`{"crossings":[]}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"10000",` +
			`"asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111",` +
			`"extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	defer srv.Close()
	eps := endpoints{Fuel: srv.URL, Air: srv.URL, Border: srv.URL, Recall: srv.URL}

	// Distinctive argument values: if any of them shows up in the log, the
	// log is carrying the user's query.
	const args = `{"lat":47.65881,"lon":-117.42601,"place":"SECRETPLACE","radius":48}`
	secrets := []string{"47.65881", "117.42601", "SECRETPLACE", "radius", srv.URL, testKey, strings.TrimPrefix(testKey, "0x")}

	type step struct {
		name, args             string
		wantTool               string
		wantPaid, wantComplete bool
		wantStatus             int
		wantErr                bool
		ts                     *toolset
	}
	paidTS := newToolset(&client.Client{Signer: signer}, eps)
	previewTS := newToolset(&client.Client{}, eps)
	steps := []step{
		{"border_crossings", args, "border_crossings", true, true, 200, false, paidTS},
		{"border_crossings", args, "border_crossings", false, false, 402, false, previewTS},
		{"payment_status", `{}`, "payment_status", false, true, 0, false, previewTS},
		{"border_crossings", `{"lat":"SECRETPLACE","lon":0}`, "border_crossings", false, false, 0, true, previewTS},
		{"no_such_tool", args, "<unknown>", false, false, 0, true, previewTS},
		{"air_quality", args, "air_quality", false, false, 402, false, previewTS},
		// Tools that are not offered are refused by name before any request,
		// and logged under that name, which is ours rather than the caller's.
		{"options_research", args, "options_research", false, false, 0, true, paidTS},
	}
	for _, s := range steps {
		buf := captureLog(t)
		_, _, err := s.ts.call(context.Background(), s.name, json.RawMessage(s.args))
		if (err != nil) != s.wantErr {
			t.Fatalf("%s(%s): err=%v, wantErr=%v", s.name, s.args, err, s.wantErr)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s: logged %d lines, want exactly 1: %q", s.name, len(lines), buf.String())
		}
		m := callLine.FindStringSubmatch(lines[0])
		if m == nil {
			t.Fatalf("%s: log line %q does not match the call format", s.name, lines[0])
		}
		got := fmt.Sprintf("tool=%s paid=%s complete=%s status=%s error=%s", m[1], m[2], m[3], m[4], m[6])
		want := fmt.Sprintf("tool=%s paid=%t complete=%t status=%d error=%t", s.wantTool, s.wantPaid, s.wantComplete, s.wantStatus, s.wantErr)
		if got != want {
			t.Errorf("%s(%s): logged %q, want %q", s.name, s.args, got, want)
		}
		for _, secret := range secrets {
			if strings.Contains(buf.String(), secret) {
				t.Errorf("%s: log line carries %q: %q", s.name, secret, buf.String())
			}
		}
		for _, p := range payloads {
			if p != "" && strings.Contains(buf.String(), p) {
				t.Errorf("%s: log line carries the payment payload", s.name)
			}
		}
	}
	if len(payloads) != 1 {
		t.Fatalf("the paid step should have presented exactly one payment, saw %d", len(payloads))
	}
}

// The User-Agent names the program, the version that serverInfo reports, and
// where to read about it — and nothing about the user.
func TestUserAgentNamesProgramAndVersion(t *testing.T) {
	want := "scalpmcp/" + buildVersion + " (+https://github.com/DV1-321/scalpstream-mcp)"
	if got := userAgent(); got != want {
		t.Errorf("userAgent() = %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^scalpmcp/\d+\.\d+\.\d+ \(\+https://[^)]+\)$`).MatchString(userAgent()) {
		t.Errorf("userAgent() = %q does not follow product/version (+url)", userAgent())
	}
}

// syncBuffer is a bytes.Buffer safe to read while the server writes to it.
//
// Needed only by tests that inspect output WHILE serve is still running. The
// server serialises its own writes under its mutex, but a test reading the same
// bytes.Buffer from another goroutine is a data race on the buffer's internals —
// which `go test -race` catches and a plain `go test` does not.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
