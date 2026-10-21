# ScalpStream MCP server

[![Listed on mcpservers.org](https://mcpservers.org/badge.svg)](https://mcpservers.org/servers/dv1-321/scalpstream-mcp)

Pay-per-call MCP server for Claude Desktop, Claude Code, Cursor and any MCP client: air quality (AQI), US border wait times, cheapest fuel prices, US product recalls, and options, crypto, dividend and DeFi-yield research, settled in USDC over x402 on Base.

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
| `options_research` | Ranked short-term US equity options candidates with the observed metrics behind each ranking. Impersonal research data — no trade plan, sizing, or verdict. |
| `municipal_income` | Federally tax-exempt municipal bond funds and muni CEFs, with tax-equivalent yields for the 24/32/37% brackets. |
| `crypto_research` | Ranked cryptocurrency candidates: technical signals plus a Fear & Greed and news-sentiment overlay. |
| `crypto_yields` | Where to earn interest on crypto: DeFi staking APY, stablecoin lending, liquidity-pool yields across CeFi and DeFi, risk-adjusted by TVL. |
| `cheapest_fuel` | Cheapest fuel by location and grade. Station-level for Spain, France, Italy; official regional averages for the US. |
| `air_quality` | Current AQI and pollutants anywhere in the world, an EPA-based verdict on exercising outdoors, and the cleanest window ahead. |
| `border_crossings` | US ports of entry ranked by **all-in** time — drive plus current CBP wait — per lane type. The nearest crossing is often not the fastest. |
| `product_recalls` | Active US recalls for a product, brand, ingredient or vehicle across FDA, NHTSA and CPSC in one query, with severity normalised onto one scale, the hazard, the remedy, and the lot numbers that tell whether a specific unit is affected. |
| `payment_status` | What's configured, what's been spent. Makes no network call and costs nothing. |

## Install

```bash
go install github.com/DV1-321/scalpstream-mcp/cmd/scalpmcp@latest
```

## Configure

Claude Desktop (`claude_desktop_config.json`), Claude Code, or any MCP client:

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

| Variable | Default | Meaning |
|---|---|---|
| `EVM_BASE_PRIVATE_KEY` | *(unset)* | Base-mainnet key holding a little USDC. Unset = preview-only. |
| `SCALPMCP_MAX_PRICE_USD` | `0.10` | Refuse any single call quoted above this. |
| `SCALPMCP_BUDGET_USD` | `2.00` | Refuse once the process has spent this much in total. |

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
care about.

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
`scalpmcp: call tool=air_quality paid=true complete=true status=200 ms=412 error=false`.
It never logs argument values, the spending key, or the payment payload.

## Payment rails

Sellers accept USDC on Base, Arbitrum and Polygon, and XRP or RLUSD on the XRP
Ledger. This client pays **USDC on Base**, preferring it over other EVM rails
regardless of the order the server lists them in.

## What the data is not

The financial tools return impersonal, general-circulation research data,
identical for every buyer. They are **not investment advice**, not personalised,
and carry no trade plan, position sizing, or buy/sell verdict. The operator
trades securities that appear in these feeds. Every response carries its own
disclaimer and source attribution; read them.

## Versions

Releases are the Git tags on this repository (`v0.1.7` is current); each one is published to the MCP registry as `io.github.DV1-321/scalpstream-mcp` and as the `ghcr.io/dv1-321/scalpstream-mcp` image. See the [tags](https://github.com/DV1-321/scalpstream-mcp/tags).

## License

MIT - see [LICENSE](LICENSE).
