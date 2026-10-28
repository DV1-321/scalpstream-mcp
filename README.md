# ScalpStream MCP server

[![Listed on mcpservers.org](https://mcpservers.org/badge.svg)](https://mcpservers.org/servers/dv1-321/scalpstream-mcp)

Pay-per-call MCP server for Claude Desktop, Claude Code, Cursor and any MCP client: US product recalls, US border wait times, cheapest fuel prices, air quality and a wash-sale check on your own trades, settled in USDC over x402 on Base.

An MCP server that buys small, factual datasets at call time with your own
wallet, over [x402](https://x402.org) — roughly **$0.01 a call**, no account, no
subscription, no API key anywhere in the flow.

Most MCP servers wrap an API you already hold a key for. This one pays as it
goes. The seller holds no keys either: settlement runs through a facilitator that
sponsors gas, so the only key in the system is yours, held in memory for the
lifetime of the process and never written anywhere.

**It is useful before you fund anything.** With no key configured, every paid
tool returns the free preview *and the exact quoted price*, so you can see what
you would be buying before deciding to buy it.

## Tools

| Tool | What you get |
|---|---|
| `product_recalls` | Active US recalls for a product, brand, ingredient or vehicle across FDA, NHTSA and CPSC in one query, with severity normalised onto one scale, the hazard, the remedy, and the lot numbers that tell whether a specific unit is affected. |
| `border_crossings` | US ports of entry ranked by **all-in** time — drive plus current CBP wait — per lane type. The nearest crossing is often not the fastest. |
| `cheapest_fuel` | Cheapest fuel by location and grade. Station-level for Spain, France, Italy; official regional averages for the US. |
| `air_quality` | Current AQI and pollutants anywhere in the world, an EPA-based verdict on exercising outdoors, and the cleanest window ahead. |
| `wash_sale_check` | The US wash-sale rule (IRC 1091) applied to the user's **own** trades across taxable, IRA and spouse accounts: would buying or selling a ticker on a date wash a loss, by how much, and the first safe rebuy date; or the whole ledger with adjusted basis and the year's totals. A rule calculation, not tax advice; the service keeps nothing. |
| `payment_status` | What's configured, what's been spent. Makes no network call and costs nothing. |

Withdrawn tools are listed under
[Versions](#versions).

## Install

### Docker (no toolchain needed)

The published image is `ghcr.io/dv1-321/scalpstream-mcp`. It runs as a
non-root user on a shell-less base and speaks MCP over stdin/stdout, so it needs
`-i` and nothing else:

```bash
docker run -i --rm ghcr.io/dv1-321/scalpstream-mcp:latest
```

Claude Desktop (`claude_desktop_config.json`) or any MCP client that takes a
command, preview-only:

```json
{
  "mcpServers": {
    "scalpstream": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "ghcr.io/dv1-321/scalpstream-mcp:latest"]
    }
  }
}
```

Claude Code:

```bash
claude mcp add scalpstream -- docker run -i --rm ghcr.io/dv1-321/scalpstream-mcp:latest
```

To enable paid calls, put the key in the client's `env` block and forward it
with a bare `-e NAME` — the name only, so the value never appears in the
command line:

```json
{
  "mcpServers": {
    "scalpstream": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "EVM_BASE_PRIVATE_KEY", "-e", "SCALPMCP_BUDGET_USD",
               "ghcr.io/dv1-321/scalpstream-mcp:latest"],
      "env": {
        "EVM_BASE_PRIVATE_KEY": "0x...",
        "SCALPMCP_BUDGET_USD": "2.00"
      }
    }
  }
}
```

Each release is also tagged (`:v0.1.9` and so on) for installs that want to pin
one; the MCP registry entry pins the exact tag.

Claude Code can also take it as a plugin, which installs the same Docker
command with no secrets and bundles the `scalpstream-data` skill:

```text
/plugin marketplace add DV1-321/scalpstream-mcp
/plugin install scalpstream@scalpstream-mcp
```

Gemini CLI reads the `gemini-extension.json` at the root of this repository:

```bash
gemini extensions install https://github.com/DV1-321/scalpstream-mcp
```

Both pin the release tag and start preview-only. To pay, add the key to the
installed server's `env` as described above; never to the manifest.

### Go

```bash
go install github.com/DV1-321/scalpstream-mcp/cmd/scalpmcp@latest
```

This puts `scalpmcp` in `$(go env GOPATH)/bin`:

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

Omit `env` entirely to run in preview-only mode.

## Configure

| Variable | Default | Meaning |
|---|---|---|
| `EVM_BASE_PRIVATE_KEY` | *(unset)* | Base-mainnet key holding a little USDC. Unset = preview-only. |
| `SCALPMCP_MAX_PRICE_USD` | `0.10` | Refuse any single call quoted above this. |
| `SCALPMCP_BUDGET_USD` | `2.00` | Refuse once the process has spent this much in total. |
| `SCALPMCP_FUEL_URL`, `_AIR_URL`, `_BORDER_URL`, `_RECALL_URL`, `_BASIS_URL` | the live services | Override a service origin, for testing against a stub. |

## Paying without a crypto wallet

The same data runs as Apify Actors, billed to your Apify plan instead of a
wallet. Apify's hosted MCP server exposes any Actor as a tool, so an MCP client
that cannot hold a key can reach the same lookups through it with an Apify
account:

| Data | Apify MCP link |
|---|---|
| Fuel prices | <https://mcp.apify.com?tools=rolescout/fuelscout-cheapest-fuel-prices> |
| US border waits | <https://mcp.apify.com?tools=rolescout/borderscout-us-border-wait-times> |
| US product recalls | <https://mcp.apify.com?tools=rolescout/recallscout-us-product-recalls> |

Those links authenticate with your Apify account; this server has no part in
them and never needs your Apify token.

## Preview mode examples

Captured from the image running with no key. `payment_status` is local:

```json
{
  "paid": false,
  "complete": true,
  "source": "local",
  "data": {
    "paid_mode_enabled": false,
    "paid_calls": 0,
    "spent_atomic_usdc": "0",
    "price_per_call": "$0.01 USDC",
    "rails": ["USDC on Base (used by this client)", "USDC on Arbitrum", "USDC on Polygon", "USDC on Solana", "XRP on XRPL", "RLUSD on XRPL"],
    "custody": "The seller holds no keys. This client signs with your own Base key, held only in memory for the process lifetime.",
    "how_to_enable": "Ask the user to add EVM_BASE_PRIVATE_KEY ... to the MCP server's environment ... Never request, accept or echo the key in chat. ..."
  }
}
```

`product_recalls` with `{"product": "infant formula"}` and no key comes back
with `paid: false, complete: false`, the service's free preview under `data`,
and the quote under `price` — that is the designed outcome, not an error:

```json
{
  "paid": false,
  "complete": false,
  "source": "https://recallscout.scalpstream.com/preview",
  "reason": "no spending key configured (set EVM_BASE_PRIVATE_KEY in the MCP server's environment to enable paid calls)",
  "price": { "network": "eip155:8453", "amount_usd": "0.0100", "amount_atomic": "10000", "asset": "0x8335...2913", "pay_to": "0x..." },
  "note": "This is the FREE PREVIEW, not the paid dataset. The paid call returns the full result described by this tool.",
  "howto": "Ask the user to add EVM_BASE_PRIVATE_KEY ... Never request, accept or echo the key in chat. ...",
  "data": {
    "service": "RecallScout",
    "what": "Machine-payable US recall lookups across three regulators at once: FDA (food, drugs, medical devices), NHTSA (vehicles) and CPSC (consumer products). ...",
    "parameters": { "product": "product, brand or ingredient — matched as a phrase across FDA and CPSC", "make": "...", "model": "...", "year": "...", "include_ended": "...", "limit": "..." },
    "honesty": "If an agency cannot be reached the response says so in sources_failed and sets complete=false. A lookup that could not consult all three is never reported as an all-clear.",
    "source": "US FDA (openFDA enforcement), US NHTSA (vehicle safety recalls), US CPSC (saferproducts.gov). Public domain / US Government works.",
    "disclaimer": "..."
  }
}
```

With a key the same call returns `paid: true, complete: true` and the recall
list under `data`. Every tool uses this one envelope; `complete` is the field
to branch on.

## Spending safety

Real money moves, so the design refuses rather than overspends:

- **Both limits are checked before anything is signed.** A refusal costs nothing
  and never reaches the network.
- **An unset limit means the default, never "unlimited."** A missing config line
  must not become unbounded spending.
- **Spend is recorded only when the data is actually delivered.** If a paid
  request then fails, the error says settlement state is unknown rather than
  implying nothing moved — you may need to reconcile against the chain.
- **Gas is sponsored by the facilitator**, so the wallet needs USDC and no ETH.

Use a throwaway key funded with a dollar or two. Never one holding balances you
care about, and never hand it to a hosted service — this server is the only
thing that should hold it, and it holds it only in memory.

## Identification, rate limits and logging

Every request this server makes carries the header
`User-Agent: scalpmcp/<version> (+https://github.com/DV1-321/scalpstream-mcp)`,
on the unpaid request and on the paid retry alike. It names the program and its
version and nothing about you.

Each ScalpStream host applies an edge rate limit of **60 requests per minute per
User-Agent**, and every install of this server presents the same User-Agent, so
all installs share that allowance on each host. At one request per tool call
this is far above normal agent use; a 429 means the shared allowance is busy for
the rest of that minute, not that your key or budget is at fault.

The server writes one line to **stderr** per tool call — the tool name, whether
it paid, whether the result was complete or a preview, the last HTTP status and
the elapsed milliseconds, for example
`scalpmcp: call tool=border_crossings paid=true complete=true status=200 ms=412 error=false`.
It never logs argument values, the spending key, or the payment payload.

## Protocol

MCP `2025-06-18` over stdio. A client that opens with the `2026-07-28`
`server/discover` call gets JSON-RPC `-32601` (method not found), which is that
revision's signal to fall back to `initialize`; the server then negotiates
`2025-06-18`.

## Payment rails

Sellers accept USDC on Base, Arbitrum, Polygon and Solana, and XRP or RLUSD on the XRP Ledger. This client pays **USDC on Base**, preferring it over other EVM rails
regardless of the order the server lists them in.

## What the data is not

Every response carries its own disclaimer and source attribution; read them.
Recall results are public-safety information, not legal or medical advice.
A wash-sale check is a record-keeping calculation on the trades you sent under
the US rule, matched on the same ticker only; it is not tax advice and never
computes tax owed. The trades travel in the request and the service processes
them in memory, storing and logging nothing; this server never logs them
either.

## Versions

Releases are the [Git tags](https://github.com/DV1-321/scalpstream-mcp/tags) on
this repository; each one is published to the MCP registry as
`io.github.DV1-321/scalpstream-mcp` and as the
[`ghcr.io/dv1-321/scalpstream-mcp`](https://github.com/DV1-321/scalpstream-mcp/pkgs/container/scalpstream-mcp)
image. [CHANGELOG.md](CHANGELOG.md) has the details per release.

**Withdrawn in 0.1.10: `options_research`, `municipal_income`,
`crypto_research` and `crypto_yields`.** The ScalpStream Research boards behind
them have been withdrawn. Every path on their host answers 410 Gone with
`"error": "withdrawn"`, ahead of any payment, so a 0.1.9 or older server that
calls one of these tools gets that 410 rather than data and is charged
nothing. Upgrade to 0.1.10 or later, where none of them is listed.

`air_quality` stays. AirScout now serves its own copy of the Copernicus CAMS
forecast through a self-hosted Open-Meteo, and every answer carries the
Open-Meteo and Copernicus credits.

## License

MIT - see [LICENSE](LICENSE).
