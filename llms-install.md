# Installation guide for AI agents

This file is written for an AI coding agent (Cline, Claude Code, Cursor) setting
this server up on a user's machine. Follow it top to bottom.

## What you are installing

An MCP server that buys small factual datasets at call time over the x402
payment protocol — air quality, US border-crossing waits, fuel prices, and market
research — for about $0.01 a call. There is no account to create and no API key
to obtain.

**Install it without a wallet first.** The server is fully functional in
preview-only mode: every paid tool returns the service's free preview plus the
exact quoted price. Do not ask the user for a private key to complete the
installation. Only configure one if they explicitly ask for paid calls.

## Step 1 — Install the binary

Requires Go 1.26.6 or newer (`go version`). On an older toolchain the build
downloads 1.26.6 automatically unless `GOTOOLCHAIN=local` is set — if it is, the
build fails until Go is upgraded.

```bash
go install github.com/DV1-321/scalpstream-mcp/cmd/scalpmcp@latest
```

This puts `scalpmcp` in `$(go env GOPATH)/bin`. If that directory is not on the
user's `PATH`, use the absolute path in the config below rather than editing
their shell profile.

```bash
# Resolve the absolute path to use in the MCP config
echo "$(go env GOPATH)/bin/scalpmcp"
```

No Go toolchain? A container image is published instead:

```bash
docker run --rm -i ghcr.io/dv1-321/scalpstream-mcp:latest
```

## Step 2 — Add it to the MCP settings

Preview-only (recommended default — no key, no spending):

```json
{
  "mcpServers": {
    "scalpstream": {
      "command": "scalpmcp"
    }
  }
}
```

Use the absolute path from Step 1 if `scalpmcp` is not on `PATH`.

## Step 3 — Verify

Restart the MCP client and call the `payment_status` tool. It makes no network
request and cannot cost anything, so it is the safe way to confirm the server is
alive. Expect JSON containing:

```json
{ "paid_mode_enabled": false, "price_per_call": "$0.01 USDC" }
```

Then try `air_quality` with `{"lat": 47.6588, "lon": -117.4260}`. With no key it
returns the free preview and the quoted price — that is success, not an error.

## Optional — enabling paid calls

Only do this if the user asks. It spends real money.

```json
{
  "mcpServers": {
    "scalpstream": {
      "command": "scalpmcp",
      "env": {
        "EVM_BASE_PRIVATE_KEY": "0x...",
        "SCALPMCP_MAX_PRICE_USD": "0.10",
        "SCALPMCP_BUDGET_USD": "2.00"
      }
    }
  }
}
```

Tell the user plainly:

- The key must control a **Base mainnet** address holding a little USDC. Gas is
  sponsored by the payment facilitator, so it needs **no ETH**.
- Use a **throwaway wallet** funded with only what they intend to spend, never
  one holding balances they care about.
- `SCALPMCP_MAX_PRICE_USD` caps any single call and `SCALPMCP_BUDGET_USD` caps
  the whole process. Both are checked before anything is signed, so a refusal
  costs nothing. Leaving them unset means the defaults above, never unlimited.
- Never paste the key into a chat, a committed file, or a shell command — a
  command line is readable by other processes. Put it in the MCP client's `env`
  block only.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `command not found: scalpmcp` | `$(go env GOPATH)/bin` is not on `PATH`. Use the absolute path in the config. |
| Client reports a parse error or drops the connection | Something is writing to stdout. This server writes only JSON-RPC there and all diagnostics to stderr, so suspect a wrapper script. |
| A paid tool returns `"paid": false` with a price | Expected with no key. Not an error. |
| `EVM_BASE_PRIVATE_KEY is set but unusable` | The key is not a 32-byte hex value. It fails fast on purpose rather than silently falling back to preview mode. |

## Notes for the agent

- Tools requiring coordinates validate them locally, so a bad latitude returns an
  error rather than spending money.
- The financial tools return impersonal, general-circulation research data. They
  are **not** investment advice and carry no trade plan, sizing, or verdict.
  Present their disclaimers when relaying results.
