// Package pay is the buyer half of an x402 EVM payment: the wire types a
// facilitator expects, and an EIP-3009 signer that authorises a transfer without
// ever broadcasting a transaction.
//
// WHY A BUYER NEEDS NO GAS
//
//	EIP-3009 transferWithAuthorization lets a token holder authorise a transfer
//	off-chain; anyone may then submit it and pay the gas. In x402 the facilitator
//	submits, so the buyer needs a token balance and no ETH, and the seller needs
//	no key at all. That indirection is what makes both ends custody-free.
//
// The signature is EIP-712 typed data bound to one token contract on one chain,
// so an authorisation for USDC on Base cannot be replayed against a different
// token or a different network.
package pay

// USDCDecimals is USDC's atomic scale on every chain below: an Amount of
// "10000" is $0.01, not ten thousand dollars. Named rather than written as a
// literal at each use, because getting it wrong misprices by a factor of 10^6.
const USDCDecimals = 6

// Networks in CAIP-2 form, as facilitators report them.
const (
	NetworkBase        = "eip155:8453"
	NetworkBaseSepolia = "eip155:84532"
	NetworkArbitrum    = "eip155:42161"
	NetworkPolygon     = "eip155:137"
)

// Chain IDs matching the networks above. The chain id is signed into the EIP-712
// domain, so it is taken from the challenge in practice and never assumed; these
// are for display, sanity checks and tests.
const (
	ChainBase        int64 = 8453
	ChainBaseSepolia int64 = 84532
	ChainArbitrum    int64 = 42161
	ChainPolygon     int64 = 137
)

// Circle's NATIVE USDC contracts — not a bridged USDC.e.
//
// VERIFIED ON-CHAIN, not copied from a web page. Each contract was queried
// directly and reports symbol() "USDC", name() "USD Coin", version() "2",
// decimals() 6, and a live DOMAIN_SEPARATOR() — the EIP-712 domain an EIP-3009
// signature is built over. For Arbitrum and Polygon the on-chain
// DOMAIN_SEPARATOR() was additionally reproduced from
// {name, version, chainId, contract} using this package's own hashing, proving
// each uses the standard EIP-712 domain with no Polygon-style salt, so the
// signing path here produces signatures those tokens accept:
//
//	Base      0x02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f
//	Arbitrum  0x08d11903f8419e68b1b8721bcbe2e9fc68569122a77ef18c216f10b3b5112c78
//	Polygon   0xcaa2ce1a5703ccbe253a34eb3166df60a705c561b44b192061e28f2a985be2ca
//
// An asset address is the difference between being paid real USDC and a
// worthless lookalike. Re-verify the same way before changing any of them.
const (
	USDCBase        = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	USDCBaseSepolia = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	USDCArbitrum    = "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"
	USDCPolygon     = "0x3c499c542cEF5E3811e1192ce70d8cc03d5c3359"
)

// Authorization is the buyer's signed EIP-3009 transferWithAuthorization. A
// seller never constructs or signs one — it is produced by the buyer's wallet
// and relayed to the facilitator verbatim.
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	// Nonce is a 32-byte random value. It is what makes an authorisation
	// single-use at the token contract, independently of any replay store the
	// seller keeps.
	Nonce string `json:"nonce"`
}

// ExactPayload is the "exact" scheme payload: an authorisation plus its
// signature. This is what travels in the x402 payment envelope.
type ExactPayload struct {
	Signature     string        `json:"signature"`
	Authorization Authorization `json:"authorization"`
}
