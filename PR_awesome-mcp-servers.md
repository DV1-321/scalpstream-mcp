# Listing steps for scalpstream-mcp (to do by hand)

Nothing here has been opened, submitted or claimed. Each step needs your own
GitHub, LobeHub or email identity, so it is yours to take. Checked 2026-10-09.

**Do this first:** merge branch `mcp1009/listing-cleanup` into `main` without
this file, then push `main`. Every site below reads the GitHub repo. Until it is
pushed they still see the 8-tool README and no `glama.json`. Merging does not
need a release tag.

This file is a private checklist and the repo is public, so keep it off `main`.
A plain merge would publish it twice: in the tree, and in the branch commit
that added it. Squash-merge and drop it instead, in the scalpstream-mcp checkout:

```powershell
git switch main
git merge --squash mcp1009/listing-cleanup
git rm -f PR_awesome-mcp-servers.md
git commit
```

In the commit editor, delete the bullet about this file, and change "since
v0.1.6" to "since v0.1.3" (product_recalls shipped in v0.1.3). Do not push the
branch itself. This file stays on it and in the `wt_mcp_1009` worktree.

## 1. punkpeye/awesome-mcp-servers

- Repo: https://github.com/punkpeye/awesome-mcp-servers
- Section: `### 💰 Finance & Fintech`. On 2026-10-09 the header was at line 2050
  of `README.md`, with 434 entries and none of ours. A re-check later on 10-09
  found it at line 2068 with 440 entries, still none of ours. The list grows
  every day, so find the spot by the neighbour names below, not by line number.
- Placement: CONTRIBUTING.md asks for alphabetical order inside each section.
  Put the line after `- [dqj1998/japan-company-info-mcp-bridge](...)` and before
  `- [edge-claw/mood-booster-agent](...)`. Those two were still next to each
  other at the re-check (lines 2208-2209). Many recent entries were added at the
  top of the section, but the written rule is alphabetical.
- Do **not** add `🤖🤖🤖` to the title. CONTRIBUTING.md offers that fast track
  only "If you are an automated agent", and you are opening this PR yourself.

### The line (copy exactly, one line)

```markdown
- [DV1-321/scalpstream-mcp](https://github.com/DV1-321/scalpstream-mcp) [![DV1-321/scalpstream-mcp MCP server](https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp/badges/score.svg)](https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp) 🏎️ ☁️ 🍎 🪟 🐧 - Buys data per call over x402 with your Base key under a budget cap (free previews without one): market research, crypto yields, fuel, air quality, border waits, recalls.
```

The Glama badge URL returned 200 on 2026-10-09.

The description is one sentence of 169 characters, ending in a period like every
other entry. On 10-09 the section's 434 descriptions had a median of 134
characters, a 90th percentile of 155 and a maximum of 189. At the re-check (440
entries) the median was 135 and the 90th percentile 155. One new entry has a
273-character description, so 169 is still well within range. CONTRIBUTING.md asks
for "concise and informative descriptions" in the existing style. "Market
research" covers the options, municipal-income and crypto research tools.

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

It is an installable Go MCP server (stdio, MIT) that buys small datasets per call over x402: options, municipal-income and crypto research, crypto yields, cheapest fuel, air quality, US border waits and product recalls. It only buys data: it places no orders, connects to no brokerage and gives no investment advice.

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

## 2. Glama (already claimed: re-sync, then re-inspect)

- The listing is already claimed. On 2026-10-09 its badge read "claimed by its
  maintainer, tool definitions rated A, 8 tools, remote-capable, maintenance
  rated B".
- `glama.json` is in this branch with `{"$schema":"https://glama.ai/mcp/schemas/server.json","maintainers":["DV1-321"]}`.
  The schema URL resolves, and its `$id` matches. Glama's blog post
  glama.ai/blog/2025-07-08-what-is-glamajson describes the same format.
- After the push, open https://glama.ai/mcp/servers/DV1-321/scalpstream-mcp,
  sign in with GitHub as DV1-321 and re-run the Claim ownership flow so Glama
  syncs `glama.json`. The same blog post says to go through that flow again
  after adding or updating the file. The page may show no "Claim" button,
  because the listing is yours already; that is not a failure.
- Then request a re-inspection (re-scan) so Glama sees 9 tools.
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

If it asks for a description, use the awesome-list sentence: "Buys data per call
over x402 with your Base key under a budget cap (free previews without one):
market research, crypto yields, fuel, air quality, border waits, recalls."

## 5. MCP Market

- At https://mcpmarket.com/submit, paste the repo URL
  https://github.com/DV1-321/scalpstream-mcp.
- Take the free queue (average 4-6 weeks). Skip the paid fast track ($29 on
  10-09, listed within 24 hours); it only shortens the wait.

## 6. Official MCP Registry (no action now)

- The registry still shows v0.1.6 with the old description. This branch changes
  `server.json`'s description to name recalls and say the server buys feeds.
  That reaches the registry only with the next release: a version bump in
  `server.json` and `buildVersion`, then a `v*` tag push, which runs
  publish.yml.
- The version was not bumped here and no tag was pushed. A release is your
  decision.
