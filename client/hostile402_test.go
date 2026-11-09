package client_test

// 2026-11-04 review, finding 1: the client signed whatever asset, chain and
// payee a 402 named. A hostile or compromised seller origin could obtain an
// EIP-3009 authorization from the user's key for an arbitrary token on any
// EVM chain, with the cap counted in that token's unknown atomic units. Now
// only Circle's USDC on a chain pinned in pay.go is ever signed.

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
)

func hostileSeller(t *testing.T, accepts string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var paidAttempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			paidAttempts.Add(1)
			_, _ = w.Write([]byte(`{"got":"your signature"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[` + accepts + `]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &paidAttempts
}

// Sweep 2026-11-08, finding G-2. An explicit zero limit means spend nothing,
// so even a quote of zero is refused unsigned. A nil limit is still the default.
func TestAZeroLimitRefusesEveryQuoteUnsigned(t *testing.T) {
	const genuine = `{"scheme":"exact","network":"eip155:8453","amount":"%s","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111"}`
	for name, limits := range map[string]func(*client.Client){
		"budget": func(c *client.Client) { c.Budget = big.NewInt(0) },
		"cap":    func(c *client.Client) { c.MaxPriceAtomic = big.NewInt(0) },
	} {
		for _, amount := range []string{"10000", "0"} {
			t.Run(name+" quoted "+amount, func(t *testing.T) {
				srv, paid := hostileSeller(t, fmt.Sprintf(genuine, amount))
				c := &client.Client{Signer: testSigner(t)}
				limits(c)
				if c.CanPay() {
					t.Error("CanPay is true with a zero limit")
				}
				_, err := c.Fetch(context.Background(), srv.URL)
				var refused *client.ErrPaymentRequired
				if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "turned off") {
					t.Fatalf("got %v, want a refusal saying spending is turned off", err)
				}
				if paid.Load() != 0 {
					t.Fatal("a zero limit signed a payment")
				}
			})
		}
	}
	srv, paid := hostileSeller(t, fmt.Sprintf(genuine, "10000"))
	c := &client.Client{Signer: testSigner(t)}
	if _, err := c.Fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("nil limits must still mean the defaults: %v", err)
	}
	if paid.Load() != 1 {
		t.Fatalf("paid attempts = %d, want 1", paid.Load())
	}
}

func TestAnOptionNamingAnotherAssetOrChainIsNeverSigned(t *testing.T) {
	cases := map[string]string{
		"a lookalike token on Base": `{"scheme":"exact","network":"eip155:8453","amount":"100000","asset":"0x000000000000000000000000000000000000dEaD","payTo":"0x4141414141414141414141414141414141414141","extra":{"name":"Not USDC","version":"9"}}`,
		"USDC's address but on mainnet (chain 1), which this client does not know": `{"scheme":"exact","network":"eip155:1","amount":"100000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x4141414141414141414141414141414141414141"}`,
		"the Polygon USDC address offered on Arbitrum": `{"scheme":"exact","network":"eip155:42161","amount":"100000","asset":"0x3c499c542cEF5E3811e1192ce70d8cc03d5c3359","payTo":"0x4141414141414141414141414141414141414141"}`,
	}
	for name, accept := range cases {
		t.Run(name, func(t *testing.T) {
			srv, paid := hostileSeller(t, accept)
			c := &client.Client{Signer: testSigner(t)}
			_, err := c.Fetch(context.Background(), srv.URL)
			var pay *client.ErrPaymentRequired
			if !errors.As(err, &pay) {
				t.Fatalf("want ErrPaymentRequired, got %v", err)
			}
			if !strings.Contains(pay.Reason, "USDC") {
				t.Errorf("reason = %q, want it to name USDC", pay.Reason)
			}
			if paid.Load() != 0 {
				t.Fatal("a payment was signed and sent for a hostile option")
			}
			if spent, calls := c.Spent(); spent != "0" || calls != 0 {
				t.Errorf("spend recorded: %s over %d", spent, calls)
			}
		})
	}
	// The real thing, beside a hostile option, is still paid: the hostile
	// option is skipped, not fatal.
	srv, paid := hostileSeller(t,
		`{"scheme":"exact","network":"eip155:8453","amount":"100000","asset":"0x000000000000000000000000000000000000dEaD","payTo":"0x4141414141414141414141414141414141414141"},`+
			`{"scheme":"exact","network":"eip155:8453","amount":"10000","asset":"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913","payTo":"0x1111111111111111111111111111111111111111","extra":{"name":"USD Coin","version":"2"}}`)
	c := &client.Client{Signer: testSigner(t)}
	if _, err := c.Fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("the genuine USDC option (lower-case address) was refused: %v", err)
	}
	if paid.Load() != 1 {
		t.Fatalf("paid attempts = %d, want 1", paid.Load())
	}
}
