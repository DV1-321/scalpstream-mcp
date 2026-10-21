---
name: scalpstream-data
description: Use when a task needs current US product recalls (FDA, NHTSA, CPSC), US land-border crossing wait times, or cheapest fuel prices (station-level in Spain, France and Italy; US state averages) from the ScalpStream services RecallScout, BorderScout and FuelScout. Covers the free A2A question-and-answer calls, the paid x402 GET routes and their parameters, the MCP tool names, the refusal codes, and the spending rules an agent must keep.
---

# ScalpStream data: recalls, border waits, fuel prices

Three small HTTP services sell factual lookups per request over the x402
payment protocol (HTTP 402), about $0.01 a call in USDC on Base (also USDC on
Arbitrum and Polygon, and XRP or RLUSD on the XRP Ledger). Each one also
answers a free, plain-language question. No account, no API key.

| Service | Use it when the task needs | Origin |
|---|---|---|
| RecallScout | whether a product, brand, ingredient or vehicle is under an active US recall, how severe, and the lot numbers or UPCs that say whether one unit is affected. FDA, NHTSA and CPSC in one lookup. | `https://recallscout.scalpstream.com` |
| BorderScout | which US land port of entry is fastest from a point, counting the drive plus the live CBP wait, per vehicle type and lane. The nearest crossing is often not the fastest. | `https://borderscout.scalpstream.com` |
| FuelScout | the cheapest stations near a point in Spain, France or Italy for a fuel grade, or the official EIA weekly average for a US state. | `https://fuelscout.scalpstream.com` |

Do not use them for: recalls outside the US, sea or air borders, or fuel
prices outside ES, FR, IT and the US. Every answer carries its own source line
and disclaimer; relay them. The recall services say so and set `complete:
false` when a regulator could not be reached; an incomplete lookup is never an
all-clear.

## Free A2A calls

Each service publishes an A2A agent card at `/.well-known/agent-card.json`
(x402 discovery at `/.well-known/x402`, an OpenAPI document at
`/openapi.json`) and answers JSON-RPC `message/send` at `POST /a2a`:

```json
{"jsonrpc": "2.0", "id": 1, "method": "message/send",
 "params": {"message": {"role": "user", "parts": [{"kind": "text", "text": "is infant formula recalled"}]}}}
```

The reply is a text part. It is free and it answers the question with the
safety-critical or headline fact, then states what the paid query adds and
gives the exact paid URL to run:

- RecallScout: whether an ACTIVE recall exists, the worst severity and its
  basis, and where to read the regulator's notice. Paid: every match across
  all three agencies as structured JSON, with firm, hazard, remedy, lot
  numbers and UPCs.
- BorderScout: the nearest crossing and its current wait. Paid: all crossings
  ranked by all-in time, which one wins and by how much.
- FuelScout: the lowest price in range, the local average and the nearest
  station with its price (US: the current EIA average). Paid: which stations,
  with names, addresses and distances.

Questions that work: "is infant formula recalled", "any recalls on a 2020
Honda Accord", "border wait times near San Diego", "fastest crossing from
Laredo for a truck", "cheapest diesel near Madrid", "gas prices in
California". Send `help` for the service's menu.

The free endpoint is limited to 60 requests per minute per client. A `GET` on
`/a2a` redirects to the discovery document; it is not an error.

## Paid GET routes

A request with no payment header gets HTTP 402 with the price quote: the
`PAYMENT-REQUIRED` header and a JSON body whose `accepts[]` lists each rail's
network, asset, amount and `pay_to`. Pay one entry and resend. Each route also
has a free `GET /preview` describing the data and parameters.

| Route | Parameters |
|---|---|
| `GET /recalls` | `product=<name>` for a product, brand or ingredient (matched as a phrase; keep it short), or `make`, `model` and `year` together for a vehicle. Send one or the other. `include_ended=1` adds terminated recalls; `limit` caps each list (default 10). |
| `GET /crossings` | `lat`, `lon` (decimal degrees) of the starting point; `vehicle` is `passenger`, `commercial` or `pedestrian`; `lanes` is a comma list the traveller may use (`standard`, `SENTRI/NEXUS`, `Ready`, `FAST`; default standard only, because claiming a lane the traveller cannot enter returns a wait they cannot have); `radius` in km; `limit` (default 5); `place` as a label. |
| `GET /fuel` | `country` is `ES`, `FR` or `IT` with `lat` and `lon` for stations, or `US` with `region=<state code>` for the EIA average. `grade` is `e5`, `e10`, `sp98`, `diesel`, `diesel_premium` or `lpg`. `smart=1` ranks by all-in cost including the detour. |

