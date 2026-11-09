package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DV1-321/scalpstream-mcp/client"
)

// A tool is one callable capability. buildURL turns the model's arguments into
// the paid resource URL; preview is the free endpoint used when payment is not
// possible, so a caller without a spending key still gets something real back.
type tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	// Output is the JSON Schema of what a successful call returns. Declaring it
	// is what lets a calling agent know the result shape BEFORE it spends
	// anything — which matters more for a paid tool than a free one, because a
	// response it cannot parse is a response it paid for.
	Output   map[string]any
	Preview  string
	BuildURL func(args map[string]any) (string, error)
	// Local marks a tool that makes no network request and costs nothing.
	Local bool
	// Direct marks a tool that makes its own request and pays nothing
	// (my_engine_status, engine.go): it returns the envelope and the HTTP
	// status it saw. Idempotent, since nothing is bought.
	Direct func(ctx context.Context, args map[string]any) (map[string]any, int, error)
}

type toolset struct {
	client *client.Client
	tools  []tool
	byName map[string]tool
	// statusToken is where my_engine_status reads the customer's status
	// token; the environment in production, a stub in tests.
	statusToken func() string
	engineHTTP  *http.Client
	// notOffered maps the name of a tool this server knows but does not offer
	// to what a caller asking for it is told. Neither tools/list nor dispatch
	// serves one; see withdrawnNames.
	notOffered map[string]string
}

