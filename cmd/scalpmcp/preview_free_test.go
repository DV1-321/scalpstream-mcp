package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Sweep 2026-11-08, finding G-1. When the paid URL was refused, the "free
// preview" was fetched through the paying client. A /preview answering 402 with
// a quote under the cap was therefore signed and paid, and the envelope, hard
// coded, still said paid:false and "This is the FREE PREVIEW".
func TestAPreviewThatAsksForPaymentIsNeverPaid(t *testing.T) {
	const usdc = `"asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111","extra":{"name":"USD Coin","version":"2"}`
	var mainPaid, previewPaid atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isPreview := strings.HasPrefix(r.URL.Path, "/preview")
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			if isPreview {
				previewPaid.Add(1)
			} else {
				mainPaid.Add(1)
			}
			_, _ = w.Write([]byte(`{"got":"your signature"}`))
			return
		}
		amount := "500000" // the paid URL: over the default $0.10 cap
		if isPreview {
			amount = "10000" // the preview: comfortably under it
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"` + amount + `",` + usdc + `}]}`))
	}))
	t.Cleanup(srv.Close)

	ts := payingToolset(t, srv.URL)
	_, env, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
	if err != nil {
		t.Fatalf("the refusal must stay a result with the quote: %v", err)
	}
	if n := previewPaid.Load(); n != 0 {
		t.Errorf("the free preview was paid %d time(s)", n)
	}
	if n := mainPaid.Load(); n != 0 {
		t.Errorf("the over-cap paid URL was paid %d time(s)", n)
	}
	if spent, calls := ts.client.Spent(); spent != "0" || calls != 0 {
		t.Errorf("spent %s over %d calls, want nothing", spent, calls)
	}
	if held, n := ts.client.Unconfirmed(); held != "0" || n != 0 {
		t.Errorf("unconfirmed %s over %d calls, want nothing", held, n)
	}
	if env["paid"] != false || env["complete"] != false {
		t.Errorf("paid=%v complete=%v, want both false", env["paid"], env["complete"])
	}
	if price, _ := env["price"].(map[string]any); price["amount_atomic"] != "500000" {
		t.Errorf("price = %v, want the paid URL's quote", env["price"])
	}
	if _, has := env["data"]; has {
		t.Errorf("data = %v; no preview was delivered", env["data"])
	}
	if why, _ := env["error"].(string); !strings.Contains(why, "asked for payment") {
		t.Errorf("error = %q, want it to say the preview asked for payment", why)
	}
	// There is no preview in this result, so it must not claim to be one.
	note, _ := env["note"].(string)
	if strings.Contains(note, "This is the FREE PREVIEW") || !strings.Contains(note, "Nothing was charged") {
		t.Errorf("note = %q, want no preview claim and that nothing was charged", note)
	}
}
