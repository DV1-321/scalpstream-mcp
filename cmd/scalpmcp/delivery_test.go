package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
	"github.com/DV1-321/scalpstream-mcp/pay"
)

// deliveryTx is the settlement hash the stub seller reports. Split so no file
// carries a 64-digit hex literal.
const deliveryTx = "0x" + "ab12cd34ef56ab12cd34ef56ab12cd34" + "ef56ab12cd34ef56ab12cd34ef56ab12"

// stubSeller answers /preview with previewBody, an unpaid request with a 402
// (or, when free, with body directly) and a paid one with body. It counts the
// payments presented and remembers the last authorization nonce.
type stubSeller struct {
	*httptest.Server
	payments  atomic.Int32
	lastNonce atomic.Value
}

func newStubSeller(t *testing.T, body, previewBody []byte, free bool) *stubSeller {
	t.Helper()
	s := &stubSeller{}
	s.lastNonce.Store("")
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/preview") {
			_, _ = w.Write(previewBody)
			return
		}
		sig := r.Header.Get("PAYMENT-SIGNATURE")
		if sig == "" && !free {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"10000",` +
				`"asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111",` +
				`"extra":{"name":"USD Coin","version":"2"}}]}`))
			return
		}
		if sig != "" {
			s.payments.Add(1)
			raw, _ := base64.StdEncoding.DecodeString(sig)
			var env struct {
				Payload struct {
					Authorization struct {
						Nonce string `json:"nonce"`
					} `json:"authorization"`
				} `json:"payload"`
			}
			_ = json.Unmarshal(raw, &env)
			s.lastNonce.Store(env.Payload.Authorization.Nonce)
			settled, _ := json.Marshal(map[string]any{"success": true, "transaction": deliveryTx, "network": "eip155:8453"})
			w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(settled))
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func payingToolset(t *testing.T, origin string) *toolset {
	t.Helper()
	// The client package's test key: never used on any network, never to be funded.
	signer, err := pay.NewSigner("0xcc20f897f3181022d04694efab0c83f1697b2a1e0da3359ec25f27cce9c1ccd2")
	if err != nil {
		t.Fatal(err)
	}
	return newToolset(&client.Client{Signer: signer},
		endpoints{Fuel: origin, Air: origin, Border: origin, Recall: origin, Basis: origin})
}

const borderCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"border_crossings","arguments":{"lat":32.7157,"lon":-117.1611}}}`

// callBorder runs one border_crossings call through the real transport loop
// and returns the JSON-RPC result and the one log line the call wrote.
func callBorder(t *testing.T, ts *toolset) (result map[string]any, logLine string) {
	t.Helper()
	buf := captureLog(t)
	got := drive(t, ts, borderCall)
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1: %v", len(got), got)
	}
	if got[0]["error"] != nil {
		t.Fatalf("protocol error: %v", got[0]["error"])
	}
	return got[0]["result"].(map[string]any), strings.TrimSpace(buf.String())
}

// envelopeOf returns structuredContent after checking the text block is the
// same value: a client reading either must see the same thing.
func envelopeOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text), &fromText); err != nil {
		t.Fatalf("the text block is not JSON: %v", err)
	}
	if !reflect.DeepEqual(fromText, structured) {
		t.Error("structuredContent and the text block disagree")
	}
	return structured
}

