package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
	"github.com/DV1-321/scalpstream-mcp/pay"
)

// challengeServer answers 402 with a standard x402 v2 challenge quoting amount,
// and 200 once a payment header is presented. It records how many payments it
// saw, so a test can prove a refusal never reached the wire.
func challengeServer(t *testing.T, amount string) (*httptest.Server, *int) {
	t.Helper()
	paid := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			paid++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":"the goods"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"` +
			amount + `","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111","extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &paid
}

// A client with no signer must never attempt payment, and must report the price
// so a caller can decide whether to enable one.
func TestNoSignerRefusesAndQuotes(t *testing.T) {
	srv, paid := challengeServer(t, "10000")
	c := &client.Client{}
	_, err := c.Fetch(context.Background(), srv.URL)

	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if *paid != 0 {
		t.Errorf("a client with no key must not present a payment; server saw %d", *paid)
	}
	if pay.Quote.AmountUSD != "0.0100" {
		t.Errorf("quote = %q, want 0.0100", pay.Quote.AmountUSD)
	}
	if !strings.Contains(pay.Reason, "EVM_BASE_PRIVATE_KEY") {
		t.Errorf("reason should tell the caller how to enable payment, got %q", pay.Reason)
	}
}

// testSigner uses a key generated for these tests that has never been used on
// any network. It only makes the paying code path reachable against stub servers
// that settle nothing. It must never be funded, and must never be copied into
// anything that spends.
func testSigner(t *testing.T) *pay.Signer {
	t.Helper()
	s, err := pay.NewSigner("0xcc20f897f3181022d04694efab0c83f1697b2a1e0da3359ec25f27cce9c1ccd2")
	if err != nil {
		t.Fatalf("test signer: %v", err)
	}
	return s
}

// The per-call cap must be enforced BEFORE signing, so an overpriced resource
// costs nothing at all rather than being paid and then reported.
func TestPerCallCapRefusesBeforeSigning(t *testing.T) {
	srv, paid := challengeServer(t, "5000000") // $5.00, far above the cap
	c := &client.Client{Signer: testSigner(t), MaxPriceAtomic: big.NewInt(100_000)}
	_, err := c.Fetch(context.Background(), srv.URL)

	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if !strings.Contains(pay.Reason, "per-call cap") {
		t.Errorf("reason = %q, want it to name the per-call cap", pay.Reason)
	}
	if *paid != 0 {
		t.Errorf("an over-cap quote must never be paid; server saw %d payments", *paid)
	}
}

// The session budget must stop spending once exhausted, and the calls made
// before that point must still have succeeded. A budget that refuses everything,
// or one that never refuses, are both failures.
func TestSessionBudgetStopsSpendingAfterItIsExhausted(t *testing.T) {
	srv, paid := challengeServer(t, "10000") // $0.01 a call
	c := &client.Client{
		Signer: testSigner(t),
		Budget: big.NewInt(25_000), // $0.025 — enough for exactly two calls
	}
	for i := 1; i <= 2; i++ {
		if _, err := c.Fetch(context.Background(), srv.URL); err != nil {
			t.Fatalf("call %d should have succeeded within budget: %v", i, err)
		}
	}
	_, err := c.Fetch(context.Background(), srv.URL)
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("third call should exceed the budget, got %v", err)
	}
	if !strings.Contains(pay.Reason, "budget") {
		t.Errorf("reason = %q, want it to name the budget", pay.Reason)
	}
	if *paid != 2 {
		t.Errorf("exactly 2 payments should have reached the server, got %d", *paid)
	}
	spent, calls := c.Spent()
	if spent != "20000" || calls != 2 {
		t.Errorf("accounting = %s over %d calls, want 20000 over 2", spent, calls)
	}
}

// Spend is recorded only when the resource is actually delivered. Counting at
// signing time would overstate spending whenever a paid request then failed.
func TestSpendIsRecordedOnlyOnDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"10000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111","extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	defer srv.Close()

	c := &client.Client{Signer: testSigner(t)}
	_, err := c.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("a 500 on the paid retry must be an error")
	}
	// The failure must be explicit that settlement state is unknown, because the
	// caller may need to reconcile against the chain.
	if !strings.Contains(err.Error(), "settlement state unknown") {
		t.Errorf("error should flag the ambiguity, got %q", err)
	}
	if spent, calls := c.Spent(); spent != "0" || calls != 0 {
		t.Errorf("undelivered resource must not count as spend; got %s over %d", spent, calls)
	}
}

// An unset limit must mean the conservative default, never "unlimited". Getting
// this backwards would turn a missing config line into unbounded spending.
func TestZeroLimitsMeanDefaultsNotUnlimited(t *testing.T) {
	// $3.00 is above the $2.00 default budget and above the $0.10 default cap.
	srv, paid := challengeServer(t, "3000000")
	c := &client.Client{} // both limits left at zero
	_, err := c.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("a $3.00 quote must not be paid under default limits")
	}
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if *paid != 0 {
		t.Errorf("nothing should have been paid, server saw %d", *paid)
	}
}