func newToolset(c *client.Client, base endpoints) *toolset {
	ts := &toolset{client: c, byName: map[string]tool{}, notOffered: map[string]string{},
		statusToken: statusTokenFromEnv, engineHTTP: &http.Client{Timeout: engineTimeout,
			// The status route never redirects, and Go keeps Authorization on a
			// same-host redirect, https to http included. The 3xx is reported.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ts.tools = buildTools(base)
	ts.tools = append(ts.tools, engineStatusTool(base, func() string { return ts.statusToken() }, ts.engineHTTP, c.UserAgent))
	for _, t := range ts.tools {
		ts.byName[t.Name] = t
	}
	for _, name := range withdrawnNames {
		ts.notOffered[name] = "has been withdrawn"
	}
	return ts
}

// withdrawnNames are tools earlier releases listed and this one does not: the
// ScalpStream Research boards behind them have been withdrawn, and their host
// answers 410 Gone on every path. A client still holding an old tool list that
// calls one is told so, before any request, rather than "unknown tool".
// CHANGELOG.md lists them; do not re-add one.
var withdrawnNames = []string{"options_research", "municipal_income", "crypto_research", "crypto_yields"}

// endpoints are the service origins, overridable so tests can point at a local
// stub instead of the live, money-moving feeds.
type endpoints struct {
	Fuel, Air, Border, Recall, Basis string
	// Engine is the enrolment host that serves a managed-trading customer's
	// engine status (engine.go); SCALPMCP_ENGINE_URL overrides it.
	Engine string
}

func defaultEndpoints() endpoints {
	return endpoints{
		Fuel:   "https://fuelscout.scalpstream.com",
		Air:    "https://airscout.scalpstream.com",
		Border: "https://borderscout.scalpstream.com",
		Recall: "https://recallscout.scalpstream.com",
		Basis:  "https://basisscout.scalpstream.com",
		Engine: "https://enroll.scalpstream.com",
	}
}

func (ts *toolset) list() []map[string]any {
	out := make([]map[string]any, 0, len(ts.tools))
	for _, t := range ts.tools {
		entry := map[string]any{
			"name":        t.Name,
			"title":       t.Title,
			"description": t.Description,
			"inputSchema": t.Schema,
			// Behavioural hints, so a host can decide whether a call needs
			// confirmation without parsing the description. Everything here
			// reads remote data and changes nothing: no tool has a destructive
			// or state-mutating form, and saying so is what lets an agent reach
			// for these without a prompt in front of every call.
			"annotations": map[string]any{
				"title":           t.Title,
				"readOnlyHint":    true,
				"destructiveHint": false,
				// Not idempotent for paid tools: the DATA is stable for a given
				// query, but each call settles its own payment, so repeating one
				// is not free and must not be presented as a no-op retry. A
				// direct tool buys nothing and may be repeated.
				"idempotentHint": t.Local || t.Direct != nil,
				"openWorldHint":  !t.Local,
			},
		}
		if t.Output != nil {
			entry["outputSchema"] = t.Output
		}
		out = append(out, entry)
	}
	return out
}

func (ts *toolset) instructions() string {
	mode := "PREVIEW-ONLY: no spending key is configured, so paid tools return the free preview plus the exact price. " +
		"To enable paid calls the user adds EVM_BASE_PRIVATE_KEY to this server's environment and restarts it; never ask for the key in chat."
	if ts.client.CanPay() {
		spent, calls := ts.client.Spent()
		mode = fmt.Sprintf("PAID MODE: calls settle USDC on Base at roughly $0.01 each. Spent so far: %s atomic units over %d calls.", spent, calls)
	} else if ts.client.SpendingOff() {
		mode = "SPENDING OFF: a spending limit is zero, or could not be read and counts as zero, so nothing can be charged and paid tools return the free preview plus the exact price. " + spendingOffHowTo
	}
	// The domain list is the tool catalogue in prose, and it must stay in step
	// with buildTools: a domain named here that no tool serves sends a model
	// looking for a tool that is not there (the research boards, after 0.1.10), and a tool left out of it is one a model never thinks to
	// call (recalls, before). TestInstructionsNameEveryToolDomainAndNothingElse
	// pins both directions.
	return "ScalpStream sells small, factual answers per request over the x402 payment protocol " +
		"(HTTP 402): cheapest fuel, air quality, US border-crossing wait times, US product recalls, and a " +
		"wash-sale check on the user's own stock trades (US rule, IRC 1091: a calculation, not tax advice). " +
		"my_engine_status is different: free, and only for a user who subscribes to the ScalpStream managed-trading " +
		"engine; it reads their own engine's status with the status token in " + statusTokenEnv + " and never " +
		"asks for that token in chat. " +
		"Every tool returns JSON. " + mode
}

// howToEnable is returned INTO the model's context whenever a paid call could
// not be made. It has to say where the key goes, because an eager agent reading
// "set EVM_BASE_PRIVATE_KEY" will otherwise ask the user to paste it into the
// conversation — and a spending key in a chat transcript is a spent key.
const howToEnable = "Ask the user to add EVM_BASE_PRIVATE_KEY (a throwaway Base-mainnet key holding a little USDC) " +
	"to the MCP server's environment — the env block of this server's entry in the MCP client's config — " +
	"and restart the server. Never request, accept or echo the key in chat. Gas is sponsored by the facilitator, so no ETH is needed."

// spendingOffHowTo replaces howToEnable when a limit of zero is what stops the
// payment, key or no key: a key would still not spend, so asking for one is the
// wrong advice, and it is the user's choice, so an agent should not try to talk
// them out of it.
const spendingOffHowTo = "SCALPMCP_MAX_PRICE_USD or SCALPMCP_BUDGET_USD is set to 0 in this server's environment, or to a value that could not be read " +
	"as a dollar amount (the server's log names which), and either turns spending off. " +
	"Only the user can change that, by setting a plain number such as 2.00 and restarting the server."

// howTo is what a caller is told about enabling paid calls.
func (ts *toolset) howTo() string {
	if ts.client.SpendingOff() {
		return spendingOffHowTo
	}
	return howToEnable
}

// callTimeout bounds a single tool call, including its paid fetch. It is
// generous because abandoning a PAID request loses the money — settlement runs
// before the seller's handler does — so giving up early is the expensive choice.
const callTimeout = 170 * time.Second

// call runs a tool by name. Argument errors are returned rather than panicking on
// a bad type, because arguments come from a model and are not to be trusted.
//
// ctx carries the caller's deadline and cancellation, so notifications/cancelled
// reaches the HTTP request rather than being noted and ignored.
//
// It returns the result as BOTH the text a model reads and the structured object
// that validates against the tool's outputSchema — the two are the same value,
// rendered twice, so they can never disagree.
func (ts *toolset) call(ctx context.Context, name string, rawArgs json.RawMessage) (string, map[string]any, error) {
	start := time.Now()
	rec := callRecord{tool: "<unknown>"}
	text, structured, err := ts.dispatch(ctx, name, rawArgs, &rec)
	rec.failed = err != nil
	log.Print(rec.line(time.Since(start)))
	return text, structured, err
}

// callRecord is what one tool call leaves in the stderr log: the tool, whether
// it paid, whether the result was the complete dataset or a preview, the last
// HTTP status seen, and how long it took.
//
// It is the only per-call trace this server writes, and it is deliberately
// narrow. Argument values are never logged, because they are the user's
// query — coordinates, a product, a state — and stderr is routinely captured
// by MCP hosts into files the user never reviews. Error text is never logged
// either: the client's errors name the URL, and the URL carries the arguments.
// The spending key and the signed payment never come near this path at all.
type callRecord struct {
	tool     string
	paid     bool
	complete bool
	status   int
	failed   bool
}

func (r callRecord) line(d time.Duration) string {
	return fmt.Sprintf("call tool=%s paid=%t complete=%t status=%d ms=%d error=%t",
		r.tool, r.paid, r.complete, r.status, d.Milliseconds(), r.failed)
}

// dispatch is call without the log line; rec is filled in as the outcome
// becomes known so the one log line can be written on every exit path.
func (ts *toolset) dispatch(ctx context.Context, name string, rawArgs json.RawMessage, rec *callRecord) (string, map[string]any, error) {
	t, ok := ts.byName[name]
	if !ok {
		if status, known := ts.notOffered[name]; known {
			// One of this server's own tool names, so logging it carries
			// nothing of the caller's.
			rec.tool = name
			return "", nil, fmt.Errorf("%s %s; call tools/list for the available set", name, status)
		}
		return "", nil, fmt.Errorf("unknown tool %q; call tools/list for the available set", name)
	}
	rec.tool = t.Name
	args := map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", nil, fmt.Errorf("arguments were not a JSON object: %v", err)
		}
	}
	if t.Direct != nil { // its own request, nothing bought
		out, status, err := t.Direct(ctx, args)
		rec.status = status
		if err != nil {
			return "", nil, err
		}
		rec.complete, _ = out["complete"].(bool)
		return render(out)
	}
	if t.BuildURL == nil { // local tool, no network
		rec.complete = true
		return render(ts.status())
	}
	target, err := t.BuildURL(args)
	if err != nil {
		return "", nil, err
	}

	resp, err := ts.client.FetchResponse(ctx, target)
	rec.status = resp.Status
	if err == nil {
		paid := resp.Paid
		out := map[string]any{"paid": paid, "source": target}
		delivered := attachBody(out, resp.Body, resp.Truncated)
		out["complete"] = delivered
		rec.paid, rec.complete = paid, delivered
		switch {
		case paid && !delivered:
			// The money has moved and there is nothing to show for it. This is a
			// RESULT, not an error, and it says so in every field a caller might
			// read: an error here reads as "it failed, try again", and trying
			// again is a second payment for the same unusable reply.
			out["payment"] = resp.Payment
			out["note"] = paidUndeliveredNote
		case !paid && delivered:
			// The server answered the unpaid request with 200: a mirror, a
			// cache, or a feed with its gate down. The data is whatever it
			// served, and no money moved — saying paid:true here would have
			// the model report a charge that payment_status cannot show.
			out["note"] = "The resource was served without a payment challenge, so nothing was charged."
		case !paid && !delivered:
			out["note"] = "The resource was served without a payment challenge, so nothing was charged; " +
				"its reply could not be returned as data (see undelivered)."
		}
		return render(out)
	}

	// Payment refused or impossible: return the free preview and the exact price
	// rather than only an error. The caller learns what the data looks like and
	// what it would cost, which is the whole point of the free tier.
	var pay *client.ErrPaymentRequired
	if errors.As(err, &pay) && t.Preview != "" {
		out := map[string]any{
			"paid":     false,
			"source":   t.Preview,
			"complete": false,
			"reason":   pay.Reason,
			"price":    pay.Quote,
			"howto":    ts.howTo(),
			"note":     "This is the FREE PREVIEW, not the paid dataset. The paid call returns the full result described by this tool.",
		}
		// FetchFree, not FetchResponse: this path tells the caller nothing was
		// charged, so it must not be able to pay. Through the paying client a
		// /preview answering 402 under the cap was signed, spent the user's
		// money, and was still reported paid:false.
		preview, perr := ts.client.FetchFree(ctx, t.Preview)
		if perr == nil {
			attachBody(out, preview.Body, preview.Truncated)
		} else {
			out["error"] = perr.Error()
			out["note"] = "No free preview could be fetched, so this result carries the price only. Nothing was charged."
		}
		return render(out)
	}
	return "", nil, err
}

