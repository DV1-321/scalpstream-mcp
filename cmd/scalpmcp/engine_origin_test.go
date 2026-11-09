package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Sweep 2026-11-08, finding G-7. SCALPMCP_ENGINE_URL was used verbatim, so an
// http:// override sent the status token in cleartext. The token goes over
// https, or to this machine for a local stub, and nowhere else.
func TestTheStatusTokenOnlyTravelsOverHTTPSOrToThisMachine(t *testing.T) {
	const tok = "st-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for origin, allowed := range map[string]bool{
		"https://enroll.scalpstream.com": true,
		"https://status.example.com":     true, // a warning at start, not a refusal
		"http://127.0.0.1:8080":          true,
		"http://localhost:8080":          true,
		"http://[::1]:8080":              true,
		"http://enroll.scalpstream.com":  false,
		"http://192.168.1.50":            false,
		"http://localhost.example.com":   false,
		"ftp://enroll.scalpstream.com":   false,
		"enroll.scalpstream.com":         false,
		"://nonsense":                    false,
	} {
		t.Run(origin, func(t *testing.T) {
			var sent atomic.Int32
			ts := newToolset(&client.Client{}, endpoints{Engine: origin})
			ts.statusToken = func() string { return tok }
			ts.engineHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				sent.Add(1)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Request: r,
					Body: io.NopCloser(strings.NewReader(`{"reporting":true}`))}, nil
			})
			_, _, err := ts.call(context.Background(), "my_engine_status", nil)
			if allowed {
				if err != nil || sent.Load() != 1 {
					t.Fatalf("err=%v requests=%d, want one successful read", err, sent.Load())
				}
				return
			}
			if sent.Load() != 0 {
				t.Fatalf("the token was sent to %s", origin)
			}
			if err == nil || !strings.Contains(err.Error(), "https") {
				t.Fatalf("err = %v, want a refusal naming https", err)
			}
			if strings.Contains(err.Error(), tok) {
				t.Fatal("the refusal quotes the token")
			}
		})
	}
}

// Go keeps Authorization on a same-host redirect, including https to http, so
// the token is not carried through one. The status route never redirects.
func TestTheStatusTokenDoesNotFollowARedirect(t *testing.T) {
	const tok = "st-cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	var elsewhere atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			elsewhere.Add(1)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	ts := newToolset(&client.Client{}, endpoints{Engine: srv.URL})
	ts.statusToken = func() string { return tok }
	_, _, err := ts.call(context.Background(), "my_engine_status", nil)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("err = %v, want the 302 reported", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the redirect was followed with the token")
	}
}

func TestAnEngineOverrideOutsideScalpstreamIsWarnedAboutAtStart(t *testing.T) {
	for origin, warns := range map[string]bool{
		"https://enroll.scalpstream.com/": false,
		"https://scalpstream.com":         false,
		"http://127.0.0.1:8080":           false,
		"https://evilscalpstream.com":     true,
		"https://status.example.com":      true,
		"http://enroll.scalpstream.com":   true,
	} {
		t.Run(origin, func(t *testing.T) {
			buf := captureLog(t)
			t.Setenv("SCALPMCP_ENGINE_URL", origin)
			if got := endpointsFromEnv().Engine; got != strings.TrimRight(origin, "/") {
				t.Errorf("Engine = %q", got)
			}
			if got := strings.Contains(buf.String(), "WARNING"); got != warns {
				t.Errorf("warned=%v, want %v: %q", got, warns, buf.String())
			}
		})
	}
}
