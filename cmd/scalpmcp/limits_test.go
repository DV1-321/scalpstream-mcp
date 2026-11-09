package main

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// Sweep 2026-11-08, finding G-2. SCALPMCP_BUDGET_USD=0 and any negative value
// took the warning-and-default path, so a user turning spending off with a key
// still set got a $2.00 budget, told only by one stderr line.
func TestSpendingLimitValues(t *testing.T) {
	def := big.NewInt(2_000_000)
	for raw, want := range map[string]int64{
		"":          2_000_000, // unset: the default
		"  ":        2_000_000,
		"0":         0, // zero is honoured: spend nothing
		"0.00":      0,
		"0.10":      100_000,
		"2":         2_000_000,
		"0.000001":  1,
		"0.0000015": 1, // rounded down, never up
	} {
		got, err := parseUSD(raw, def)
		if err != nil {
			t.Errorf("parseUSD(%q): %v", raw, err)
			continue
		}
		if got.Cmp(big.NewInt(want)) != 0 {
			t.Errorf("parseUSD(%q) = %s, want %d", raw, got, want)
		}
	}
	// A value that is not a limit stops the server rather than becoming the
	// default, which could be more than the user meant to allow.
	for _, raw := range []string{"-1", "-0.01", "two dollars", "$2", "0.0000001"} {
		if got, err := parseUSD(raw, def); err == nil {
			t.Errorf("parseUSD(%q) = %s, want an error", raw, got)
		}
	}
}

// A user who chose to spend nothing must not be asked to fund and install a key
// that still would not spend.
func TestAZeroLimitWithNoKeyDoesNotAskForAKey(t *testing.T) {
	for name, set := range map[string]func(*toolset){
		"budget 0": func(ts *toolset) { ts.client.Budget = big.NewInt(0) },
		"cap 0":    func(ts *toolset) { ts.client.MaxPriceAtomic = big.NewInt(0) },
	} {
		t.Run(name, func(t *testing.T) {
			seller := newStubSeller(t, []byte(`{"crossings":[]}`), []byte(`{"sample":true}`), false)
			ts := payingToolset(t, seller.URL)
			ts.client.Signer = nil
			set(ts)

			_, env, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
			if err != nil {
				t.Fatal(err)
			}
			if env["paid"] != false || env["data"] == nil {
				t.Errorf("paid=%v data=%v, want the free preview", env["paid"], env["data"])
			}
			st := ts.status()["data"].(map[string]any)
			for what, text := range map[string]string{
				"reason":        env["reason"].(string),
				"howto":         env["howto"].(string),
				"instructions":  ts.instructions(),
				"how_to_enable": st["how_to_enable"].(string),
			} {
				if strings.Contains(text, "EVM_BASE_PRIVATE_KEY") {
					t.Errorf("%s asks for a key a zero limit would not spend: %s", what, text)
				}
			}
			if why := env["reason"].(string); !strings.Contains(why, "turned off") {
				t.Errorf("reason = %q, want it to say spending is turned off", why)
			}
			if !strings.Contains(ts.instructions(), "SPENDING OFF") {
				t.Errorf("instructions do not say spending is off: %s", ts.instructions())
			}
			if how := env["howto"].(string); !strings.Contains(how, "SCALPMCP_BUDGET_USD") {
				t.Errorf("howto = %q, want it to name the limit", how)
			}
		})
	}
}

func TestAZeroLimitSpendsNothingEvenWithAKeySet(t *testing.T) {
	for name, set := range map[string]func(*toolset){
		"budget 0": func(ts *toolset) { ts.client.Budget = big.NewInt(0) },
		"cap 0":    func(ts *toolset) { ts.client.MaxPriceAtomic = big.NewInt(0) },
	} {
		t.Run(name, func(t *testing.T) {
			seller := newStubSeller(t, []byte(`{"crossings":[]}`), []byte(`{"sample":true}`), false)
			ts := payingToolset(t, seller.URL)
			set(ts)

			_, env, err := ts.call(context.Background(), "border_crossings", json.RawMessage(`{"lat":32.7157,"lon":-117.1611}`))
			if err != nil {
				t.Fatal(err)
			}
			if n := seller.payments.Load(); n != 0 {
				t.Fatalf("a zero limit presented %d payment(s)", n)
			}
			if spent, calls := ts.client.Spent(); spent != "0" || calls != 0 {
				t.Errorf("spent %s over %d calls", spent, calls)
			}
			if env["paid"] != false || env["data"] == nil {
				t.Errorf("paid=%v data=%v, want the free preview", env["paid"], env["data"])
			}
			if why, _ := env["reason"].(string); !strings.Contains(why, "turned off") {
				t.Errorf("reason = %q, want it to say spending is turned off", why)
			}
			// The key is set, so "add a key" would be the wrong advice.
			how, _ := env["howto"].(string)
			if strings.Contains(how, "add EVM_BASE_PRIVATE_KEY") || !strings.Contains(how, "SCALPMCP_BUDGET_USD") {
				t.Errorf("howto = %q, want it to name the limit, not ask for a key", how)
			}
			if !strings.Contains(ts.instructions(), "SPENDING OFF") {
				t.Errorf("instructions do not say spending is off: %s", ts.instructions())
			}
			st := ts.status()["data"].(map[string]any)
			if st["paid_mode_enabled"] != false || !strings.Contains(st["how_to_enable"].(string), "SCALPMCP_BUDGET_USD") {
				t.Errorf("payment_status = %v", st)
			}
		})
	}
}
