package client_test

// 2026-11-10 review of the 0.1.14 branch, finding M-1. The paid request followed
// redirects. A seller that took the signed payment and then answered 302 to an
// address nothing listens on made the client see a dial error, which neverSent
// reads as "nothing was sent", so the reservation was released: ten $0.10
// payments went out against a $0.20 budget that still read nothing spent. The
// signed payment was also carried to whatever host the redirect named.

import (
	"bufio"
	"context"
	"fmt"
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

// The same review, second pass (M-1b). No redirect is needed: the seller answers
// the unpaid GET on a keep-alive connection, reads the paid GET on that same
// connection, closes it without answering and stops listening. Go's transport
// retries a GET whose reused connection died before any answer, the retry's dial
// is refused, and that dial error used to read as "nothing was sent".
func TestAPaymentReadOnAReusedConnectionStaysReserved(t *testing.T) {
	var got atomic.Int32
	c := &client.Client{Signer: testSigner(t), MaxPriceAtomic: big.NewInt(100_000), Budget: big.NewInt(200_000)}
	for round := 0; round < 6; round++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			br := bufio.NewReader(conn)
			for {
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.Header.Get("PAYMENT-SIGNATURE") == "" {
					_, _ = fmt.Fprintf(conn, "HTTP/1.1 402 Payment Required\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(redirectQuote), redirectQuote)
					continue
				}
				got.Add(1) // the signed payment is in the seller's hands
				_ = l.Close()
				return
			}
		}()
		if _, err := c.FetchResponse(context.Background(), "http://"+l.Addr().String()+"/x"); err == nil {
			t.Fatalf("round %d: a dropped paid request was reported as a success", round)
		}
		// Once the budget is used up no paid request follows the 402, and the
		// seller would wait on the idle connection for ever.
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		<-done
		_ = l.Close()
	}
	if n := got.Load(); n != 2 {
		t.Fatalf("the seller read %d signed payments of $0.10 against a $0.20 budget, want 2", n)
	}
	if _, n := c.Unconfirmed(); n != 2 {
		t.Fatalf("unconfirmed calls = %d, want 2", n)
	}
}

// A seller that is gone before the paid request gets any connection has seen
// nothing, and the money goes back: an outage must not eat the budget.
func TestAPaidRequestThatNeverGetsAConnectionIsReleased(t *testing.T) {
	c := &client.Client{Signer: testSigner(t), MaxPriceAtomic: big.NewInt(100_000), Budget: big.NewInt(200_000)}
	for round := 0; round < 5; round++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
				return
			}
			_ = l.Close() // gone before the paid request can connect
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 402 Payment Required\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(redirectQuote), redirectQuote)
		}()
		if _, err := c.FetchResponse(context.Background(), "http://"+l.Addr().String()+"/x"); err == nil {
			t.Fatalf("round %d: a refused connection was reported as a success", round)
		}
		<-done
	}
	if held, n := c.Unconfirmed(); n != 0 {
		t.Fatalf("an outage held %s over %d calls; nothing was presented, so nothing should be held", held, n)
	}
}
