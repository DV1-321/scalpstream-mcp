package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
)

// Sweep 2026-11-08, finding G-6. The descriptions promise ranges the code did
// not enforce: hours:-5 and year:1e300 went to the seller, the second through
// int() of an out-of-range float. A bad argument is an error before any request
// and so before any 402 is answered; the bounds are the tool descriptions' and
// the same ones the scouts Actors enforce.
func TestArgumentsOutOfRangeAreRefusedBeforeAnyRequest(t *testing.T) {
	const here = `"lat":32.7157,"lon":-117.1611`
	const car = `"make":"Honda","model":"Civic"`
	for _, tc := range []struct{ tool, args, want string }{
		{"air_quality", `{` + here + `,"hours":-5}`, "hours"},
		{"air_quality", `{` + here + `,"hours":0}`, "hours"},
		{"air_quality", `{` + here + `,"hours":169}`, "hours"},
		{"air_quality", `{` + here + `,"hours":1e300}`, "hours"},
		{"air_quality", `{` + here + `,"hours":1.5}`, "hours"},
		{"air_quality", `{` + here + `,"hours":"soon"}`, "hours"},
		{"air_quality", `{` + here + `,"duration":0}`, "duration"},
		{"air_quality", `{` + here + `,"duration":-1}`, "duration"},
		{"air_quality", `{` + here + `,"duration":13}`, "duration"},
		{"air_quality", `{` + here + `,"duration":2.5}`, "duration"},
		{"air_quality", `{` + here + `,"hours":6,"duration":12}`, "cannot exceed"},
		{"border_crossings", `{` + here + `,"radius":-1}`, "radius"},
		{"border_crossings", `{` + here + `,"radius":"near"}`, "radius"},
		{"border_crossings", `{` + here + `,"limit":0}`, "limit"},
		{"border_crossings", `{` + here + `,"limit":-3}`, "limit"},
		{"border_crossings", `{` + here + `,"limit":101}`, "limit"},
		{"border_crossings", `{` + here + `,"limit":1e300}`, "limit"},
		{"border_crossings", `{` + here + `,"limit":2.5}`, "limit"},
		{"border_crossings", `{` + here + `,"limit":"many"}`, "limit"},
		{"product_recalls", `{` + car + `,"year":1e300}`, "year"},
		{"product_recalls", `{` + car + `,"year":1899}`, "year"},
		{"product_recalls", `{` + car + `,"year":2101}`, "year"},
		{"product_recalls", `{` + car + `,"year":2020.5}`, "year"},
		{"product_recalls", `{` + car + `,"year":"last"}`, "year"},
		{"product_recalls", `{"product":"infant formula","limit":0}`, "limit"},
		{"product_recalls", `{"product":"infant formula","limit":101}`, "limit"},
		{"product_recalls", `{"product":"infant formula","limit":1e300}`, "limit"},
		{"product_recalls", `{"product":"infant formula","limit":"lots"}`, "limit"},
	} {
		t.Run(tc.tool+" "+tc.args, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			ts := newToolset(&client.Client{}, endpoints{Air: srv.URL, Border: srv.URL, Recall: srv.URL})
			_, _, err := ts.call(context.Background(), tc.tool, json.RawMessage(tc.args))
			if err == nil {
				t.Fatalf("no error; the seller saw %d request(s)", hits.Load())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			if hits.Load() != 0 {
				t.Errorf("the seller saw %d request(s) for a refused argument", hits.Load())
			}
		})
	}
}

// What the tools promise still works, and what a model sends for "not set" is
// still "not set".
func TestArgumentsInRangeReachTheURL(t *testing.T) {
	byName := map[string]tool{}
	for _, tl := range buildTools(endpoints{Air: "https://a", Border: "https://b", Recall: "https://r"}) {
		byName[tl.Name] = tl
	}
	lat, lon := 32.7157, -117.1611
	for _, tc := range []struct {
		tool       string
		args       map[string]any
		want, deny []string
	}{
		{"air_quality", map[string]any{"lat": lat, "lon": lon, "hours": 168.0, "duration": 12.0}, []string{"hours=168", "duration=12"}, nil},
		{"air_quality", map[string]any{"lat": lat, "lon": lon, "hours": "48", "duration": "2"}, []string{"hours=48", "duration=2"}, nil},
		{"air_quality", map[string]any{"lat": lat, "lon": lon, "hours": "", "duration": " "}, nil, []string{"hours=", "duration="}},
		{"air_quality", map[string]any{"lat": lat, "lon": lon, "duration": 12.0}, []string{"duration=12"}, []string{"hours="}},
		{"border_crossings", map[string]any{"lat": lat, "lon": lon, "radius": 0.0, "limit": 100.0}, []string{"limit=100"}, []string{"radius="}},
		{"border_crossings", map[string]any{"lat": lat, "lon": lon, "radius": 48.5, "limit": "7"}, []string{"radius=48.5", "limit=7"}, nil},
		{"product_recalls", map[string]any{"make": "Honda", "model": "Civic", "year": 2024.0, "limit": 100.0}, []string{"year=2024", "limit=100"}, nil},
		{"product_recalls", map[string]any{"make": "Honda", "model": "Civic", "year": "2024"}, []string{"year=2024"}, []string{"limit="}},
		{"product_recalls", map[string]any{"product": "infant formula", "year": ""}, []string{"product="}, []string{"year=", "make="}},
	} {
		u, err := byName[tc.tool].BuildURL(tc.args)
		if err != nil {
			t.Errorf("%s %v: %v", tc.tool, tc.args, err)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(u, want) {
				t.Errorf("%s %v: %s lacks %s", tc.tool, tc.args, u, want)
			}
		}
		for _, deny := range tc.deny {
			if strings.Contains(u, deny) {
				t.Errorf("%s %v: %s carries %s", tc.tool, tc.args, u, deny)
			}
		}
	}
}
