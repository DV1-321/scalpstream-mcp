package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
)

// engineStub answers GET /v1/status as the vault does: 200 with a status for
// the one good token, 401 for any other, and records what it saw.
func engineStub(t *testing.T, good string) (*httptest.Server, *atomic.Int32, *string) {
	t.Helper()
	var hits atomic.Int32
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		ua = r.Header.Get("User-Agent")
		if r.URL.Path != "/v1/status" || r.Method != http.MethodGet {
			w.WriteHeader(404)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("the token or anything else travelled in the query: %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"a valid bearer token is required"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"customer":"jane-1042","reporting":true,"age_seconds":12,"engine":{"v":1,"state":"running","armed":true,"account":{"equity":6123.45},"open":[],"recent_closes":[],"results":{"closes":2,"net":2.2}},"vault":{"key_held":true,"signs":812}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &ua
}

func TestMyEngineStatusReadsTheUsersEngineWithTheTokenFromTheEnvironment(t *testing.T) {
	const tok = "st-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	srv, hits, ua := engineStub(t, tok)
	ts := newToolset(&client.Client{UserAgent: "scalpmcp/test"}, endpoints{Engine: srv.URL})

	// No token: an honest answer and no request.
	ts.statusToken = func() string { return "" }
	text, structured, err := ts.call(context.Background(), "my_engine_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if structured["complete"] != false || !strings.Contains(text, "SCALPSTREAM_STATUS_TOKEN") || !strings.Contains(text, "pricing-plans") {
		t.Fatalf("without a token: %s", text)
	}
	if hits.Load() != 0 {
		t.Fatal("a request was made with no token")
	}
	// The token: the status, complete, with the note that it is a record.
	ts.statusToken = func() string { return tok }
	text, structured, err = ts.call(context.Background(), "my_engine_status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if structured["complete"] != true || structured["paid"] != false {
		t.Fatalf("with the token: %s", text)
	}
	data, _ := structured["data"].(map[string]any)
	if data["customer"] != "jane-1042" || data["reporting"] != true {
		t.Fatalf("data: %v", data)
	}
	if !strings.Contains(text, "not a forecast") {
		t.Fatalf("the answer does not say it is a record: %s", text)
	}
	if *ua != "scalpmcp/test" {
		t.Errorf("User-Agent = %q", *ua)
	}
	// A refused token: not an error, a reason and the how-to, token never echoed.
	ts.statusToken = func() string { return "st-wrong" }
	text, structured, err = ts.call(context.Background(), "my_engine_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if structured["complete"] != false || !strings.Contains(text, "refused (401)") || strings.Contains(text, "st-wrong") {
		t.Fatalf("refused token: %s", text)
	}
}

func TestMyEngineStatusNeverLogsTheToken(t *testing.T) {
	const tok = "st-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv, _, _ := engineStub(t, tok)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)
	ts := newToolset(&client.Client{}, endpoints{Engine: srv.URL})
	ts.statusToken = func() string { return tok }
	if _, _, err := ts.call(context.Background(), "my_engine_status", nil); err != nil {
		t.Fatal(err)
	}
	// And through the transport, with a dead host: the error names the URL,
	// never the token.
	dead := newToolset(&client.Client{}, endpoints{Engine: "http://127.0.0.1:1"})
	dead.statusToken = func() string { return tok }
	_, _, err := dead.call(context.Background(), "my_engine_status", nil)
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("dead host: %v", err)
	}
	if strings.Contains(buf.String(), tok) || strings.Contains(buf.String(), "aaaaaaaa") {
		t.Fatalf("the token reached the log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "tool=my_engine_status") {
		t.Fatalf("no call line:\n%s", buf.String())
	}
}

func TestMyEngineStatusIsListedAsFreeReadOnlyAndRepeatable(t *testing.T) {
	got := drive(t, previewOnlyToolset(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, raw := range got[0]["result"].(map[string]any)["tools"].([]any) {
		tl := raw.(map[string]any)
		if tl["name"] != "my_engine_status" {
			continue
		}
		ann := tl["annotations"].(map[string]any)
		if ann["readOnlyHint"] != true || ann["idempotentHint"] != true || ann["openWorldHint"] != true || ann["destructiveHint"] != false {
			t.Fatalf("annotations: %v", ann)
		}
		desc, _ := tl["description"].(string)
		for _, must := range []string{"record and not a forecast", "SCALPSTREAM_STATUS_TOKEN", "costs nothing"} {
			if !strings.Contains(desc, must) {
				t.Errorf("description lacks %q", must)
			}
		}
		return
	}
	t.Fatal("my_engine_status is not listed")
}

// 2026-11-04 review, findings 4 and 11: NaN is not a number for these tools,
// and a symbol must be a FIELD of the trades, not a substring.
func TestNaNIsNotACoordinateAndASymbolMustBeAField(t *testing.T) {
	if _, ok := argNum(map[string]any{"lat": "NaN"}, "lat"); ok {
		t.Error(`argNum accepted "NaN"`)
	}
	if _, ok := argNum(map[string]any{"lat": math.Inf(1)}, "lat"); ok {
		t.Error("argNum accepted +Inf")
	}
	if _, ok := argNum(map[string]any{"lat": "12.5"}, "lat"); !ok {
		t.Error(`argNum refused "12.5"`)
	}
	if _, _, err := requireLatLon(map[string]any{"lat": "NaN", "lon": 0.0}); err == nil {
		t.Error("requireLatLon accepted lat=NaN")
	}
	trades := "fid,taxable,2026-08-01,PLTR,buy,100,10;fid,trad_ira,2026-08-02,PLTR,sell,100,9"
	if tradesName(trades, "A") || tradesName(trades, "IRA") || tradesName(trades, "TAX") {
		t.Error("a substring of a field passed as a symbol")
	}
	if !tradesName(trades, "pltr") || !tradesName(`[{"symbol":"PLTR","kind":"buy"}]`, "PLTR") {
		t.Error("a real symbol was not found")
	}
}
