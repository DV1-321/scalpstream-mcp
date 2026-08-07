// Command scalpmcp is a Model Context Protocol server that gives any MCP client
// — Claude Desktop, Cursor, Claude Code — pay-per-call access to the ScalpStream
// family of data services over x402.
//
//	scalpmcp                       # preview-only: real data, no payments
//	EVM_BASE_PRIVATE_KEY=0x... scalpmcp   # paid: settles USDC on Base per call
//
// WHY THIS IS NOT AN ORDINARY API WRAPPER
//
//	Most MCP servers wrap an API you already have a key for. This one buys data
//	at call time with the user's own wallet, at roughly a cent a call, with no
//	account, subscription, or API key anywhere in the flow. The seller holds no
//	keys either — settlement runs through a facilitator that sponsors gas — so
//	the only key in the system is the buyer's, held here in memory for the
//	process lifetime and never written anywhere.
//
// SPENDING SAFETY
//
//	Real money moves, so the defaults are conservative and refuse rather than
//	overspend: a per-call cap and a per-process budget, both checked before any
//	signature is produced. Without a key the server still works — paid tools
//	return the free preview plus the exact quoted price — so a user can see
//	precisely what they would be buying before enabling payments.
//
// CONFIGURATION (all optional except the key)
//
//	EVM_BASE_PRIVATE_KEY   Base-mainnet key holding a little USDC. Absent =
//	                       preview-only. Use a throwaway key; gas is sponsored,
//	                       so it needs no ETH.
//	SCALPMCP_MAX_PRICE_USD Per-call ceiling in dollars (default 0.10).
//	SCALPMCP_BUDGET_USD    Whole-process ceiling in dollars (default 2.00).
//	SCALPMCP_FEED_URL, _FUEL_URL, _AIR_URL, _BORDER_URL
//	                       Override service origins (testing).
package main

import (
	"io"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/DV1-321/scalpstream-mcp/pay"
	"github.com/DV1-321/scalpstream-mcp/client"
)

// buildVersion is reported in serverInfo and in MCP directory listings.
const buildVersion = "1.1.0"

func main() {
	// STDOUT BELONGS TO THE PROTOCOL. The MCP stdio transport reads JSON-RPC
	// from this process's stdout, so a single stray line there — a log message,
	// a panic trace, a stray Println — corrupts the stream and the client drops
	// the session with an opaque parse error. Redirect the standard logger to
	// stderr before anything can write, and never use fmt.Print* in this program.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("scalpmcp: ")

	client := &client.Client{
		// Some feeds gate on a sanctions attestation; sending it always is
		// harmless where it is not required.
		Attestation:    "not-sanctioned-party",
		MaxPriceAtomic: usdEnv("SCALPMCP_MAX_PRICE_USD", client.DefaultMaxPrice),
		Budget:         usdEnv("SCALPMCP_BUDGET_USD", client.DefaultBudget),
	}

	if key := strings.TrimSpace(os.Getenv("EVM_BASE_PRIVATE_KEY")); key != "" {
		signer, err := pay.NewSigner(key)
		if err != nil {
			// Fail loudly: a user who set a key expects paid calls, and silently
			// degrading to preview-only would look like the service is broken.
			log.Fatalf("EVM_BASE_PRIVATE_KEY is set but unusable: %v", err)
		}
		client.Signer = signer
		log.Printf("paid mode: buyer %s, per-call cap %s, session budget %s (atomic USDC)",
			signer.Address(), client.MaxPriceAtomic, client.Budget)
	} else {
		log.Printf("preview-only: no EVM_BASE_PRIVATE_KEY set. Paid tools return the free preview and the price.")
	}

	ts := newToolset(client, endpointsFromEnv())
	srv := newServer(ts, os.Stdout)

	if err := srv.serve(os.Stdin); err != nil && err != io.EOF {
		log.Fatalf("transport: %v", err)
	}
}

func endpointsFromEnv() endpoints {
	e := defaultEndpoints()
	if v := os.Getenv("SCALPMCP_FEED_URL"); v != "" {
		e.Feed = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_FUEL_URL"); v != "" {
		e.Fuel = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_AIR_URL"); v != "" {
		e.Air = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_BORDER_URL"); v != "" {
		e.Border = strings.TrimRight(v, "/")
	}
	return e
}

// usdEnv reads a dollar amount and converts it to USDC's atomic units (6
// decimals). A malformed value falls back to the default rather than erroring:
// the alternative is refusing to start over a spending LIMIT, and a limit that
// prevents the program running protects nothing.
func usdEnv(name string, def *big.Int) *big.Int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok || rat.Sign() <= 0 {
		log.Printf("WARNING: %s=%q is not a positive number; using the default %s atomic units", name, raw, def)
		return def
	}
	atoms := rat.Mul(rat, new(big.Rat).SetInt64(1_000_000))
	if !atoms.IsInt() {
		// Sub-cent precision beyond USDC's 6 decimals: round DOWN, so a limit is
		// never quietly raised above what the user asked for.
		return new(big.Int).Quo(atoms.Num(), atoms.Denom())
	}
	return atoms.Num()
}
