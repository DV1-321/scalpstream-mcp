# Changelog

Releases are the Git tags on this repository. Each is published to the MCP
registry as `io.github.DV1-321/scalpstream-mcp` and as the
`ghcr.io/dv1-321/scalpstream-mcp` image.

## 0.1.14

### Security

- A free preview is never paid for. When a paid call was refused the preview was
  fetched through the paying client, so a `/preview` answering 402 under the cap
  was signed and paid and still reported `paid: false`. A preview that asks for
  payment is now refused and the result carries the price only.
- `SCALPMCP_MAX_PRICE_USD=0` or `SCALPMCP_BUDGET_USD=0` now means spend nothing.
  Both used to become the default, so a user turning spending off with a key set
  got a $2.00 budget. Paid tools return the preview and the price, even for a
  quote of zero. A negative or unreadable value now stops the server. With a
  limit of zero nothing asks the user to add a key, set or not.
- The EIP-712 domain of USDC is pinned on Base, Arbitrum and Polygon. It was taken
  from the seller's 402, and a wrong one could hold the session budget as
  unconfirmed with no money moving. An option naming another domain is refused
  unsigned. A seller that refuses a correct signature is still held against the
  budget, because that cannot be told from one that settled and then failed.
- Base Sepolia is no longer paid by default. Its domain is not pinned, so an
  option naming it could hold the budget the same way, and listing it first
  outranked a real rail. It is paid only when `Client.PreferChainID` names it,
  which the `scalpmcp` binary never does.
- `my_engine_status` sends the status token only over https or to this machine,
  and does not follow redirects. An override outside `*.scalpstream.com` is warned
  about at start.

### Fixed

- `air_quality`, `border_crossings` and `product_recalls` refuse an out-of-range
  or unreadable number before any request: `hours` 1-168, `duration` 1-12 and no
  more than `hours`, `radius` 0 or more, `limit` 1-100, `year` 1900-2100. These
  used to be sent on or dropped silently. A fractional `radius` is no longer cut
  to a whole number.

## 0.1.13

### Security

- Built with Go 1.26.10. The 0.1.12 image was built with Go 1.26.9, which carries
  GO-2026-6809 / CVE-2026-111943 (crypto/x509), reachable from every outbound TLS
  fetch this server makes. Upgrade from any earlier image.

### Fixed

- A paid reply that cannot be delivered is no longer reported as a generic
  encoding error after the money moved. A 200 whose body is not JSON, is empty or
  is over the 1 MiB cap now returns a normal result with `complete: false`, the
  reason in `undelivered`, and, when the call was paid, `paid: true` with the
  payment reference (network, amount, payee, payer, authorization nonce and the
  seller's transaction when it sent one) and a note not to call again.
- A `tools/call` cancelled while it is still queued never runs. The cancel used
  to be lost because the call was registered only once it held a slot.
- Removed the unused `initialized` flag: the server serves `tools/list` and
  `tools/call` with or without the notification, and a test now says so.

## 0.1.12

### Added

- **`my_engine_status`**: for ScalpStream managed-trading subscribers, the
  status of their OWN engine - running, paused on the plan minimum, halted,
  crash mode or unarmed and why; equity; realized all-time and over 24 h, 7 d
  and 30 d; every open position with the bid and what selling into it would
  net; the last 20 closes; the loss brake; this minute's pass - as the engine
  itself reports it every minute and the vault serves it with its age. Free,
  read-only, repeatable; no x402. It needs the subscriber's status token in
  `SCALPSTREAM_STATUS_TOKEN` (shown once on the enrolment page; it reads one
  engine's status and can do nothing else). Without it the tool answers with
  how to subscribe. `SCALPMCP_ENGINE_URL` overrides the origin.

### Fixed

- **The client signs Circle's USDC on Base, Arbitrum or Polygon and nothing
  else.** 0.1.11 and earlier signed whatever asset, chain and payee the 402
  named, so a compromised or substituted seller origin could obtain an
  EIP-3009 authorization from the user's key for an arbitrary token on any EVM
  chain, with the cap counted in that token's unknown units. An option naming
  another asset or chain is now skipped unsigned; the refusal says so.
- A `SCALPMCP_MAX_PRICE_USD` or `SCALPMCP_BUDGET_USD` under one atomic
  unit (0.000001) used to round to zero and silently become the DEFAULT, which
  raised a limit set to turn spending off. The server now refuses to start
  and says why.
- `NaN` and `Inf` are no longer accepted as coordinates, quantities or
  years: they passed the range checks and became paid requests for nothing.
- `wash_sale_check` requires `symbol` to be a field of `trades`, not a
  substring (`A` matched `TAXABLE`).

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
