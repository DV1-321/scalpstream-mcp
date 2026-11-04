package main

// engine.go — my_engine_status: the ScalpStream managed-trading customer's
// view of THEIR OWN engine, through the same MCP server that sells the data
// tools.
//
// WHAT IT IS NOT. Not a paid tool and not an x402 flow: the customer has a
// monthly subscription (scalpstream.com, "Pricing plans") and, from the
// enrolment page where their Coinbase key went into the operator's vault, a
// STATUS TOKEN. That token opens exactly one thing — GET /v1/status for that
// one customer on the enrolment host — and cannot sign, trade, withdraw or
// read anyone else. It is the only credential this tool holds, it comes from
// the environment (SCALPSTREAM_STATUS_TOKEN) like the spending key does, and
// like the spending key it is never asked for, echoed or logged. Without it
// the tool answers with how to subscribe and where the token comes from, so
// an install with no subscription still gets a useful, honest answer.
//
// WHAT COMES BACK is the engine's own report, written by the customer's
// engine every minute and served by the vault with its age: whether the
// engine is running, paused on the plan minimum, halted by a brake, in crash
// mode or unarmed, and why; equity and what was realized all-time and over
// the last day, week and month; every open position with the venue's bid
// and what selling into it would net; the last twenty closes; this pass's
// counts. The figures are the customer's own ledger, net of the fees the
// engine knew. They are a record, not a forecast, and the tool says so.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// statusTokenEnv is the environment variable the customer's status token
	// is read from, at call time, so a token set after start is seen.
	statusTokenEnv = "SCALPSTREAM_STATUS_TOKEN"
	// engineStatusPath is the vault's status endpoint on the enrolment host.
	engineStatusPath = "/v1/status"
	// engineTimeout bounds one status read: a small JSON file behind Caddy.
	engineTimeout = 20 * time.Second
	// engineMaxBody caps the read; a status is a few kilobytes.
	engineMaxBody = 1 << 20

	plansURL = "https://www.scalpstream.com/pricing-plans/plans-pricing"
)

// engineHowTo is returned into the model's context whenever the token is
// missing or refused. Like howToEnable it must say where the token goes, so
// an agent does not ask the user to paste it into the conversation.
const engineHowTo = "The user subscribes at " + plansURL + " (monthly plan, cancel any time; the engine trades " +
	"their own Coinbase account under the published model), enrols their Coinbase key at the enrolment page " +
	"named in the welcome email, and that page shows a STATUS TOKEN once (st-...). The user adds it as " +
	statusTokenEnv + " to this MCP server's environment and restarts the server. Never request, accept or " +
	"echo the token in chat. The token reads that one engine's status and can do nothing else."

// engineStatusTool is the tool. token is read at call time; httpc is the
// client the read goes through (a test points it at a stub).
func engineStatusTool(e endpoints, token func() string, httpc *http.Client, userAgent string) tool {
	return tool{
		Name:  "my_engine_status",
		Title: "My ScalpStream engine: status, positions, results",
		Description: "The status of the USER'S OWN ScalpStream managed-trading engine (a subscription that trades " +
			"the user's own Coinbase account under the published model): whether it is running, paused on the " +
			"plan minimum, halted by a loss brake, in crash mode or unarmed, and why; equity; what it has realized " +
			"all-time and over the last 24 hours, 7 days and 30 days; every open position with the venue's bid and " +
			"what selling into it would net after fees; the last 20 closes; the loss-brake reading; what this " +
			"minute's scan did. Needs the user's status token in " + statusTokenEnv + "; without one the answer " +
			"explains how to subscribe and where the token comes from. The figures are the user's own ledger, a " +
			"record and not a forecast: never present them as a promise of future results, and relay `reporting` " +
			"and `note` - a status older than five minutes means the engine is not reporting. Read-only; costs " +
			"nothing; repeatable.",
		Schema: obj(map[string]any{}),
		Output: envelopeSchema("The vault's status response: `customer`, `reporting` (the engine wrote a status in the " +
			"last five minutes), `age_seconds`, `note` (why it is not reporting, or that it has not reported yet), " +
			"`engine` (the engine's own report: `state`, `why`, `until`, `armed`, `account`, `plan`, `brake`, `open`, " +
			"`recent_closes`, `results`, `this_pass`, `at`) and `vault` (whether the key is held, how many requests " +
			"it has signed and when last)."),
		Direct: func(ctx context.Context, _ map[string]any) (map[string]any, int, error) {
			src := e.Engine + engineStatusPath
			tok := strings.TrimSpace(token())
			if tok == "" {
				return map[string]any{
					"paid": false, "complete": false, "source": src,
					"reason": "no status token is configured: " + statusTokenEnv + " is not set in this server's environment",
					"howto":  engineHowTo,
					"data":   map[string]any{"subscribe": plansURL},
				}, 0, nil
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
			if err != nil {
				return nil, 0, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Accept", "application/json")
			if userAgent != "" {
				req.Header.Set("User-Agent", userAgent)
			}
			resp, err := httpc.Do(req)
			if err != nil {
				// The error text names the URL only; the token travels in a
				// header and never appears in it.
				return nil, 0, fmt.Errorf("my_engine_status: could not reach %s: %w", src, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, engineMaxBody+1))
			if err != nil {
				return nil, resp.StatusCode, fmt.Errorf("my_engine_status: reading the status: %w", err)
			}
			if len(body) > engineMaxBody {
				return nil, resp.StatusCode, errors.New("my_engine_status: the status response is over the size limit")
			}
			switch resp.StatusCode {
			case http.StatusOK:
				if !json.Valid(body) {
					return nil, resp.StatusCode, errors.New("my_engine_status: the status response is not JSON")
				}
				return map[string]any{
					"paid": false, "complete": true, "source": src,
					"data": json.RawMessage(body),
					"note": "These figures are the user's own ledger as the engine reported them, net of the fees the engine knew. " +
						"A record, not a forecast.",
				}, resp.StatusCode, nil
			case http.StatusUnauthorized:
				return map[string]any{
					"paid": false, "complete": false, "source": src,
					"reason": "the status token was refused (401): it has been rotated by the operator, the key it belongs to was revoked, or it was mistyped",
					"howto":  engineHowTo,
					"data":   map[string]any{"subscribe": plansURL},
				}, resp.StatusCode, nil
			case http.StatusTooManyRequests:
				return nil, resp.StatusCode, errors.New("my_engine_status: the status host is rate-limiting this address (12 reads a minute); try again in a minute")
			}
			return nil, resp.StatusCode, fmt.Errorf("my_engine_status: %s answered HTTP %d: %s", src, resp.StatusCode, snippetOf(body, 200))
		},
	}
}

// snippetOf trims a body for an error message.
func snippetOf(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// statusTokenFromEnv is the production token source.
func statusTokenFromEnv() string { return os.Getenv(statusTokenEnv) }