// A free (200) resource must be returned untouched and never counted as spend.
func TestFreeResourceIsNotCharged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"free":true}`))
	}))
	defer srv.Close()

	c := &client.Client{}
	body, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("free fetch failed: %v", err)
	}
	if string(body) != `{"free":true}` {
		t.Errorf("body = %s", body)
	}
	spent, calls := c.Spent()
	if spent != "0" || calls != 0 {
		t.Errorf("a free fetch must not register spend; got %s over %d calls", spent, calls)
	}
}

// A 402 offering only rails this client cannot settle must fail clearly rather
// than picking something it cannot sign for.
func TestNonEVMOnlyChallengeIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"xrpl:0","amount":"9669","asset":"XRP","payTo":"rAbc"}]}`))
	}))
	defer srv.Close()

	c := &client.Client{}
	_, err := c.Fetch(context.Background(), srv.URL)
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if !strings.Contains(pay.Reason, "EVM") {
		t.Errorf("reason = %q, want it to explain the rail mismatch", pay.Reason)
	}
}

// The challenge must still be readable when the PAYMENT-REQUIRED header is
// missing — some intermediaries strip unknown headers, and the body carries the
// same JSON.
func TestChallengeReadFromBodyWhenHeaderAbsent(t *testing.T) {
	srv, _ := challengeServer(t, "10000")
	c := &client.Client{}
	_, err := c.Fetch(context.Background(), srv.URL)
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("challenge should have been parsed from the body, got %v", err)
	}
	if pay.Quote.Network != "eip155:8453" {
		t.Errorf("network = %q", pay.Quote.Network)
	}
}

// A 402 with no usable challenge at all is an error, not a silent success.
func TestUnparseableChallengeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`<html>please pay</html>`))
	}))
	defer srv.Close()

	c := &client.Client{}
	if _, err := c.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("a 402 with no parseable challenge must be an error")
	}
}

// Base must be chosen over other EVM rails even when it is not first, because
// accepts[] order is only a hint and Base is where the catalog entry lives.
func TestBaseIsPreferredOverOtherEVMRails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[
          {"scheme":"exact","network":"eip155:137","amount":"10000","asset":"0x3c499c542cEF5E3811e1192ce70d8cc03d5c3359","payTo":"0x1111111111111111111111111111111111111111"},
          {"scheme":"exact","network":"eip155:8453","amount":"10000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111"}]}`))
	}))
	defer srv.Close()

	c := &client.Client{}
	_, err := c.Fetch(context.Background(), srv.URL)
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if pay.Quote.Network != "eip155:8453" {
		t.Errorf("selected %s; Base should win regardless of array order", pay.Quote.Network)
	}
}

// The quote surfaced to a caller must be the server's real terms, so a user
// deciding whether to fund a wallet is looking at accurate numbers.
func TestQuoteReportsServerTerms(t *testing.T) {
	srv, _ := challengeServer(t, "250000") // $0.25
	c := &client.Client{}
	_, err := c.Fetch(context.Background(), srv.URL)
	var pay *client.ErrPaymentRequired
	if !errors.As(err, &pay) {
		t.Fatalf("want ErrPaymentRequired, got %v", err)
	}
	if pay.Quote.AmountAtomic != "250000" || pay.Quote.AmountUSD != "0.2500" {
		b, _ := json.Marshal(pay.Quote)
		t.Errorf("quote = %s, want 250000 / 0.2500", b)
	}
	if pay.Quote.PayTo != "0x1111111111111111111111111111111111111111" {
		t.Errorf("payTo = %q", pay.Quote.PayTo)
	}
}

// The budget must hold under concurrent calls.
//
// The check used to read `spent`, release the lock, and only add after the
// resource came back — so two calls could both pass a budget neither should
// have passed. That was harmless while the MCP server dispatched serially, and
// an overspend the moment it stopped. Reserving under one lock is what closes it.
//
// (The race detector needs cgo, which this toolchain does not have; this test
// exercises the invariant rather than the memory model.)
func TestBudgetHoldsUnderConcurrentCalls(t *testing.T) {
	const price = 10_000 // $0.01
	var served int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			atomic.AddInt32(&served, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453",
			"amount":"10000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x0000000000000000000000000000000000000001",
			"extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	defer srv.Close()

	signer, err := pay.NewSigner("0x1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	// Budget for exactly 3 calls; 20 goroutines race for them.
	c := &client.Client{
		Signer: signer,
		Budget: big.NewInt(3 * price),
		HTTP:   srv.Client(),
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Fetch(context.Background(), srv.URL+"/picks")
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&served); got > 3 {
		t.Errorf("seller was paid %d times against a 3-call budget", got)
	}
	spent, calls := c.Spent()
	if calls > 3 {
		t.Errorf("recorded %d paid calls against a 3-call budget", calls)
	}
	want := big.NewInt(int64(calls) * price)
	if spent != want.String() {
		t.Errorf("spent = %s, want %s for %d calls", spent, want, calls)
	}
}

// A call the client REFUSED must not leave money reserved, or a budget bleeds
// away on requests that never happened.
func TestRefusedCallReleasesItsReservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453",
			"amount":"10000","asset":"0xUSDC","payTo":"0xrecv","extra":{"name":"USD Coin","version":"2"}}]}`))
	}))
	defer srv.Close()

	// No signer: every call is refused before anything is signed.
	c := &client.Client{HTTP: srv.Client()}
	for i := 0; i < 50; i++ {
		if _, err := c.Fetch(context.Background(), srv.URL+"/picks"); err == nil {
			t.Fatal("a call with no signer should be refused")
		}
	}
	if held, n := c.Unconfirmed(); held != "0" || n != 0 {
		t.Errorf("refused calls left %s held over %d unconfirmed; want 0/0", held, n)
	}
}
