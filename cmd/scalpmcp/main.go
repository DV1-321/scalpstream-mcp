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
//	                       0 in either one turns spending off, key or no key.
//	                       A negative or unreadable value stops the server.
//	SCALPMCP_FUEL_URL, _AIR_URL, _BORDER_URL, _RECALL_URL, _BASIS_URL, _ENGINE_URL
//	                       Override service origins (testing). One per
//	                       service; a service with no override cannot be
//	                       pointed at a stub, so every tool's origin has one.
//	SCALPSTREAM_STATUS_TOKEN
//	                       A managed-trading subscriber's status token (from
//	                       their enrolment page), for my_engine_status. Not a
//	                       spending key: it reads one engine's status and can
//	                       do nothing else. Absent = the tool explains how to
//	                       subscribe.
package main

import (
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/DV1-321/scalpstream-mcp/client"
	"github.com/DV1-321/scalpstream-mcp/pay"
)

// buildVersion is reported in serverInfo and in MCP directory listings.
//
// A var, not a const, so one release can stamp every surface from a single
// source: `-ldflags "-X main.buildVersion=$VERSION"`. Three surfaces used to
// carry the version independently — server.json's `version`, its OCI image tag,
// and this literal — and they had drifted to 0.1.3, v0.1.1 and 1.1.0. A host
// that lists the registry entry, pulls the image, then reads initialize got
// three different answers, which reads as an unmaintained server.
//
// The fallback below is the version to publish when nothing was stamped; it
// must equal server.json's `version`, and TestBuildVersionMatchesServerJSON
// fails the build if it drifts. "Keep them equal" was only a comment before,
// and they did not stay equal: v0.1.4 shipped an image reporting 0.1.3, because
// the release workflow computed the version and then never passed it to the
// linker. The workflow now stamps it and refuses to publish a tag that
// disagrees with this file.
var buildVersion = "0.1.14"

// userAgent identifies this program on every outbound request. Without it the
// Go default ("Go-http-client/1.1") made MCP installs indistinguishable from
// crawler traffic in the sellers' logs — no Go client appeared among the top
// User-Agent families at all — so the MCP channel could not be measured. It
// names the program and its version and nothing else; it carries no user data.
// The "+URL" convention is the one well-behaved bots use to say where a
// server operator can read about the client.
func userAgent() string {
	return "scalpmcp/" + buildVersion + " (+https://github.com/DV1-321/scalpstream-mcp)"
}

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
		UserAgent:      userAgent(),
	}

	if key := strings.TrimSpace(os.Getenv("EVM_BASE_PRIVATE_KEY")); key != "" {
		signer, err := pay.NewSigner(key)
		if err != nil {
			// Fail loudly: a user who set a key expects paid calls, and silently
			// degrading to preview-only would look like the service is broken.
			log.Fatalf("EVM_BASE_PRIVATE_KEY is set but unusable: %v", err)
		}
		client.Signer = signer
		if client.CanPay() {
			log.Printf("paid mode: buyer %s, per-call cap %s, session budget %s (atomic USDC)",
				signer.Address(), client.MaxPriceAtomic, client.Budget)
		} else {
			log.Printf("spending off: a key is set, but the per-call cap (%s) or the session budget (%s) is zero. Paid tools return the free preview and the price.",
				client.MaxPriceAtomic, client.Budget)
		}
	} else if client.SpendingOff() {
		log.Printf("spending off: the per-call cap (%s) or the session budget (%s) is zero, so no key would spend. Paid tools return the free preview and the price.",
			client.MaxPriceAtomic, client.Budget)
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
	if v := os.Getenv("SCALPMCP_FUEL_URL"); v != "" {
		e.Fuel = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_AIR_URL"); v != "" {
		e.Air = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_BORDER_URL"); v != "" {
		e.Border = strings.TrimRight(v, "/")
	}
	// Added with the recalls tool's catalogue entry: the tool shipped in 0.1.8
	// without this override, so it was the one service that could not be
	// exercised against a local stub from a running binary.
	if v := os.Getenv("SCALPMCP_RECALL_URL"); v != "" {
		e.Recall = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_BASIS_URL"); v != "" {
		e.Basis = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SCALPMCP_ENGINE_URL"); v != "" {
		e.Engine = strings.TrimRight(v, "/")
		warnEngineOverride(e.Engine)
	}
	return e
}

// usdEnv reads a dollar limit from the environment. A value that cannot be read
// as a limit stops the server: the alternative is guessing, and the guess that
// used to be made (the default) could be more than the user meant to allow.
func usdEnv(name string, def *big.Int) *big.Int {
	out, err := parseUSD(os.Getenv(name), def)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return out
}

// parseUSD converts a dollar amount to USDC's atomic units (6 decimals). Empty
// is the default. Zero is a real limit, "spend nothing", and is returned as
// zero (2026-11-08 sweep, finding G-2): it, and a negative value, used to log
// one stderr line and become the default, so a user turning spending off with a
// key still set got a $2.00 budget.
func parseUSD(raw string, def *big.Int) (*big.Int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, fmt.Errorf("%q is not a dollar amount: use a plain number such as 0.10, 0 to turn spending off, or unset it for the default %s atomic units", raw, def)
	}
	if rat.Sign() < 0 {
		return nil, fmt.Errorf("%q is negative: use a plain number such as 0.10, 0 to turn spending off, or unset it for the default %s atomic units", raw, def)
	}
	if rat.Sign() == 0 {
		return new(big.Int), nil
	}
	atoms := rat.Mul(rat, new(big.Rat).SetInt64(1_000_000))
	out := atoms.Num()
	if !atoms.IsInt() {
		// Sub-cent precision beyond USDC's 6 decimals: round DOWN, so a limit is
		// never quietly raised above what the user asked for.
		out = new(big.Int).Quo(atoms.Num(), atoms.Denom())
	}
	if out.Sign() <= 0 {
		// A positive value under one atomic unit would round to 0 and turn
		// spending off, which is not what asking for a tiny limit says
		// (2026-11-04 review, finding 2). Refuse to start and say so.
		return nil, fmt.Errorf("%q is under one atomic unit of USDC (0.000001): set it to at least 0.000001, 0 to turn spending off, or unset it for the default %s atomic units", raw, def)
	}
	return out, nil
}