// paidUndeliveredNote is what a caller reads when a call was paid for and the
// seller's reply could not be handed over. It is written for a model that is
// about to decide whether to call the tool again.
const paidUndeliveredNote = "PAID, NOT DELIVERED. The payment for this call settled (paid=true; `payment` identifies it) " +
	"and the seller answered 200, but its reply could not be returned as data; `undelivered` says why. " +
	"Do NOT call this tool again to get the result: every call settles its own payment, and the same request " +
	"is likely to get the same reply. Tell the user that the call was charged and returned nothing usable, " +
	"and give them the payment reference; payment_status shows the charge."

// attachBody puts a fetched 200 body into the envelope and reports whether it
// went in as `data`.
//
// A body is `data` only when it is the whole reply and valid JSON. The envelope
// used to wrap whatever bytes arrived in json.RawMessage, so a reply cut at the
// client's size cap, or an HTML error page behind a 200, failed inside render
// as "encoding the result" - an error naming neither the cause nor, when the
// call had been paid for, the fact that the money had already moved.
//
// What could not be delivered is named in `undelivered`. A reply that is whole
// and merely not JSON is still the caller's (they may have paid for it), so it
// is passed on verbatim as `data_text` when it is text.
func attachBody(out map[string]any, body []byte, truncated bool) bool {
	switch {
	case truncated:
		out["undelivered"] = fmt.Sprintf("The reply was larger than this client's %d-byte limit and was cut off, "+
			"so it is not valid JSON and is not returned.", client.MaxBodyBytes)
		return false
	case len(bytes.TrimSpace(body)) == 0:
		out["undelivered"] = "The reply was empty."
		return false
	case json.Valid(body):
		out["data"] = json.RawMessage(body)
		return true
	case utf8.Valid(body):
		out["undelivered"] = "The reply was not JSON. It is returned unparsed in data_text."
		out["data_text"] = string(body)
		return false
	default:
		out["undelivered"] = fmt.Sprintf("The reply was %d bytes of binary data, not JSON, and is not returned.", len(body))
		return false
	}
}

// render turns one value into the text block and the structured object that
// tools/call returns together. Marshalling once and re-parsing keeps the two
// byte-identical in content, so a client reading either sees the same thing.
func render(v map[string]any) (string, map[string]any, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encoding the result: %w", err)
	}
	var structured map[string]any
	if err := json.Unmarshal(raw, &structured); err != nil {
		return "", nil, fmt.Errorf("re-reading the result: %w", err)
	}
	return string(raw), structured, nil
}

