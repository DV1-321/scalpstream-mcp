# Changelog

Releases are the Git tags on this repository. Each is published to the MCP
registry as `io.github.DV1-321/scalpstream-mcp` and as the
`ghcr.io/dv1-321/scalpstream-mcp` image.

## 0.1.11

### Added

- **`wash_sale_check`**: the US wash-sale rule (IRC section 1091) applied to
  the user's own trades, served by BasisScout
  (`https://basisscout.scalpstream.com`). `check=buy` asks whether buying a
  ticker on a date would disallow a loss already taken (how much, from which
  sale, and the first safe rebuy date); `check=sell` whether a sale at a loss
  is already washed by a purchase in the 30 days before; no check returns the
  ledger alone: determinations, sales with realized/disallowed/allowed split
  short and long term, open lots with adjusted basis and holding date, and
  the year's totals. Taxable, IRA and spouse accounts together, with an IRA
  replacement marked as a permanent loss. Arguments are validated locally
  before anything is paid (trades required, a check needs its symbol, a sell
  its account and quantity). Every answer is a rule calculation, not tax
  advice; the service processes the trades in memory and keeps nothing, and
  this server never logs them. `SCALPMCP_BASIS_URL` overrides the origin.
- The `initialize` instructions and the skill name the new domain.

### Fixed

- `payment_status` lists every rail the services accept again: USDC on Base,
  Arbitrum, Polygon and Solana, and XRP or RLUSD on the XRP Ledger. 0.1.10
  listed only the USDC rails. This client still pays USDC on Base.

## 0.1.10

### Withdrawn

- **`options_research`, `municipal_income`, `crypto_research` and
  `crypto_yields`.** The ScalpStream Research boards behind these tools have
  been withdrawn, so the tools are gone from `tools/list` and from the
  `initialize` instructions. Every path on their host answers **410 Gone** with
  `"error": "withdrawn"`, ahead of any payment, so a 0.1.9 or older server that
  still lists one of them gets that 410 and is charged nothing. Upgrade to
  0.1.10 or later.

### Added

- Packaging for galleries that index the repository: a Gemini CLI extension
  (`gemini-extension.json` + `GEMINI.md`), a Claude Code plugin with its own
  one-plugin marketplace (`.claude-plugin/`, so
  `/plugin marketplace add DV1-321/scalpstream-mcp` then
  `/plugin install scalpstream@scalpstream-mcp` works), and an Agent Skill
  (`skills/scalpstream-data/SKILL.md`) covering RecallScout, BorderScout and
  FuelScout: the free A2A calls, the paid GET routes, the refusal codes and
  the spending rules. All three run the same `docker run -i --rm` command as
  the README, carry no secrets, and are tied to `server.json`'s version by
  `TestGeminiExtensionMatchesServerJSON` and `TestClaudePluginMatchesServerJSON`.

### Changed

- `initialize` instructions name the four remaining domains, cheapest fuel,
  air quality, US border-crossing wait times and US product recalls (the
  recalls tool shipped in 0.1.8 but was never mentioned), and nothing else.
- `air_quality` is unchanged. AirScout now answers from a self-hosted
  Open-Meteo, and its answers carry the Open-Meteo and Copernicus credits.
- `payment_status` lists the rails the services accept: USDC on Base,
  Arbitrum, Polygon and Solana.
- Calling a withdrawn tool by name (from a client holding an older tool list)
  returns a tool error saying it has been withdrawn, before any request is
  made, instead of "unknown tool".
- `SCALPMCP_FEED_URL` is gone with the research boards.
- `server/discover` (the MCP 2026-07-28 opening call) is answered explicitly
  with JSON-RPC `-32601`, the documented signal for a newer client to fall back
  to `initialize`. The server still speaks 2025-06-18 only.
- New `SCALPMCP_RECALL_URL` override, matching the other service origins.
- README: Docker install first, with Claude Desktop and Claude Code
  configurations; the Apify Actors as a way to reach the same data without a
  wallet; preview-mode examples captured from the image.
- `server.json` title is "ScalpStream Data".

## 0.1.9

- Registry-length description.
- Every outbound request carries `User-Agent: scalpmcp/<version> (+repo URL)`.
- One stderr line per tool call: tool, paid, complete, HTTP status, elapsed
  milliseconds. Never argument values, the key or the payment payload.

## 0.1.8

- `product_recalls`: active US recalls across FDA, NHTSA and CPSC.
- Honest `paid` and `complete` booleans; unconfirmed payments are held against
  the budget.
- `glama.json` so the Glama listing can be claimed.

## 0.1.7 and earlier

See the Git tags.