// assertPaidNotDelivered checks everything a caller needs when the money moved
// and the reply could not be handed over: a RESULT (not an error), paid=true,
// complete=false, the reason, the payment reference, and the instruction not to
// call again.
func assertPaidNotDelivered(t *testing.T, ts *toolset, seller *stubSeller, result map[string]any, logLine, wantWhy string) map[string]any {
	t.Helper()
	if result["isError"] == true {
		t.Fatalf("a paid call whose reply could not be delivered was reported as a tool error: %v", result["content"])
	}
	env := envelopeOf(t, result)
	if env["paid"] != true {
		t.Errorf("paid = %v; the payment settled and the envelope must say so", env["paid"])
	}
	if env["complete"] != false {
		t.Errorf("complete = %v; nothing usable was delivered", env["complete"])
	}
	if _, has := env["data"]; has {
		t.Errorf("data = %v; an undeliverable reply must not be presented as data", env["data"])
	}
	if why, _ := env["undelivered"].(string); !strings.Contains(why, wantWhy) {
		t.Errorf("undelivered = %q, want it to say %q", why, wantWhy)
	}
	note, _ := env["note"].(string)
	for _, want := range []string{"PAID, NOT DELIVERED", "settled", "Do NOT call this tool again"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
	p, ok := env["payment"].(map[string]any)
	if !ok {
		t.Fatalf("no payment reference in the envelope: %v", env)
	}
	if nonce, _ := p["authorization_nonce"].(string); nonce == "" || nonce != seller.lastNonce.Load().(string) {
		t.Errorf("payment.authorization_nonce = %q, but the nonce presented was %q", nonce, seller.lastNonce.Load())
	}
	if p["transaction"] != deliveryTx {
		t.Errorf("payment.transaction = %v, want the seller's %s", p["transaction"], deliveryTx)
	}
	payer, _ := p["payer"].(string)
	payTo, _ := p["pay_to"].(string)
	if p["amount_atomic"] != "10000" || p["network"] != "eip155:8453" || payer == "" || payTo == "" {
		t.Errorf("payment = %v, want the terms that were signed", p)
	}

	// The one log line tells the same story, and still carries no argument.
	m := callLine.FindStringSubmatch(logLine)
	if m == nil {
		t.Fatalf("log line %q does not match the call format", logLine)
	}
	if m[2] != "true" || m[3] != "false" || m[4] != "200" || m[6] != "false" {
		t.Errorf("logged %q, want paid=true complete=false status=200 error=false", logLine)
	}

	// Charged once, and counted once.
	if n := seller.payments.Load(); n != 1 {
		t.Errorf("the seller saw %d payments, want 1", n)
	}
	if spent, calls := ts.client.Spent(); spent != "10000" || calls != 1 {
		t.Errorf("payment_status would show %s over %d calls, want 10000 over 1", spent, calls)
	}
	return env
}

// Review 2026-11-04, finding 3. A paid 200 whose body was not JSON was reported
// as the tool error "encoding the result: ... invalid character '<'", with the
// money already moved and nothing in the message saying so. A model reads that
// as a failure to retry, and each retry is another payment.
func TestPaidReplyThatIsNotJSONSaysThePaymentSettled(t *testing.T) {
	seller := newStubSeller(t, []byte(`<html>oops</html>`), nil, false)
	ts := payingToolset(t, seller.URL)

	result, logLine := callBorder(t, ts)
	env := assertPaidNotDelivered(t, ts, seller, result, logLine, "not JSON")
	// The reply is whole and it is text: it was paid for, so it is handed over.
	if env["data_text"] != `<html>oops</html>` {
		t.Errorf("data_text = %v, want the seller's reply verbatim", env["data_text"])
	}
}

// The same for a reply past the client's 1 MiB cap: it used to be cut silently
// and fail as "unexpected end of JSON input".
func TestPaidReplyOverTheSizeLimitSaysThePaymentSettled(t *testing.T) {
	big := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), client.MaxBodyBytes+512)...)
	big = append(big, `"}`...)
	seller := newStubSeller(t, big, nil, false)
	ts := payingToolset(t, seller.URL)

	result, logLine := callBorder(t, ts)
	env := assertPaidNotDelivered(t, ts, seller, result, logLine, "1048576-byte limit")
	if _, has := env["data_text"]; has {
		t.Error("a megabyte of cut-off JSON must not be passed on as data_text")
	}
}

// An empty paid 200 is the same case.
func TestPaidEmptyReplySaysThePaymentSettled(t *testing.T) {
	seller := newStubSeller(t, nil, nil, false)
	ts := payingToolset(t, seller.URL)
	result, logLine := callBorder(t, ts)
	assertPaidNotDelivered(t, ts, seller, result, logLine, "empty")
}