// envelopeSchema is the shape EVERY tool here returns, and it is uniform on
// purpose. A tool that returned the dataset on success and a differently-shaped
// note when payment was refused could not declare an honest outputSchema at all,
// because both are successful results.
//
// `paid` and `complete` are the two fields worth branching on: complete=false
// means what came back is the free preview, not the thing the tool describes.
// `data` is left unconstrained — its shape is the seller's, documented per tool
// in the description and in each feed's own /schema endpoint — because a schema
// that guessed at it would reject valid results the moment a feed added a field.
func envelopeSchema(dataDescription string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paid":        map[string]any{"type": "boolean", "description": "Whether this result was paid for. False means the free preview."},
			"complete":    map[string]any{"type": "boolean", "description": "Whether `data` is the full dataset this tool describes. False with paid=true means the payment settled and the reply could not be delivered: read `undelivered` and `payment`, and do not call again."},
			"source":      map[string]any{"type": "string", "description": "The exact URL the data came from."},
			"data":        map[string]any{"type": "object", "description": dataDescription},
			"undelivered": map[string]any{"type": "string", "description": "Present when the service answered but its reply could not be returned as `data`: why (over the size limit, not JSON, empty)."},
			"data_text":   map[string]any{"type": "string", "description": "Present with `undelivered` when the reply was text but not JSON: the reply, unparsed."},
			"payment": map[string]any{
				"type":        "object",
				"description": "Present when paid=true and complete=false: the reference for the payment that settled.",
				"properties": map[string]any{
					"network":             map[string]any{"type": "string"},
					"asset":               map[string]any{"type": "string"},
					"amount_atomic":       map[string]any{"type": "string"},
					"amount_usd":          map[string]any{"type": "string"},
					"pay_to":              map[string]any{"type": "string"},
					"payer":               map[string]any{"type": "string"},
					"authorization_nonce": map[string]any{"type": "string", "description": "The EIP-3009 nonce of the transfer; unique to this payment and visible on-chain."},
					"transaction":         map[string]any{"type": "string", "description": "The settlement transaction, as the seller reported it."},
				},
			},
			"reason": map[string]any{"type": "string", "description": "Present when paid=false: why payment did not happen."},
			"price": map[string]any{
				"type":        "object",
				"description": "Present when paid=false: the seller's quoted terms.",
				"properties": map[string]any{
					"network":       map[string]any{"type": "string"},
					"asset":         map[string]any{"type": "string"},
					"amount_atomic": map[string]any{"type": "string"},
					"amount_usd":    map[string]any{"type": "string"},
					"pay_to":        map[string]any{"type": "string"},
				},
			},
			"howto": map[string]any{"type": "string", "description": "Present when paid=false: how to enable paid calls."},
			"note":  map[string]any{"type": "string"},
			"error": map[string]any{"type": "string", "description": "Present when even the free preview could not be fetched."},
		},
		"required": []string{"paid", "complete", "source"},
	}
}

// paymentRails are the rails the services accept, as payment_status reports
// them: USDC on four networks, and XRP or RLUSD on the XRP Ledger. This client
// pays the first; each 402
// quote lists every rail with its own asset, amount and pay-to address.
var paymentRails = []string{"USDC on Base (used by this client)", "USDC on Arbitrum", "USDC on Polygon", "USDC on Solana", "XRP on XRPL", "RLUSD on XRPL"}

// status is the local, free tool. It returns the same envelope every other tool
// does — paid=false because nothing was bought, complete=true because this IS
// the whole answer rather than a preview of one.
func (ts *toolset) status() map[string]any {
	spent, calls := ts.client.Spent()
	held, unconfirmed := ts.client.Unconfirmed()
	st := map[string]any{
		"paid_mode_enabled": ts.client.CanPay(),
		"spent_atomic_usdc": spent,
		"paid_calls":        calls,
		"price_per_call":    "$0.01 USDC",
		"rails":             paymentRails,
		"custody":           "The seller holds no keys. This client signs with your own Base key, held only in memory for the process lifetime.",
	}
	if unconfirmed > 0 {
		// Reported separately because it means something different: money that
		// was committed but never bought anything. A seller settles before its
		// handler runs, so an upstream outage can charge for a 502.
		st["unconfirmed_atomic_usdc"] = held
		st["unconfirmed_calls"] = unconfirmed
		st["unconfirmed_meaning"] = "Payment was presented but the resource never arrived, so it may or may not have settled. " +
			"Counted against the budget, because a payment on the wire is a payment."
	}
	if !ts.client.CanPay() {
		st["how_to_enable"] = ts.howTo()
	}
	return map[string]any{
		"paid":     false, // this tool costs nothing
		"complete": true,  // and it is the whole answer, not a preview
		"source":   "local",
		"data":     st,
	}
}

// ---- argument helpers -------------------------------------------------------
//
// Arguments arrive from a model, so a number may be a float64, a string, or
// missing entirely. These normalise without ever panicking.

