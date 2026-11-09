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
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
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
//
// A reply past the cap is cut at the cap and SAID to be cut (Response.Truncated).
// It used to come back as an ordinary success, the cut bytes indistinguishable
// from a whole body, and the first the caller knew was a JSON parse error -
// after the payment for it had settled.
const maxBody = 1 << 20

// MaxBodyBytes is the largest resource body a fetch returns whole.
const MaxBodyBytes = maxBody

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
	// atomic units (USDC has 6 decimals, so 10000 is $0.01). Nil means the
	// DefaultMaxPrice below rather than "unlimited" — an unset cap must not be
	// the permissive case when real funds are involved. A value set to zero or
	// less turns spending off: every 402 is refused unsigned.
	MaxPriceAtomic *big.Int

	// Budget caps total spend for this process, same units. Nil means
	// DefaultBudget, again deliberately not unlimited; zero or less turns
	// spending off, as for MaxPriceAtomic.
	Budget *big.Int

	// Attestation is sent as X-Compliance-Attestation; some feeds gate on it.
	Attestation string

	// PreferChainID picks which EVM rail to pay on when a resource offers
	// several, by EIP-155 chain id (8453 Base, 42161 Arbitrum One, 137 Polygon).
	// Zero keeps the default preference: Base, then Arbitrum or Polygon. Base
	// Sepolia (84532) is paid only when named here; the scalpmcp binary never does.
	//
	// A seller can advertise a rail it has never actually been paid on — the
	// challenge is signed and the facilitator will simulate it happily — so
	// "configured" and "proven" are different states, and telling them apart
	// requires choosing the rail deliberately rather than taking the default.
	// The wallet must hold the asset ON that chain; balances do not travel.
	PreferChainID int64

	// UserAgent is sent as the User-Agent header on EVERY request — the unpaid
	// GET and the paid retry alike. Empty leaves Go's default ("Go-http-client/1.1"),
	// which is indistinguishable from crawler traffic in a seller's logs: with no
	// identifying agent, MCP installs could not be told apart from scrapers, so
	// the channel could not be judged at all. It carries the program and version
	// only, never anything about the user. The sellers' edge applies a
	// per-User-Agent rate limit, so every install sharing one agent string
	// shares that limit.
	UserAgent string

	mu    sync.Mutex
	spent big.Int // settled: the resource came back
	held  big.Int // in flight, or paid without a confirmed delivery
	calls int
	// unconfirmed counts calls whose payment was presented but whose outcome is
	// unknown — the seller answered non-200 after settlement had already run.
	unconfirmed int
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
	if c.MaxPriceAtomic != nil {
		return c.MaxPriceAtomic
	}
	return DefaultMaxPrice
}

func (c *Client) budget() *big.Int {
	if c.Budget != nil {
		return c.Budget
	}
	return DefaultBudget
}

// CanPay reports whether this client is configured to spend: it holds a key and
// neither limit turns spending off.
func (c *Client) CanPay() bool { return c.Signer != nil && !c.SpendingOff() }

// SpendingOff reports whether a limit was explicitly set to zero or less, which
// refuses every 402 whether or not a key is held.
func (c *Client) SpendingOff() bool {
	return c.maxPrice().Sign() <= 0 || c.budget().Sign() <= 0
}

// Spent returns confirmed atomic units spent and the number of paid calls that
// actually returned a resource.
func (c *Client) Spent() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spent.String(), c.calls
}

// Unconfirmed returns money that was committed but whose delivery was never
// confirmed, and how many calls it covers.
//
// It is reported separately rather than folded into Spent because the two mean
// different things to a caller reconciling a session: Spent is money that bought
// something, Unconfirmed is money that may have moved for nothing. Both count
// against the budget — a payment on the wire is a payment.
func (c *Client) Unconfirmed() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held.String(), c.unconfirmed
}

