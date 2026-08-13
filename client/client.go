// Package x402client is the BUYER side of x402: it fetches a paid resource,
// and if the server answers 402 it signs an EIP-3009 authorization on an EVM
// rail and retries.
//
// It exists so the payment path is written once. cmd/basebuyer and the MCP
// server both spend real money through it, and duplicated payment code is how
// two callers end up disagreeing about which offer they accepted.
//
// CUSTODY. This is the only place in the repo that holds a spending key, and it
// holds it only for the process lifetime, from the environment. The resource
// servers remain custody-free; that property is about the SELLER and is
// unaffected by a buyer signing its own payments.
//
// SPENDING. A Client with no Signer cannot pay: Fetch returns ErrPaymentRequired
// with the quoted terms instead, so a caller can run entirely on free previews
// and discover the price without ever risking a transaction. When a Signer IS
// present every call moves real funds, so two limits apply and both are checked
// BEFORE signing: MaxPriceAtomic caps any single call, and Budget caps the whole
// process. Exceeding either is a refusal, never a smaller payment.
package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DV1-321/scalpstream-mcp/pay"
	"github.com/DV1-321/scalpstream-mcp/x402"
)

// ErrPaymentRequired is returned when a resource needs payment the client
// cannot or will not make. Quote carries the server's terms so a caller can
// report the price, or decide to raise a limit.
type ErrPaymentRequired struct {
	Resource string
	Quote    Quote
	Reason   string
}

func (e *ErrPaymentRequired) Error() string {
	return fmt.Sprintf("payment required for %s: %s", e.Resource, e.Reason)
}

// Quote is the human-facing summary of one offered payment option.
type Quote struct {
	Network      string `json:"network"`
	Asset        string `json:"asset"`
	AmountAtomic string `json:"amount_atomic"`
	AmountUSD    string `json:"amount_usd,omitempty"`
	PayTo        string `json:"pay_to"`
}

// maxBody caps a resource read. Feeds return tens of kilobytes; a megabyte is
// generous and stops a misbehaving or hostile endpoint exhausting memory.
const maxBody = 1 << 20

// Client fetches x402-gated resources, paying when it can.
type Client struct {
	// HTTP is the transport. Nil uses a client with a generous timeout, because
	// abandoning a PAID request loses the money: settlement happens before the
	// handler runs, so a client that gives up mid-handler has still been charged.
	HTTP *http.Client

	// Signer holds the Base spending key. Nil means preview-only: the client
	// reports prices and never pays.
	Signer *pay.Signer

	// MaxPriceAtomic refuses any single call quoted above this, in the asset's
	// atomic units (USDC has 6 decimals, so 10000 is $0.01). Zero means the
	// DefaultMaxPrice below rather than "unlimited" — an unset cap must not be
	// the permissive case when real funds are involved.
	MaxPriceAtomic *big.Int

	// Budget caps total spend for this process, same units. Zero means
	// DefaultBudget, again deliberately not unlimited.
	Budget *big.Int

	// Attestation is sent as X-Compliance-Attestation; some feeds gate on it.
	Attestation string

	// PreferChainID picks which EVM rail to pay on when a resource offers
	// several, by EIP-155 chain id (8453 Base, 42161 Arbitrum One, 137 Polygon).
	// Zero keeps the default preference: Base, then whatever else is offered.
	//
	// A seller can advertise a rail it has never actually been paid on — the
	// challenge is signed and the facilitator will simulate it happily — so
	// "configured" and "proven" are different states, and telling them apart
	// requires choosing the rail deliberately rather than taking the default.
	// The wallet must hold the asset ON that chain; balances do not travel.
	PreferChainID int64

	mu    sync.Mutex
	spent big.Int
	calls int
}

// Conservative defaults. Every ScalpStream endpoint costs $0.01, so a $0.10 cap
// leaves room for a price change without silently authorising a large payment,
// and a $2.00 budget bounds a runaway loop to something recoverable.
var (
	DefaultMaxPrice = big.NewInt(100_000)   // $0.10 USDC
	DefaultBudget   = big.NewInt(2_000_000) // $2.00 USDC
)

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 150 * time.Second}
}

func (c *Client) maxPrice() *big.Int {
	if c.MaxPriceAtomic != nil && c.MaxPriceAtomic.Sign() > 0 {
		return c.MaxPriceAtomic
	}
	return DefaultMaxPrice
}

func (c *Client) budget() *big.Int {
	if c.Budget != nil && c.Budget.Sign() > 0 {
		return c.Budget
	}
	return DefaultBudget
}

// CanPay reports whether this client is configured to spend.
func (c *Client) CanPay() bool { return c.Signer != nil }

// Spent returns total atomic units spent and the number of paid calls made.
func (c *Client) Spent() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spent.String(), c.calls
}