func argStr(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func argNum(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case string:
		// ParseFloat accepts "NaN" and "Inf"; neither is a coordinate, a
		// quantity or a year, and a NaN passes every range check (2026-11-04
		// review, finding 4), so it would have cost a paid call for nothing.
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

// argSet reports whether an optional argument was given at all: a model sends
// null or a blank string for "not set".
func argSet(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	default:
		return true
	}
}

// argWhole reads an optional whole number in lo..hi, comparing as a float so
// that 1e300 is refused rather than handed to int(), whose result out of range
// is implementation-defined. A value that is given and is not one is an error,
// raised by BuildURL before any request: it used to be dropped or sent on, and
// the wallet paid for an answer to a different question.
func argWhole(args map[string]any, key string, lo, hi int) (int, bool, error) {
	if !argSet(args, key) {
		return 0, false, nil
	}
	v, ok := argNum(args, key)
	if !ok || v != math.Trunc(v) || v < float64(lo) || v > float64(hi) {
		return 0, false, fmt.Errorf("%s must be a whole number from %d to %d, got %v", key, lo, hi, args[key])
	}
	return int(v), true, nil
}

// argBool reads a boolean argument. Models send booleans as JSON true/false, as
// the strings "true"/"false"/"1"/"0"/"yes"/"no" in any case, and occasionally as
// the numbers 1/0; all of those are accepted. Anything else is an error, and
// the error is raised by BuildURL — BEFORE any request, so a bad flag costs a
// message rather than a paid call.
//
// ok=false means the argument was absent (or an empty string), not false. The
// distinction matters to a caller that wants "omitted" to mean the default.
//
// The old code was `args["smart"].(bool)`: a string "true" was silently treated
// as absent, the wallet paid, and the feed returned the dataset the model had
// NOT asked for, labelled complete.
func argBool(args map[string]any, key string) (val, ok bool, err error) {
	switch v := args[key].(type) {
	case nil:
		return false, false, nil
	case bool:
		return v, true, nil
	case float64:
		switch v {
		case 1:
			return true, true, nil
		case 0:
			return false, true, nil
		}
		return false, false, fmt.Errorf("%s must be a boolean (true/false), got %v", key, v)
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			return false, false, nil
		case "true", "1", "yes":
			return true, true, nil
		case "false", "0", "no":
			return false, true, nil
		}
		return false, false, fmt.Errorf("%s must be a boolean (true/false), got %q", key, v)
	default:
		return false, false, fmt.Errorf("%s must be a boolean (true/false), got %T", key, v)
	}
}

// requireLatLon is shared by the three geographic tools. Coordinates are
// validated here rather than at the server, so a mistyped longitude costs an
// error message instead of a paid call that returns nothing useful.
func requireLatLon(args map[string]any) (string, string, error) {
	lat, okLat := argNum(args, "lat")
	lon, okLon := argNum(args, "lon")
	if !okLat || !okLon {
		return "", "", errors.New("lat and lon are required (decimal degrees)")
	}
	if lat < -90 || lat > 90 {
		return "", "", fmt.Errorf("lat %g is out of range (-90..90)", lat)
	}
	if lon < -180 || lon > 180 {
		return "", "", fmt.Errorf("lon %g is out of range (-180..180)", lon)
	}
	return strconv.FormatFloat(lat, 'f', -1, 64), strconv.FormatFloat(lon, 'f', -1, 64), nil
}

// tradesName reports whether symbol is one of the FIELDS of trades (rows of
// comma-separated fields, or a JSON array of objects), not merely a substring:
// "A" is inside "TAXABLE" and "IRA" inside "trad_ira", and a substring test
// let the guard above pass for a ticker the trades never name (2026-11-04
// review, finding 11).
func tradesName(trades, symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return false
	}
	isSep := func(r rune) bool { return r == ',' || r == ';' || r == '"' || r == '\'' || r == ':' || r == '[' || r == ']' || r == '{' || r == '}' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }
	for _, f := range strings.FieldsFunc(trades, isSep) {
		if strings.ToUpper(f) == symbol {
			return true
		}
	}
	return false
}

func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func enumProp(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}
func obj(props map[string]any, required ...string) map[string]any {
	sort.Strings(required)
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	} else {
		m["required"] = []string{}
	}
	return m
}