// Fetch GETs url, paying if the server demands it.
//
// A 200 on the first try is returned as-is: a free endpoint costs nothing and is
// never charged for. Only a 402 triggers the payment path. Callers that need to
// know which of the two happened use FetchPaid; this form is kept for callers
// that only want the bytes.
//
// The bytes are at most MaxBodyBytes. Neither this form nor FetchPaid can say
// that a longer reply was cut; a caller that must know (anything that parses
// the body) uses FetchResponse and reads Truncated.
func (c *Client) Fetch(ctx context.Context, url string) ([]byte, error) {
	body, _, err := c.FetchPaid(ctx, url)
	return body, err
}

// FetchPaid is Fetch, also reporting whether a payment was actually presented
// and the resource delivered against it. paid=false with a nil error means the
// server answered 200 to the unpaid request: nothing moved, and a caller must
// not tell anyone it did.
func (c *Client) FetchPaid(ctx context.Context, url string) (body []byte, paid bool, err error) {
	r, err := c.FetchResponse(ctx, url)
	return r.Body, r.Paid, err
}

// Response is the outcome of one fetch. Status is the last HTTP status the
// server answered with — 200 for a delivered resource, 402 for a payment the
// client refused to make, the failing code when a request was answered with
// anything else — or 0 when no response arrived at all. It is reported so a
// caller can log what happened without the error text, which carries the URL
// and with it the query arguments.
type Response struct {
	Body   []byte
	Paid   bool
	Status int

	// Truncated reports that the server's reply was longer than MaxBodyBytes
	// and Body is only its first MaxBodyBytes. A truncated body is not the
	// resource: do not parse it, and if Paid is set do not fetch again to get
	// the rest - the payment for this reply has already settled and a second
	// fetch is a second payment for the same oversized answer.
	Truncated bool

	// Payment identifies the payment this fetch presented. Set exactly when
	// Paid is: it is what a caller hands the user when the money moved and the
	// body turned out to be unusable, so the charge can be found and disputed
	// rather than repeated.
	Payment *Payment
}

// Payment is the reference for one presented payment: the terms this client
// signed, and the seller's settlement proof when it sent one.
type Payment struct {
	Network      string `json:"network"`
	Asset        string `json:"asset"`
	AmountAtomic string `json:"amount_atomic"`
	AmountUSD    string `json:"amount_usd,omitempty"`
	PayTo        string `json:"pay_to"`
	// Payer is this client's own address.
	Payer string `json:"payer"`
	// AuthorizationNonce is the EIP-3009 nonce this client generated for the
	// transfer. It is unique to the payment and public on-chain (the token
	// emits AuthorizationUsed(payer, nonce) when it settles), so it identifies
	// the payment even when the seller sent no transaction hash.
	AuthorizationNonce string `json:"authorization_nonce"`
	// Transaction is the settlement transaction the seller reported in its
	// PAYMENT-RESPONSE header, when it sent one that looks like a hash. It is
	// the SELLER's claim, not something this client verified.
	Transaction string `json:"transaction,omitempty"`
}