// A paid reply that IS deliverable is unchanged: data, complete=true, and none
// of the undelivered fields.
func TestPaidJSONReplyIsDeliveredAsBefore(t *testing.T) {
	seller := newStubSeller(t, []byte(`{"crossings":[]}`), nil, false)
	ts := payingToolset(t, seller.URL)
	result, _ := callBorder(t, ts)
	env := envelopeOf(t, result)
	if env["paid"] != true || env["complete"] != true {
		t.Errorf("paid=%v complete=%v, want both true", env["paid"], env["complete"])
	}
	if !reflect.DeepEqual(env["data"], map[string]any{"crossings": []any{}}) {
		t.Errorf("data = %v", env["data"])
	}
	for _, field := range []string{"undelivered", "data_text", "payment", "note"} {
		if _, has := env[field]; has {
			t.Errorf("a delivered paid result carries %q: %v", field, env[field])
		}
	}
}

// The unpaid paths had the same "encoding the result" failure. Nothing was
// charged there, and the result says so instead of failing.
func TestUndeliverableFreeRepliesAreResultsThatSayNothingWasCharged(t *testing.T) {
	oversize := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), client.MaxBodyBytes+512)...)
	for name, tc := range map[string]struct {
		body     []byte
		wantWhy  string
		wantText any
	}{
		"not JSON": {[]byte("service temporarily unavailable"), "not JSON", "service temporarily unavailable"},
		"oversize": {oversize, "1048576-byte limit", nil},
		"binary":   {[]byte{0xff, 0xfe, 0x00, 0x01}, "binary", nil},
	} {
		t.Run(name, func(t *testing.T) {
			seller := newStubSeller(t, tc.body, nil, true)
			ts := newToolset(&client.Client{}, endpoints{Border: seller.URL})
			_, structured, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
			if err != nil {
				t.Fatalf("an undeliverable free reply must be a result, got the error %v", err)
			}
			if structured["paid"] != false || structured["complete"] != false {
				t.Errorf("paid=%v complete=%v, want both false", structured["paid"], structured["complete"])
			}
			if why, _ := structured["undelivered"].(string); !strings.Contains(why, tc.wantWhy) {
				t.Errorf("undelivered = %q, want it to say %q", why, tc.wantWhy)
			}
			if note, _ := structured["note"].(string); !strings.Contains(note, "nothing was charged") {
				t.Errorf("note = %q, want it to say nothing was charged", note)
			}
			if _, has := structured["payment"]; has {
				t.Error("an unpaid result names a payment")
			}
			if !reflect.DeepEqual(structured["data_text"], tc.wantText) {
				t.Errorf("data_text = %v, want %v", structured["data_text"], tc.wantText)
			}
		})
	}
}

// And the free preview shown when payment is not possible: a preview that is
// not JSON no longer turns the whole fallback (price, how to enable) into an
// error.
func TestPreviewThatIsNotJSONKeepsTheQuote(t *testing.T) {
	seller := newStubSeller(t, []byte(`{"crossings":[]}`), []byte("<html>maintenance</html>"), false)
	ts := newToolset(&client.Client{}, endpoints{Border: seller.URL}) // no key: preview fallback
	_, structured, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
	if err != nil {
		t.Fatalf("the preview fallback must not fail on a non-JSON preview: %v", err)
	}
	if structured["paid"] != false || structured["complete"] != false {
		t.Errorf("paid=%v complete=%v, want both false", structured["paid"], structured["complete"])
	}
	if price, _ := structured["price"].(map[string]any); price["amount_usd"] != "0.0100" {
		t.Errorf("the quote was lost: price = %v", structured["price"])
	}
	if why, _ := structured["undelivered"].(string); !strings.Contains(why, "not JSON") {
		t.Errorf("undelivered = %q", why)
	}
	if seller.payments.Load() != 0 {
		t.Error("a preview fallback presented a payment")
	}
}

// The envelope's new fields are declared, so a client validating
// structuredContent against outputSchema accepts them.
func TestOutputSchemaDeclaresTheUndeliveredFields(t *testing.T) {
	props := envelopeSchema("x")["properties"].(map[string]any)
	for _, field := range []string{"undelivered", "data_text", "payment"} {
		if props[field] == nil {
			t.Errorf("outputSchema does not declare %q", field)
		}
	}
	pay := props["payment"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"network", "asset", "amount_atomic", "amount_usd", "pay_to", "payer", "authorization_nonce", "transaction"} {
		if pay[field] == nil {
			t.Errorf("outputSchema.payment does not declare %q", field)
		}
	}
}