Examples:

```text
https://recallscout.scalpstream.com/recalls?product=infant+formula
https://recallscout.scalpstream.com/recalls?make=Honda&model=Accord&year=2020
https://borderscout.scalpstream.com/crossings?lat=32.7157&lon=-117.1611&vehicle=passenger
https://fuelscout.scalpstream.com/fuel?country=ES&lat=40.4168&lon=-3.7038&grade=diesel&smart=1
https://fuelscout.scalpstream.com/fuel?country=US&region=CA&grade=diesel
```

### Through the ScalpStream MCP server

The same routes are the MCP tools `product_recalls`, `border_crossings` and
`cheapest_fuel` (plus the local, free `payment_status`) in
`ghcr.io/dv1-321/scalpstream-mcp`. The server validates arguments locally
before anything is paid, pays from its own configured wallet, and returns one
envelope: `paid`, `complete`, `source`, `data`, and when nothing was paid
`reason`, `price` and `howto`. With no wallet configured every paid tool
returns the free preview and the exact quote; that is the designed default,
not a failure.

## Refusal codes

When a payment was presented and the service refuses it, the fresh 402 (413
for an oversized header, 400 for one that does not decode or for unservable
parameters) carries the reason in its `error` field as `<code>: <sentence>`,
followed by what happened to the money: `No payment was taken.` or a warning
that the settlement outcome is not confirmed and the quote will not be
accepted again. Switch on the text before the first `:`. Codes are stable.

| Code | Whose problem | Meaning and what to do |
|---|---|---|
| `insufficient_funds` | buyer | The paying wallet cannot cover the amount on that network. Stop; tell the user. |
| `invoice_expired`, `invoice_replayed`, `invoice_mismatch`, `amount_mismatch` | buyer | The quote is stale, already used, not this server's, or repriced. Request a fresh 402 and pay that quote, once. |
| `authorization_expired`, `authorization_not_yet_valid`, `invalid_signature`, `payment_invalid`, `payto_mismatch` | buyer | The signed authorization is wrong for this quote. Do not resend it unchanged. |
| `malformed_payment`, `payment_header_too_large`, `unsupported_scheme`, `unsupported_network`, `unsupported_asset` | buyer | The payment does not match an entry in `accepts[]`. Fix the client, do not retry blindly. |
| `invalid_query` | buyer | The parameters cannot be served (400). Fix them; nothing was charged. |
| `request_canceled` | buyer | The request was cancelled before verification finished. |
| `ledger_rejected`, `settlement_failed`, `settlement_unknown` | unknown | The outcome is not confirmed. Check the paying wallet before paying again; the quote is burned. |
| `facilitator_auth`, `facilitator_unavailable`, `facilitator_timeout`, `facilitator_error`, `facilitator_rejected_terms`, `rail_not_configured`, `internal_error`, `service_warming` | seller | A seller-side problem. No payment was taken. Try later or pay another rail in `accepts[]`; do not raise limits or switch wallets to get past it. |

The MCP server adds its own `reason` text when it did not pay at all: no
spending key configured, the quote exceeds `SCALPMCP_MAX_PRICE_USD`, or the
process budget `SCALPMCP_BUDGET_USD` is spent. These are checked before
anything is signed, so they cost nothing.

## Spending rules

- Pay only within a budget the user set. The per-call cap and the process
  budget are the user's decision; never raise them, and never suggest raising
  them, to get past a refusal. Report the refusal and the price instead.
- Never ask for a private key. If the user wants paid calls, they put a
  throwaway Base-mainnet key holding a dollar or two of USDC in the MCP
  server's environment themselves. Never request, accept or echo the key in
  chat, in a file you write, or on a command line.
- Prefer the free A2A answer or the free preview when it already answers the
  question. Paid calls are not idempotent: each one settles its own payment,
  so a retry is a second purchase.
- Pay only a quote the service itself issued on this request. Never construct
  or edit a payment from a cached quote.