// FetchResponse is FetchPaid, also reporting the last HTTP status seen. The
// error contract is FetchPaid's: Status is meaningful on both the success and
// the failure path.
func (c *Client) FetchResponse(ctx context.Context, url string) (Response, error) {
	resp, err := c.do(ctx, url, "")
	if err != nil {
		return Response{}, err
	}
	body, status := resp.body, resp.status
	if status == http.StatusOK {
		return Response{Body: body, Status: status, Truncated: resp.truncated}, nil
	}
	if status != http.StatusPaymentRequired {
		return Response{Status: status}, fmt.Errorf("x402client: %s: HTTP %d: %s", url, status, snippet(body, 300))
	}
	refused := Response{Status: status}

	ch, err := decodeChallenge(resp.header.Get(x402.HeaderPaymentRequiredStd), body)
	if err != nil {
		return refused, fmt.Errorf("x402client: %s: %w", url, err)
	}
	opt, chainID, ok := findEVMOption(ch, c.PreferChainID)
	if !ok {
		// Nothing this client will sign for. The seller's terms are still
		// reported (the first EVM option, for display only) so a caller can
		// see the price and the asset that was refused.
		err := &ErrPaymentRequired{Resource: url, Reason: "no option this client will pay: it pays Circle USDC on Base, Arbitrum or Polygon only, and an option naming another asset, chain or token domain is refused unsigned"}
		for _, a := range ch.Accepts {
			if a.Scheme == "exact" && strings.HasPrefix(a.Network, "eip155:") {
				err.Quote = quoteOf(a)
				break
			}
		}
		return refused, err
	}
	quote := quoteOf(opt)

	// The limit first: with a limit of zero a key would still not spend, so
	// "set a key" would send the user to fund one for nothing.
	if c.SpendingOff() {
		return refused, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: "spending is turned off: the per-call cap or the session budget is set to zero"}
	}
	if c.Signer == nil {
		return refused, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: "no spending key configured (set EVM_BASE_PRIVATE_KEY in the MCP server's environment to enable paid calls)"}
	}

	amount, ok := new(big.Int).SetString(opt.Amount, 10)
	if !ok {
		return refused, fmt.Errorf("x402client: %s: unparseable amount %q", url, opt.Amount)
	}
	// Both limits are checked BEFORE signing, so a refusal costs nothing.
	if amount.Cmp(c.maxPrice()) > 0 {
		return refused, &ErrPaymentRequired{Resource: url, Quote: quote,
			Reason: fmt.Sprintf("quoted %s exceeds the per-call cap of %s atomic units", opt.Amount, c.maxPrice())}
	}
	// RESERVE the spend against the budget, rather than checking and releasing
	// the lock. Check-then-add left a window in which two concurrent calls could
	// both pass a budget neither should have passed — harmless while the MCP
	// server dispatched serially, and an overspend the moment it does not.
	if err := c.reserve(amount); err != nil {
		return refused, &ErrPaymentRequired{Resource: url, Quote: quote, Reason: err.Error()}
	}
	// From here on the reservation must be resolved on every path: settled if
	// the resource arrives, released if we never presented a payment, or left
	// held when the outcome is unknown.
	settled := false
	defer func() {
		if !settled {
			c.release(amount)
		}
	}()

	domain, pinned := pay.USDCDomain(chainID)
	if !pinned { // the testnet, only when PreferChainID names it: findEVMOption refuses the rest
		name, _ := opt.Extra["name"].(string)
		version, _ := opt.Extra["version"].(string)
		domain = pay.TokenDomain{Name: name, Version: version, ChainID: chainID, Contract: opt.Asset}
	}
	exact, err := c.Signer.AuthorizeTransfer(domain, opt.PayTo, opt.Amount, 5*time.Minute)
	if err != nil {
		return refused, fmt.Errorf("x402client: %s: authorize: %w", url, err)
	}

	envelope := map[string]any{
		"x402Version": 2,
		"accepted":    opt,
		"payload":     exact,
		"resource":    url,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return refused, fmt.Errorf("x402client: encode payment: %w", err)
	}
	// A context already dead here has not sent anything: the signed payment
	// never leaves the process, so the reservation goes back (via the defer).
	if err := ctx.Err(); err != nil {
		return refused, fmt.Errorf("x402client: %s: cancelled before the paid request was sent: %w", url, err)
	}
	paidResp, err := c.do(ctx, url, base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		if neverSent(err) {
			// The request never reached a socket: no name resolved, no
			// connection, or the TLS handshake failed before a byte of HTTP was
			// written. Nothing was presented to the seller, so the money is
			// still ours and the reservation is released (via the defer).
			// Holding it here was a self-inflicted outage: a feed host down for
			// an hour turned every retry into $0.01 of "unconfirmed" budget
			// until the whole budget was gone.
			return Response{}, err
		}
		// Otherwise the payment may have been on the wire. Whether the seller
		// settled it before the transport failed is unknowable here, so the
		// reservation stays held rather than being released back into the
		// budget.
		c.markUnconfirmed()
		settled = true
		return Response{}, err
	}
	if paidResp.status != http.StatusOK {
		// The money may or may not have moved; say so rather than implying it did
		// not, because the caller may need to reconcile.
		//
		// This is NOT rare, and it is why the reservation is not released here.
		// Settlement runs before the seller's handler does — a feed whose upstream
		// data source is down answers 502 having already been paid — so releasing
		// would let a retry loop through an outage spend real money against a
		// budget counter that never moved.
		c.markUnconfirmed()
		settled = true
		return Response{Status: paidResp.status}, fmt.Errorf("x402client: %s: paid request returned HTTP %d (settlement state unknown, counted against the budget): %s",
			url, paidResp.status, snippet(paidResp.body, 300))
	}
	// Confirmed: move the reservation into settled spend.
	c.commit(amount)
	settled = true
	return Response{
		Body: paidResp.body, Paid: true, Status: paidResp.status, Truncated: paidResp.truncated,
		Payment: &Payment{
			Network: quote.Network, Asset: quote.Asset, AmountAtomic: quote.AmountAtomic, AmountUSD: quote.AmountUSD,
			PayTo: quote.PayTo, Payer: exact.Authorization.From, AuthorizationNonce: exact.Authorization.Nonce,
			Transaction: settlementTransaction(paidResp.header),
		},
	}, nil
}