// Fetch GETs url, paying if the server demands it.
//
// A 200 on the first try is returned as-is: a free endpoint costs nothing and is
// never charged for. Only a 402 triggers the payment path.
func (c *Client) Fetch(ctx context.Context, url string) ([]byte, error) {
	resp, err := c.do(ctx, url, "")
	if err != nil {
		return nil, err
	}
	body, status := resp.body, resp.status
	if status == http.StatusOK {
		return body, nil
	}
	if status != http.StatusPaymentRequired {
		return nil, fmt.Errorf("x402client: %s: HTTP %d: %s", url, status, snippet(body, 300))
	}

	ch, err := decodeChallenge(resp.header.Get(x402.HeaderPaymentRequiredStd), body)
	if err != nil {
		return nil, fmt.Errorf("x402client: %s: %w", url, err)
	}
	opt, chainID, ok := findEVMOption(ch, c.PreferChainID)
	if !ok {
		return nil, &ErrPaymentRequired{Resource: url, Reason: "no EVM (eip155) option offered; this client can only pay USDC on an EVM rail"}
	}
	quote := quoteOf(opt)

	if c.Signer == nil {
		return nil, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: "no spending key configured (set EVM_BASE_PRIVATE_KEY to enable paid calls)"}
	}

	amount, ok := new(big.Int).SetString(opt.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("x402client: %s: unparseable amount %q", url, opt.Amount)
	}
	// Both limits are checked BEFORE signing, so a refusal costs nothing.
	if amount.Cmp(c.maxPrice()) > 0 {
		return nil, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: fmt.Sprintf("quoted %s exceeds the per-call cap of %s atomic units", opt.Amount, c.maxPrice())}
	}
	c.mu.Lock()
	projected := new(big.Int).Add(&c.spent, amount)
	if projected.Cmp(c.budget()) > 0 {
		spentSoFar := c.spent.String()
		c.mu.Unlock()
		return nil, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: fmt.Sprintf("session budget exhausted: spent %s of %s atomic units", spentSoFar, c.budget())}
	}
	c.mu.Unlock()

	name, _ := opt.Extra["name"].(string)
	version, _ := opt.Extra["version"].(string)
	domain := pay.TokenDomain{Name: name, Version: version, ChainID: chainID, Contract: opt.Asset}
	exact, err := c.Signer.AuthorizeTransfer(domain, opt.PayTo, opt.Amount, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("x402client: %s: authorize: %w", url, err)
	}

	envelope := map[string]any{
		"x402Version": 2,
		"accepted":    opt,
		"payload":     exact,
		"resource":    url,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("x402client: encode payment: %w", err)
	}
	paid, err := c.do(ctx, url, base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		return nil, err
	}
	if paid.status != http.StatusOK {
		// The money may or may not have moved; say so rather than implying it did
		// not, because the caller may need to reconcile.
		return nil, fmt.Errorf("x402client: %s: paid request returned HTTP %d (settlement state unknown): %s",
			url, paid.status, snippet(paid.body, 300))
	}
	// Count the spend only once the resource is actually in hand.
	c.mu.Lock()
	c.spent.Add(&c.spent, amount)
	c.calls++
	c.mu.Unlock()
	return paid.body, nil
}

type rawResponse struct {
	status int
	body   []byte
	header http.Header
}

func (c *Client) do(ctx context.Context, url, payment string) (rawResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return rawResponse{}, err
	}
	if payment != "" {
		req.Header.Set(x402.HeaderPaymentSignature, payment)
	}
	if c.Attestation != "" {
		req.Header.Set("X-Compliance-Attestation", c.Attestation)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return rawResponse{}, fmt.Errorf("x402client: %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return rawResponse{}, fmt.Errorf("x402client: %s: read: %w", url, err)
	}
	return rawResponse{status: resp.StatusCode, body: body, header: resp.Header}, nil
}

// decodeChallenge reads the challenge from the PAYMENT-REQUIRED header, falling
// back to the response body. Both carry the same JSON; the header is base64 and
// some intermediaries drop unknown headers, so the body is the safety net.
func decodeChallenge(header string, body []byte) (x402.StdChallenge, error) {
	var ch x402.StdChallenge
	if header != "" {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header)); err == nil {
			if json.Unmarshal(raw, &ch) == nil && len(ch.Accepts) > 0 {
				return ch, nil
			}
		}
	}
	if err := json.Unmarshal(body, &ch); err != nil {
		return ch, errors.New("402 carried no parseable payment challenge")
	}
	if len(ch.Accepts) == 0 {
		return ch, errors.New("402 challenge offered no payment options")
	}
	return ch, nil
}

// findEVMOption picks a payable option, preferring Base because it is where the
// facilitator settles most cheaply and where the seller's catalog entry lives.
// Order in accepts[] is only a hint, so the choice is made here rather than
// trusting position.
// prefer is the caller's chosen EIP-155 chain id, or 0 for the default
// Base-first preference. A non-zero prefer that the resource does not offer is
// reported as no match rather than silently falling back — paying a different
// chain than the one asked for would defeat the point of asking.
func findEVMOption(ch x402.StdChallenge, prefer int64) (x402.StdAccept, int64, bool) {
	var fallback x402.StdAccept
	var fallbackChain int64
	for _, a := range ch.Accepts {
		if a.Scheme != "exact" || !strings.HasPrefix(a.Network, "eip155:") {
			continue
		}
		var chainID int64
		if _, err := fmt.Sscanf(a.Network, "eip155:%d", &chainID); err != nil {
			continue
		}
		if prefer != 0 {
			if chainID == prefer {
				return a, chainID, true
			}
			continue
		}
		if chainID == 8453 || chainID == 84532 {
			return a, chainID, true
		}
		if fallbackChain == 0 {
			fallback, fallbackChain = a, chainID
		}
	}
	if prefer != 0 {
		return x402.StdAccept{}, 0, false
	}
	if fallbackChain != 0 {
		return fallback, fallbackChain, true
	}
	return x402.StdAccept{}, 0, false
}

func quoteOf(a x402.StdAccept) Quote {
	q := Quote{Network: a.Network, Asset: a.Asset, AmountAtomic: a.Amount, PayTo: a.PayTo}
	// USDC is 6 decimals; render a human amount so a caller can show a price.
	if amt, ok := new(big.Rat).SetString(a.Amount); ok {
		q.AmountUSD = new(big.Rat).Quo(amt, big.NewRat(1_000_000, 1)).FloatString(4)
	}
	return q
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