// buildTools is the catalogue: every tool here is listed by tools/list and
// callable. The research tools (options_research, municipal_income,
// crypto_research, crypto_yields) have been withdrawn and are in
// withdrawnNames.
func buildTools(e endpoints) []tool {
	noArgs := obj(map[string]any{})

	return []tool{
		{
			Name:  "cheapest_fuel",
			Title: "Cheapest fuel near a location",
			Description: "Cheapest fuel for a location and grade. Station-level pricing where governments publish open " +
				"data (Spain, France, Italy); official regional averages for the US via EIA. With smart=true the " +
				"ranking weighs the pump price against the fuel and time spent on the detour, so the cheapest sign " +
				"is not always the cheapest tank.",
			Schema: obj(map[string]any{
				"country": enumProp("ISO-3166 alpha-2 country code. ES/FR/IT give station-level prices; US gives a regional average.", "ES", "FR", "IT", "US"),
				"lat":     numProp("Latitude in decimal degrees. Required for station-level countries (ES/FR/IT)."),
				"lon":     numProp("Longitude in decimal degrees. Required for station-level countries."),
				"region":  strProp("US state code, e.g. CA. Used instead of lat/lon when country=US."),
				"grade":   enumProp("Fuel grade.", "e5", "e10", "sp98", "diesel", "diesel_premium", "lpg"),
				"smart":   map[string]any{"type": "boolean", "description": "Rank by all-in cost (pump price plus the fuel and time of the detour) rather than pump price alone."},
			}, "country"),
			Preview: e.Fuel + "/preview",
			Output:  envelopeSchema("Ranked fuel prices for the requested country and grade, with station or region identity, the price, and its observation date."),
			BuildURL: func(args map[string]any) (string, error) {
				country := strings.ToUpper(argStr(args, "country"))
				if country == "" {
					return "", errors.New("country is required (ES, FR, IT for station-level prices; US for a regional average)")
				}
				q := url.Values{}
				q.Set("country", country)
				if g := argStr(args, "grade"); g != "" {
					q.Set("grade", g)
				}
				if country == "US" {
					region := argStr(args, "region")
					if region == "" {
						return "", errors.New("country=US needs a region (a US state code such as CA); the US has no free station-level feed")
					}
					q.Set("region", region)
				} else {
					lat, lon, err := requireLatLon(args)
					if err != nil {
						return "", fmt.Errorf("country=%s returns station-level prices, so %w", country, err)
					}
					q.Set("lat", lat)
					q.Set("lon", lon)
				}
				smart, _, err := argBool(args, "smart")
				if err != nil {
					return "", err
				}
				if smart {
					q.Set("smart", "1")
				}
				return e.Fuel + "/fuel?" + q.Encode(), nil
			},
		},
		{
			Name:  "air_quality",
			Title: "Air quality and the cleanest window to be outside",
			Description: "Current US AQI and pollutant breakdown for any coordinates worldwide, an EPA-based verdict on " +
				"whether it is safe to exercise outdoors, and the cleanest contiguous window in the forecast ahead " +
				"for a session of a given length. Modelled data (Copernicus CAMS), not a sensor at the exact spot.",
			Schema: obj(map[string]any{
				"lat":      numProp("Latitude in decimal degrees."),
				"lon":      numProp("Longitude in decimal degrees."),
				"hours":    numProp("How far ahead to search for a clean window, in whole hours. Default 24, maximum 168."),
				"duration": numProp("How long you want to be outside, in whole hours; the window returned is a contiguous block this long. Default 1, maximum 12, and no more than hours."),
				"place":    strProp("Optional label for the location, used in the summary text."),
			}, "lat", "lon"),
			Preview: e.Air + "/preview",
			Output:  envelopeSchema("Current AQI and pollutant readings, the EPA activity verdict, and the cleanest contiguous window in the forecast ahead."),
			BuildURL: func(args map[string]any) (string, error) {
				lat, lon, err := requireLatLon(args)
				if err != nil {
					return "", err
				}
				q := url.Values{}
				q.Set("lat", lat)
				q.Set("lon", lon)
				hours, hasHours, err := argWhole(args, "hours", 1, 168)
				if err != nil {
					return "", err
				}
				duration, hasDuration, err := argWhole(args, "duration", 1, 12)
				if err != nil {
					return "", err
				}
				span := 24 // the service's default search span
				if hasHours {
					span = hours
					q.Set("hours", strconv.Itoa(hours))
				}
				if hasDuration {
					if duration > span {
						return "", fmt.Errorf("duration (%d h) cannot exceed hours (%d h): the window must fit in the span searched", duration, span)
					}
					q.Set("duration", strconv.Itoa(duration))
				}
				if p := argStr(args, "place"); p != "" {
					q.Set("place", p)
				}
				return e.Air + "/air?" + q.Encode(), nil
			},
		},
		{
			Name:  "border_crossings",
			Title: "Fastest US border crossing by all-in time",
			Description: "Ranks US ports of entry by ALL-IN time — the drive there plus the current CBP wait once there — " +
				"for passenger, commercial or pedestrian traffic, honouring which lanes the traveller may actually use. " +
				"The nearest crossing is frequently not the fastest. Live CBP data across all 85 US ports of entry.",
			Schema: obj(map[string]any{
				"lat":     numProp("Latitude of the starting point, decimal degrees."),
				"lon":     numProp("Longitude of the starting point, decimal degrees."),
				"vehicle": enumProp("Traffic type.", "passenger", "commercial", "pedestrian"),
				"lanes":   strProp("Comma-separated lanes the traveller is eligible for, e.g. 'standard,SENTRI/NEXUS,Ready,FAST'. Defaults to standard only — trusted-traveller lanes are opt-in, since claiming one the traveller cannot use returns a wait they cannot have."),
				"radius":  numProp("Limit the search to crossings within this many km. 0 or omitted means no limit."),
				"limit":   numProp("How many crossings to return. Default 5, maximum 100."),
				"place":   strProp("Optional label for the starting point, used in the summary text."),
			}, "lat", "lon"),
			Preview: e.Border + "/preview",
			Output:  envelopeSchema("Ports of entry ranked by all-in time, each with the drive time, the current CBP wait, the lanes used and the port identity."),
			BuildURL: func(args map[string]any) (string, error) {
				lat, lon, err := requireLatLon(args)
				if err != nil {
					return "", err
				}
				q := url.Values{}
				q.Set("lat", lat)
				q.Set("lon", lon)
				if v := argStr(args, "vehicle"); v != "" {
					q.Set("vehicle", v)
				}
				if l := argStr(args, "lanes"); l != "" {
					q.Set("lanes", l)
				}
				if argSet(args, "radius") {
					r, ok := argNum(args, "radius")
					if !ok || r < 0 {
						return "", fmt.Errorf("radius must be a number of km, 0 or more, got %v", args["radius"])
					}
					if r > 0 { // 0 means no limit, which is also what omitting it means
						q.Set("radius", strconv.FormatFloat(r, 'f', -1, 64))
					}
				}
				limit, hasLimit, err := argWhole(args, "limit", 1, 100)
				if err != nil {
					return "", err
				}
				if hasLimit {
					q.Set("limit", strconv.Itoa(limit))
				}
				if p := argStr(args, "place"); p != "" {
					q.Set("place", p)
				}
				return e.Border + "/crossings?" + q.Encode(), nil
			},
		},
		{
			Name:  "product_recalls",
			Title: "Is this product or vehicle recalled",
			Description: "Active US recalls for a product, brand, ingredient or vehicle, searching FDA food/drug/" +
				"medical-device enforcement, NHTSA vehicle safety campaigns and CPSC consumer product recalls in one " +
				"query. Severity is normalised across all three regulators onto CRITICAL/SERIOUS/MODERATE/LOW — they " +
				"grade differently, and the basis for each rating is returned so it can be checked. Includes the " +
				"recalling firm, hazard, remedy, and the lot numbers and UPCs needed to tell whether a specific unit " +
				"is affected. Returns ACTIVE recalls by default: a product name can match hundreds of historical " +
				"recalls and only a handful of open ones. If an agency cannot be reached the response says so and " +
				"sets complete=false — an incomplete lookup is never an all-clear.",
			Schema: obj(map[string]any{
				"product":       strProp("Product, brand or ingredient, e.g. 'infant formula'. Matched as a phrase, so keep it short and specific."),
				"make":          strProp("Vehicle make. Requires model and year together."),
				"model":         strProp("Vehicle model."),
				"year":          numProp("Vehicle model year, 1900 to 2100."),
				"include_ended": map[string]any{"type": "boolean", "description": "Also return terminated recalls as history. Off by default; they are rarely what was meant."},
				"limit":         numProp("Max recalls per list. Default 10, maximum 100."),
			}),
			Preview: e.Recall + "/preview",
			Output:  envelopeSchema("Active (and optionally ended) recalls across FDA, NHTSA and CPSC with normalised severity, hazard, remedy, lot numbers, and a `complete` flag that is false when an agency could not be reached."),
			BuildURL: func(args map[string]any) (string, error) {
				product := argStr(args, "product")
				mk, md := argStr(args, "make"), argStr(args, "model")
				yr, hasYear, err := argWhole(args, "year", 1900, 2100)
				if err != nil {
					return "", err
				}
				anyVehicle := mk != "" || md != "" || hasYear

				if product == "" && !anyVehicle {
					return "", errors.New("give either product=<name>, or make/model/year for a vehicle")
				}
				// Validated here so a half-specified vehicle costs an error rather
				// than a paid call that returns nothing for a car that IS recalled.
				if anyVehicle && (mk == "" || md == "" || !hasYear) {
					return "", fmt.Errorf("a vehicle lookup needs make, model AND year together (got make=%q model=%q year=%v)", mk, md, args["year"])
				}
				q := url.Values{}
				if product != "" {
					q.Set("product", product)
				}
				if anyVehicle {
					q.Set("make", mk)
					q.Set("model", md)
					q.Set("year", strconv.Itoa(yr))
				}
				ended, _, err := argBool(args, "include_ended")
				if err != nil {
					return "", err
				}
				if ended {
					q.Set("include_ended", "1")
				}
				limit, hasLimit, err := argWhole(args, "limit", 1, 100)
				if err != nil {
					return "", err
				}
				if hasLimit {
					q.Set("limit", strconv.Itoa(limit))
				}
				return e.Recall + "/recalls?" + q.Encode(), nil
			},
		},
		{
			Name:  "wash_sale_check",
			Title: "Would this trade wash a loss (US wash-sale rule)",
			Description: "Applies the US wash-sale rule (IRC section 1091) to the user's OWN trades, across taxable, IRA " +
				"and spouse accounts together, and answers a pre-trade question: check=buy asks whether buying `symbol` " +
				"on date `on` would disallow a loss already taken, and returns the disallowed amount, which sale it comes " +
				"from and the first safe rebuy date; check=sell asks whether selling `quantity` shares of `symbol` from " +
				"`account` at a loss on `on` is already washed by a purchase in the 30 days before. Leave check out for " +
				"the ledger alone: every determination, every sale with realized/disallowed/allowed and short/long term, " +
				"every open lot's adjusted basis and holding date, and the year's totals. A purchase in an IRA loses the " +
				"loss permanently and the answer says so. The trades are sent in the request and processed in memory " +
				"only; the service stores and logs nothing. Everything returned is a rule calculation on the trades " +
				"given, in sentences that state the rule and never recommend a trade; it is not tax advice, never " +
				"computes tax owed, matches the same ticker only (no \"substantially identical\" judgment) and cannot " +
				"see trades it was not sent. Relay the statement and the disclaimer verbatim.",
			Schema: obj(map[string]any{
				"trades": strProp("The user's trades, one per row: account,account_type,date,symbol,kind,quantity,price[,fees], " +
					"rows separated by ';' (account_type: taxable, trad_ira, roth_ira, spouse_taxable, spouse_ira; kind: buy, " +
					"sell, reinvest, stock_plan_acquire; dates YYYY-MM-DD). A JSON array of objects with those field names " +
					"also works. At most 500 rows; send every account's trades in the ticker for the 61 days around the date " +
					"asked about, and the whole year for yearly totals."),
				"check":        enumProp("The pre-trade question. Omit for the ledger alone.", "buy", "sell"),
				"symbol":       strProp("The ticker the question is about, as it appears in trades. Required with check."),
				"on":           strProp("The date of the proposed trade, YYYY-MM-DD. Default: today."),
				"account_type": enumProp("check=buy: where the purchase would be. A purchase in an IRA disallows the loss permanently. Default taxable.", "taxable", "trad_ira", "roth_ira", "spouse_taxable", "spouse_ira"),
				"account":      strProp("check=sell: the account the shares would be sold from, as named in trades. Required with check=sell."),
				"quantity":     numProp("check=sell: how many shares would be sold. Required with check=sell."),
				"year":         numProp("The tax year to total. Default: the year of `on` with a check, otherwise the year of the latest trade."),
			}, "trades"),
			Preview: e.Basis + "/preview",
			Output: envelopeSchema("The wash-sale calculation on the trades sent: `check` (the answer, with `statement`, `safe`, " +
				"`safe_date`, `max_disallowed`, `permanent`, and the open `windows` or `replacements`), `determinations`, " +
				"`sales`, `open_lots`, `year_summary`, `errors`, plus `scope`, `privacy`, `source` and the disclaimer."),
			BuildURL: func(args map[string]any) (string, error) {
				trades := argStr(args, "trades")
				if raw, isList := args["trades"].([]any); isList {
					b, err := json.Marshal(raw)
					if err != nil {
						return "", fmt.Errorf("trades: %w", err)
					}
					trades = string(b)
				}
				if trades == "" {
					return "", errors.New("trades is required: the user's trades as rows account,account_type,date,symbol,kind,quantity,price[,fees] separated by ';', or a JSON array")
				}
				if len(trades) > 48<<10 {
					return "", fmt.Errorf("trades is %d bytes; the service takes at most %d (send one ticker's trades for the period that matters)", len(trades), 48<<10)
				}
				q := url.Values{}
				q.Set("trades", trades)
				check := strings.ToLower(argStr(args, "check"))
				symbol := strings.ToUpper(argStr(args, "symbol"))
				switch check {
				case "":
					if symbol != "" {
						return "", errors.New("symbol only applies with check=buy or check=sell; omit it for the ledger alone")
					}
				case "buy", "sell":
					if symbol == "" {
						return "", fmt.Errorf("check=%s needs symbol, the ticker the question is about", check)
					}
					if !tradesName(trades, symbol) {
						return "", fmt.Errorf("symbol %s does not appear in trades; the check would answer about nothing", symbol)
					}
					q.Set("check", check)
					q.Set("symbol", symbol)
				default:
					return "", fmt.Errorf("check must be buy or sell (or omitted for the ledger alone), got %q", check)
				}
				if on := argStr(args, "on"); on != "" {
					if _, err := time.Parse("2006-01-02", on); err != nil {
						return "", fmt.Errorf("on must be a date, YYYY-MM-DD, got %q", on)
					}
					q.Set("on", on)
				}
				if at := strings.ToLower(argStr(args, "account_type")); at != "" {
					if check != "buy" {
						return "", errors.New("account_type applies to check=buy only")
					}
					q.Set("account_type", at)
				}
				account, qty := argStr(args, "account"), argStr(args, "quantity")
				if check == "sell" {
					if account == "" || qty == "" {
						return "", errors.New("check=sell needs account (the selling account, as named in trades) and quantity (shares)")
					}
					if n, ok := argNum(args, "quantity"); !ok || n <= 0 {
						return "", fmt.Errorf("quantity must be a positive number of shares, got %q", qty)
					}
					q.Set("account", account)
					q.Set("quantity", qty)
				} else if account != "" || qty != "" {
					return "", errors.New("account and quantity apply to check=sell only")
				}
				if argSet(args, "year") {
					// Unreadable used to skip this and drop the parameter, and the
					// wallet paid for the totals of a different tax year.
					y, ok := argNum(args, "year")
					if !ok || y < 1990 || y > 2100 || y != math.Trunc(y) {
						return "", fmt.Errorf("year must be a four-digit tax year, got %v", args["year"])
					}
					q.Set("year", strconv.Itoa(int(y)))
				}
				return e.Basis + "/basis?" + q.Encode(), nil
			},
		},
		{
			Name:  "payment_status",
			Title: "Payment configuration and spend so far",
			Description: "Reports whether paid calls are enabled, how much this session has spent, the price per call, " +
				"and which payment rails the services accept. Makes no network request and costs nothing.",
			Schema: noArgs,
			Output: envelopeSchema("Whether paid mode is enabled, confirmed and unconfirmed spend, the number of paid calls, the price per call, the accepted rails, and the custody position."),
			Local:  true,
			// No BuildURL: handled locally.
		},
	}
}
