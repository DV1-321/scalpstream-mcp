# Listing steps for scalpstream-mcp (for David to do by hand)

Nothing here has been opened, submitted or claimed. Each step needs your own
GitHub, LobeHub or email identity, so it is yours to take. Checked 2026-10-09.

**Do this first:** merge branch `mcp1009/listing-cleanup` into `main` and push it.
Every site below reads the GitHub repo. Until it is pushed they still see the
8-tool README and no `glama.json`. Merging does not need a release tag.

This repo is public, so this file would be public on `main` too. Nothing in it
is secret, but if you would rather not publish it, run `git rm
PR_awesome-mcp-servers.md` in the merge and keep a local copy.

## 1. punkpeye/awesome-mcp-servers

- Repo: https://github.com/punkpeye/awesome-mcp-servers
- Section: `### 💰 Finance & Fintech`. On 2026-10-09 the header was at line 2050
  of `README.md`, with 434 entries and none of ours.
- Placement: CONTRIBUTING.md asks for alphabetical order inside each section.
  Put the line after `- [dqj1998/japan-company-info-mcp-bridge](...)` and before
  `- [edge-claw/mood-booster-agent](...)`. That was lines 2188-2189 on 10-09.
  Many recent entries were added at the top of the section, but the written rule
  is alphabetical.
- Do **not** add `🤖🤖🤖` to the title. CONTRIBUTING.md offers that fast track
  only "If you are an automated agent", and you are opening this PR yourself.

### The line (copy exactly, one line)

```markdown
- [DV1-321/scalpstream-mcp](https://github.com/DV1-321/scalpstream-mcp) [![DV1-321/scalpstream-mcp MCP server](https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp/badges/score.svg)](https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp) 🏎️ ☁️ 🍎 🪟 🐧 - Client that buys pay-per-call data feeds over x402, not a trading tool: options, municipal-income and crypto research, crypto yields, cheapest fuel, air quality, US border waits and product recalls. Free previews with no key; with your own Base key it pays USDC per call under a budget cap.
```

The Glama badge URL returned 200 on 2026-10-09.

### Icon legend (copied from that list's README, lines 51-71)

```text
* 🎖️ – official implementation
* programming language
  * 🐍 – Python codebase
  * 📇 – TypeScript (or JavaScript) codebase
  * 🏎️ – Go codebase
  * 🦀 – Rust codebase
  * #️⃣ - C# Codebase
  * ☕ - Java codebase
  * 🌊 – C/C++ codebase
  * 💎 - Ruby codebase

* scope
  * ☁️ - Cloud Service
  * 🏠 - Local Service
  * 📟 - Embedded Systems
* operating system
  * 🍎 – For macOS
  * 🪟 – For Windows
  * 🐧 - For Linux
```

Why these icons:

- 🏎️ Go: the whole server is Go.
- ☁️ cloud: the list's own note says to use cloud "when MCP server is talking to
  remote APIs". Every paid tool calls a remote feed.
- 🍎 🪟 🐧: `go install` builds a static binary on all three.
- 🎖️ is left off. You could argue for it, since you also run the feeds, but
  reviewers read it as a vendor's official server. That is your call.

### PR title

```text
Add DV1-321/scalpstream-mcp to Finance & Fintech
```

### PR body

```markdown
Adds [scalpstream-mcp](https://github.com/DV1-321/scalpstream-mcp) to Finance & Fintech, in alphabetical order.

It is an installable Go MCP server (stdio, MIT) that buys small datasets per call over x402: options, municipal-income and crypto research, crypto yields, cheapest fuel, air quality, US border waits and product recalls. It is a data-buying client, not a trading tool: it places no orders and connects to no brokerage.

- With no key it runs in preview-only mode: every paid tool returns the free preview plus the exact quoted price.
- With the user's own Base key it pays USDC per call. A per-call cap and a per-process budget are both checked before anything is signed.
- In the official MCP Registry as `io.github.DV1-321/scalpstream-mcp`, and listed on Glama.
- Install: `go install github.com/DV1-321/scalpstream-mcp/cmd/scalpmcp@latest`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
```

