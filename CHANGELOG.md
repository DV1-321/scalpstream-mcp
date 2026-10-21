# Changelog

Releases are the Git tags on this repository. Each is published to the MCP
registry as `io.github.DV1-321/scalpstream-mcp` and as the
`ghcr.io/dv1-321/scalpstream-mcp` image.

## Unreleased (0.1.10)

### Withdrawn

- **`crypto_yields`** (data licence). The upstream source of the staking,
  lending and liquidity-pool yields does not permit resale, so the tool is
  gone from `tools/list` and the feed's `/yields` and `/preview-yields`
  endpoints are being taken down. A 0.1.9 or older server that still lists the
  tool will get a **404** from the service when it is called; nothing is
  charged for it, because payment settles only when the resource is served.
  Upgrade to 0.1.10 or later.

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

- `initialize` instructions name every remaining domain, including US product
  recalls (the tool shipped in 0.1.8 but was never mentioned), and no longer
  name staking yields.
- `crypto_research` describes "technical readings" rather than "technical
  signals".
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
