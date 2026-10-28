# ScalpStream data tools

This extension runs the ScalpStream MCP server in Docker. It buys small, factual
datasets per call over the x402 payment protocol: US product recalls, US border
crossing wait times, cheapest fuel prices, air quality and a wash-sale check on
the user's own trades. There is no account and no API key. The install above has no wallet configured, so it runs **preview-only**.

## What every tool returns

One JSON envelope, for every tool:

| Field | Meaning |
|---|---|
| `paid` | Whether money moved for this result. `false` in preview-only mode, always. |
| `complete` | Whether `data` is the full dataset the tool describes. `false` means `data` is the service's free preview. |
| `source` | The exact URL the data came from (`local` for `payment_status`). |
| `data` | The dataset or the preview. |
| `reason`, `price`, `howto` | Present when `paid` is false: why no payment happened, the seller's exact quote (`amount_usd`, `network`, `asset`, `pay_to`), and how paid calls are enabled. |

`complete` is the field to branch on. A result with `paid: false, complete: false`
and a `price` is the designed outcome in preview-only mode, not an error: it
shows what the data looks like and what one call would cost (about $0.01 in
USDC on Base).

The tools:

- `product_recalls` - active US recalls for a product, brand, ingredient or
  vehicle (make, model and year together) across FDA, NHTSA and CPSC, with the
  severity normalised onto one scale, the hazard, the remedy and the lot
  numbers or UPCs that say whether a specific unit is affected. `complete` is
  false when an agency could not be reached; that is never an all-clear.
- `border_crossings` - US land ports of entry ranked by all-in time (drive
  plus the current CBP wait) from a latitude and longitude, per vehicle type
  and lane eligibility. The nearest crossing is often not the fastest.
- `cheapest_fuel` - cheapest stations near a point in Spain, France or Italy
  for a fuel grade, or the official EIA weekly average for a US state.
- `air_quality` - current US AQI and pollutants for any coordinates, an
  EPA-based outdoor exercise verdict and the cleanest window ahead.
- `wash_sale_check` - the US wash-sale rule (IRC 1091) applied to the user's
  own trades across taxable, IRA and spouse accounts: would buying or selling
  a ticker on a date wash a loss, by how much, and the first safe rebuy date,
  or the whole ledger with adjusted basis and the year's totals. A rule
  calculation, not tax advice; the service keeps nothing. Pass the trades only
  when the user gave them for this, and relay the statement verbatim.
- `payment_status` - whether paid calls are enabled and what this process has
  spent. Local, free, safe to call first.

Any other tool the server lists uses the same envelope. Every response carries
the service's own source attribution and disclaimer; relay them.

## Rules

- **Preview-only is the default.** Do not treat the absence of a key as a
  problem to fix. Call the tools, read `complete`, and present the preview
  with its price when `complete` is false.
- **Never ask for, accept or echo a private key in chat.** A spending key in a
  conversation transcript is a spent key. If the user wants paid calls, tell
  them to put `EVM_BASE_PRIVATE_KEY` in the extension's MCP server `env` and
  forward it to Docker by name (`-e EVM_BASE_PRIVATE_KEY`), then restart; you
  never need to see the value.
- **Throwaway keys only.** A paid install uses a dedicated Base-mainnet wallet
  holding a dollar or two of USDC, never a wallet with balances the user cares
  about. Gas is sponsored, so it needs no ETH.
- **Spend only inside the limits the user set.** `SCALPMCP_MAX_PRICE_USD`
  (default 0.10 per call) and `SCALPMCP_BUDGET_USD` (default 2.00 per process)
  are checked before anything is signed. A refusal costs nothing. Never suggest
  raising them to get past a refusal; report the refusal and let the user
  decide.
- Paid tools are not idempotent: each call settles its own payment, so do not
  retry one as if it were free.