// FetchFree GETs a resource that is meant to be free, and never pays for it,
// whatever the Signer, the cap or the budget would allow. A 402 is refused with
// the quote: a free preview that asks for money is a broken or hostile preview,
// and a caller telling the user "this is free" must be able to rely on that.
func (c *Client) FetchFree(ctx context.Context, url string) (Response, error) {
	resp, err := c.do(ctx, url, "")
	if err != nil {
		return Response{}, err
	}
	switch resp.status {
	case http.StatusOK:
		return Response{Body: resp.body, Status: resp.status, Truncated: resp.truncated}, nil
	case http.StatusPaymentRequired:
		refusal := &ErrPaymentRequired{Resource: url, Reason: "it is meant to be free but asked for payment, and was not paid"}
		if ch, err := decodeChallenge(resp.header.Get(x402.HeaderPaymentRequiredStd), resp.body); err == nil {
			if opt, _, ok := findEVMOption(ch, c.PreferChainID); ok {
				refusal.Quote = quoteOf(opt)
			}
		}
		return Response{Status: resp.status}, refusal
	}
	return Response{Status: resp.status}, fmt.Errorf("x402client: %s: HTTP %d: %s", url, resp.status, snippet(resp.body, 300))
}

// settlementTransaction reads the transaction the seller reported for a settled
// payment from PAYMENT-RESPONSE (x402 v2) or X-PAYMENT-RESPONSE (v1): base64 of
// a JSON object whose "transaction" is the on-chain hash.
//
// The value is the seller's and ends up in front of a model, so it is returned
// only when it has the shape of a hash or signature - 0x-hex or base58, 32 to
// 128 characters - and is otherwise dropped. A missing or malformed header is
// not an error: the authorization nonce identifies the payment without it.
func settlementTransaction(h http.Header) string {
	for _, name := range []string{x402.HeaderPaymentResponse, x402.HeaderPaymentResponseV1} {
		v := strings.TrimSpace(h.Get(name))
		if v == "" || len(v) > 4096 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			continue
		}
		var sr struct {
			Success     bool   `json:"success"`
			Transaction string `json:"transaction"`
		}
		if json.Unmarshal(raw, &sr) != nil || !sr.Success {
			continue
		}
		if tx := strings.TrimSpace(sr.Transaction); looksLikeTxRef(tx) {
			return tx
		}
	}
	return ""
}