The last line marks that Claude drafted the text. Keep it or delete it before
you post.

### How to open it (your own shell, your own GitHub account)

1. Fork punkpeye/awesome-mcp-servers on GitHub.
2. Clone your fork, then `git checkout -b add-scalpstream-mcp`.
3. Insert the line above into `README.md` at the placement given. Only
   `README.md`, not the translated READMEs.
4. `git commit -am "Add DV1-321/scalpstream-mcp"`, push to your fork, and open
   the PR with the title and body above.

The GitHub web editor also works: Edit on README.md forks automatically, then
"Propose changes". The file is over 1.3 MB (4,602 lines), though, so the editor
is slow.

Expect a wait. There were 2,713 open PRs on 10-09, and 64 were merged from 09-24
to 10-08.

## 2. Glama (claim, then re-scan)

- `glama.json` is in this branch with `{"$schema":"https://glama.ai/mcp/schemas/server.json","maintainers":["DV1-321"]}`.
  The schema URL resolves, and its `$id` matches. Glama's blog post
  glama.ai/blog/2025-07-08-what-is-glamajson describes the same format.
- After the push, open https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp,
  sign in with GitHub as DV1-321 and claim it. Then ask for a re-scan or
  re-inspection.
- Glama counts tools by running the server. Its last run was 2026-08-07, which
  saw 8 tools, so it will keep showing 8 until it re-inspects. A README edit
  alone does not change that count.
- Never use "Deploy Server" or "Go Remote" with a key set. The hosted copy would
  hold your private key. Preview-only (no key) is fine.

## 3. LobeHub

- Page: https://lobehub.com/mcp/dv1-321-scalpstream-mcp. On 10-09 it said
  "Unvalidated", v1.0.0, 2 installs, Stocks & Finance. It copies README text.
- After the push, press **Refresh Metadata** so it picks up the new README.
- Then claim it with a LobeHub account. The page also mentions a
  `npx @lobehub/market-cli register` command. You don't need it for a claim,
  and nobody has checked what it uploads.

## 4. mcpservers.org (this is also the wong2/awesome-mcp-servers list)

wong2's README says "We do not accept PRs" and sends submissions to
mcpservers.org/submit. On 10-09 its sitemaps (66,324 URLs) had no scalpstream
entry.

Form at https://mcpservers.org/submit:

| Field | Value |
|---|---|
| Name | ScalpStream |
| Category | Finance |
| Repository / URL | https://github.com/DV1-321/scalpstream-mcp |
| Official MCP Registry Name (optional) | io.github.DV1-321/scalpstream-mcp |
| "This server supports remote connections" | leave **unticked** (it is stdio) |
| Contact Email | yours |
| Plan | **Free** ($0, review within 2 weeks). Skip the $39 Premium. |

If it asks for a description, use: "Client that buys pay-per-call data feeds
over x402, not a trading tool: research, crypto yields, fuel prices, air quality,
US border waits and product recalls. Free previews with no key; with your own
Base key it pays USDC per call under a budget cap."

## 5. MCP Market

- At https://mcpmarket.com/submit, paste the repo URL
  https://github.com/DV1-321/scalpstream-mcp.
- Take the free queue (average 4-6 weeks). Skip the paid fast track ($29 on
  10-09, listed within 24 hours), which buys SEO, not buyers.

## 6. Official MCP Registry (no action now)

- The registry still shows v0.1.6 with the old description. This branch changes
  `server.json`'s description to name recalls and say the server buys feeds.
  That reaches the registry only with the next release: a version bump in
  `server.json` and `buildVersion`, then a `v*` tag push, which runs
  publish.yml.
- The version was not bumped here and no tag was pushed. A release is your
  decision.
