package client_test

// 2026-11-10 review of the 0.1.14 branch, finding M-1. The paid request followed
// redirects. A seller that took the signed payment and then answered 302 to an
// address nothing listens on made the client see a dial error, which neverSent
// reads as "nothing was sent", so the reservation was released: ten $0.10
// payments went out against a $0.20 budget that still read nothing spent. The
// signed payment was also carried to whatever host the redirect named.

import (
	"context"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
)

const redirectQuote = `{"x402Version":2,"accepts":[{"scheme":"exact","network":"eip155:8453","amount":"100000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111"}]}`

// redirectingSeller answers 402 unpaid and, once paid, redirects to target.
func redirectingSeller(t *testing.T, target func() string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var paid atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") == "" {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(redirectQuote))
			return
		}
		paid.Add(1) // a hostile seller settles here
		http.Redirect(w, r, target(), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &paid
}

func TestARedirectAfterPaymentKeepsTheMoneyReserved(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String() + "/"
	_ = l.Close()

	srv, paid := redirectingSeller(t, func() string { return dead })
	c := &client.Client{Signer: testSigner(t), MaxPriceAtomic: big.NewInt(100_000), Budget: big.NewInt(200_000)}
	for i := 0; i < 10; i++ {
		if _, err := c.FetchResponse(context.Background(), srv.URL+"/x"); err == nil {
			t.Fatalf("call %d: a redirect after payment was reported as a success", i)
		}
	}
	if got := paid.Load(); got != 2 {
		t.Fatalf("the seller received %d signed payments of $0.10 against a $0.20 budget, want 2", got)
	}
	if _, n := c.Unconfirmed(); n != 2 {
		t.Fatalf("unconfirmed calls = %d, want 2: the payments were presented and their outcome is unknown", n)
	}
}

func TestASignedPaymentIsNotCarriedToARedirectTarget(t *testing.T) {
	var carried, reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			carried.Add(1)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(other.Close)

	for name, target := range map[string]func(string) string{
		"another host":  func(string) string { return other.URL + "/" },
		"the same host": func(self string) string { return self + "/elsewhere" },
	} {
		t.Run(name, func(t *testing.T) {
			var self string
			srv, paid := redirectingSeller(t, func() string { return target(self) })
			self = srv.URL
			reached.Store(0)
			c := &client.Client{Signer: testSigner(t)}
			resp, err := c.FetchResponse(context.Background(), srv.URL+"/x")
			if err == nil || resp.Paid {
				t.Fatalf("a redirect after payment was reported as paid: %+v, %v", resp, err)
			}
			if resp.Status != http.StatusFound {
				t.Errorf("status = %d, want the seller's own 302", resp.Status)
			}
			if paid.Load() != 1 {
				t.Errorf("the seller saw %d paid requests, want 1 (the redirect must not be followed back to it)", paid.Load())
			}
			if reached.Load() != 0 || carried.Load() != 0 {
				t.Errorf("the redirect was followed: %d requests reached the other host, %d with the payment", reached.Load(), carried.Load())
			}
			if _, n := c.Unconfirmed(); n != 1 {
				t.Errorf("unconfirmed calls = %d, want 1", n)
			}
		})
	}
}

// The unpaid request is as it was: it follows a redirect, and the caller's own
// http.Client is not changed by a paid call.
func TestOnlyThePaidRequestStopsAtARedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"free":true}`))
	}))
	t.Cleanup(final.Close)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/", http.StatusFound)
	}))
	t.Cleanup(front.Close)

	own := &http.Client{}
	c := &client.Client{Signer: testSigner(t), HTTP: own}
	body, err := c.Fetch(context.Background(), front.URL+"/x")
	if err != nil || string(body) != `{"free":true}` {
		t.Fatalf("an unpaid redirect was not followed: %q, %v", body, err)
	}

	srv, _ := redirectingSeller(t, func() string { return final.URL + "/" })
	if _, err := c.FetchResponse(context.Background(), srv.URL+"/x"); err == nil {
		t.Fatal("a redirect after payment was reported as a success")
	}
	if own.CheckRedirect != nil {
		t.Fatal("the caller's http.Client was modified")
	}
}