func looksLikeTxRef(s string) bool {
	if len(s) < 32 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// neverSent reports whether a transport error is one that is CERTAIN to have
// happened before any request bytes were written: DNS failure, a failed dial
// (refused, unreachable, dial timeout), or a TLS certificate the client itself
// rejected during the handshake. Everything else — a reset mid-request, a read
// timeout, a context cancelled during the round trip — is ambiguous, and
// ambiguity keeps the money reserved.
//
// It is deliberately a whitelist. The cost of a false "never sent" is an
// overspend against the budget; the cost of a false "maybe sent" is a stuck
// reservation. The second is recoverable with a restart, the first is not.
func neverSent(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	var unknownCA x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &unknownCA) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// reserve claims amount against the budget, counting money already committed to
// in-flight or unconfirmed calls. It is the only place the budget is enforced.
func (c *Client) reserve(amount *big.Int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	committed := new(big.Int).Add(&c.spent, &c.held)
	projected := committed.Add(committed, amount)
	if projected.Cmp(c.budget()) > 0 {
		return fmt.Errorf("session budget exhausted: %s of %s atomic units committed",
			new(big.Int).Add(&c.spent, &c.held), c.budget())
	}
	c.held.Add(&c.held, amount)
	return nil
}

// release returns a reservation to the budget. Called only when no payment was
// presented, so nothing can have moved.
func (c *Client) release(amount *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held.Sub(&c.held, amount)
}

// commit turns a reservation into confirmed spend.
func (c *Client) commit(amount *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held.Sub(&c.held, amount)
	c.spent.Add(&c.spent, amount)
	c.calls++
}

// markUnconfirmed records that a presented payment had an unknown outcome. The
// reservation stays held, so the budget keeps counting it.
func (c *Client) markUnconfirmed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unconfirmed++
}

type rawResponse struct {
	status int
	body   []byte
	header http.Header
	// truncated: the reply was longer than maxBody and body holds the first
	// maxBody bytes of it.
	truncated bool
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
	// Set here, in the one place every request is built, so the unpaid GET and
	// the paid retry can never disagree about who is asking.
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return rawResponse{}, fmt.Errorf("x402client: %s: %w", url, err)
	}
	defer resp.Body.Close()
	// One byte past the cap, so a reply of exactly maxBody is whole and anything
	// longer is known to be cut rather than guessed at.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return rawResponse{}, fmt.Errorf("x402client: %s: read: %w", url, err)
	}
	truncated := len(body) > maxBody
	if truncated {
		body = body[:maxBody]
	}
	return rawResponse{status: resp.StatusCode, body: body, header: resp.Header, truncated: truncated}, nil
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
		// ONLY CIRCLE'S USDC ON A CHAIN THIS CLIENT KNOWS (2026-11-04 review,
		// finding 1). The signature this client produces is an EIP-3009
		// bearer authorization over whatever contract the option names, and
		// the cap and budget count ATOMIC UNITS with no idea of that token's
		// decimals: an option naming another asset, or a chain whose USDC
		// address is not pinned in pay.go, is refused unsigned. Before this,
		// any party that could answer a seller origin (a compromised host, an
		// override pointed at a stranger) could obtain a valid authorization
		// for an arbitrary token on any EVM chain to an arbitrary payee.
		want, known := pay.USDCFor(chainID)
		if !known || !strings.EqualFold(strings.TrimSpace(a.Asset), want) {
			continue
		}
		// The domain is pinned too (2026-11-08 sweep, finding G-5). A seller
		// naming another one has a broken or hostile payment path: its verifier
		// would reject this signature after the reservation, and that burns
		// budget as "unconfirmed" with no money moved. Refused unsigned instead.
		if d, pinned := pay.USDCDomain(chainID); pinned && !extraAgrees(a.Extra, d) {
			continue
		}
		// The testnet is opt-in. Its domain is not pinned, so an option naming it
		// could reintroduce that same budget burn through the seller's own name
		// and version, and it could outrank a real rail offered after it. A
		// mainnet session has no use for test tokens.
		if chainID == pay.ChainBaseSepolia && prefer != chainID {
			continue
		}
		if prefer != 0 {
			if chainID == prefer {
				return a, chainID, true
			}
			continue
		}
		if chainID == pay.ChainBase {
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

// extraAgrees reports whether an option's extra is silent about the token's
// domain or names the same one. Silence is fine: the signature is over the
// pinned domain either way.
func extraAgrees(extra map[string]any, d pay.TokenDomain) bool {
	if name, named := extra["name"]; named && name != any(d.Name) {
		return false
	}
	if version, named := extra["version"]; named && version != any(d.Version) {
		return false
	}
	return true
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
