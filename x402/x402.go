// Package x402 carries the buyer's view of the x402 wire format: the challenge a
// server returns with HTTP 402, and the headers that move a payment.
//
// This is deliberately the BUYER's half only. A seller additionally needs
// invoice minting, HMAC-signed terms, replay stores and settlement plumbing;
// none of that belongs in a client, and including it would suggest this package
// is safe to build a paid API on, which it is not.
//
// See https://x402.org for the protocol.
package x402

// Headers that carry the challenge and the payment.
const (
	// HeaderPaymentRequiredStd carries base64(JSON) of a StdChallenge on a 402.
	// The same JSON is normally also the response body; read the header first
	// and fall back to the body, since intermediaries may strip unknown headers.
	HeaderPaymentRequiredStd = "PAYMENT-REQUIRED"

	// HeaderPaymentSignature carries base64(JSON) of the payment envelope on the
	// retried request.
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"

	// HeaderPaymentReceipt carries base64(JSON) proof of settlement on the 200.
	HeaderPaymentReceipt = "X-Payment-Receipt"
)

// StdChallenge is the x402 v2 "Payment Required" envelope.
//
// The offered options live in Accepts, and a buyer picks one. Order is only a
// hint: a client should select by what it can actually settle rather than by
// position, because sellers order the array for their own reasons — some put
// their preferred rail first, some order it to satisfy a particular catalog's
// validator.
type StdChallenge struct {
	X402Version int          `json:"x402Version"`
	Error       string       `json:"error,omitempty"`
	Resource    *StdResource `json:"resource,omitempty"`
	Accepts     []StdAccept  `json:"accepts"`
	// Extensions carries per-facilitator metadata, such as the Coinbase Bazaar
	// discovery block. A buyer can ignore it.
	Extensions map[string]any `json:"extensions,omitempty"`
}

// StdResource describes what is being sold.
type StdResource struct {
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// StdAccept is one payment option: what to pay, in which asset, to whom, on
// which chain.
type StdAccept struct {
	// Scheme is the settlement scheme; "exact" is the one this package pays.
	Scheme string `json:"scheme"`
	// Network is CAIP-2, e.g. "eip155:8453" for Base or "xrpl:0" for the XRP
	// Ledger.
	Network string `json:"network"`
	// Amount is in the asset's ATOMIC units — 10000 is $0.01 of a 6-decimal
	// USDC, not ten thousand dollars. Kept as a string so no float rounding can
	// alter a price in transit.
	Amount            string         `json:"amount"`
	Asset             string         `json:"asset"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds,omitempty"`
	// Extra is scheme-specific. For EVM "exact" it carries the token's EIP-712
	// domain as name/version, which the signature must be built over — a wrong
	// domain produces a signature the token rejects, so it is read from here
	// rather than assumed.
	Extra map[string]any `json:"extra,omitempty"`
}

// TokenName returns the EIP-712 domain name the seller advertised for this
// option, and whether it was present.
func (a StdAccept) TokenName() (string, bool) {
	s, ok := a.Extra["name"].(string)
	return s, ok
}

// TokenVersion returns the EIP-712 domain version the seller advertised.
func (a StdAccept) TokenVersion() (string, bool) {
	s, ok := a.Extra["version"].(string)
	return s, ok
}
