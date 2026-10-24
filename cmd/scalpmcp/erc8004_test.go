package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// erc8004/*.json are the agentURIs of on-chain registrations, served raw from
// main, so they go public the moment a branch merges. Each must stay valid
// JSON. ScalpStream Research has been withdrawn, so its registration says so,
// is marked inactive and no longer lists the A2A and x402 services, whose host
// answers 410 Gone on every path. The four scouts, AirScout included since it
// returned on self-hosted data, are active.
func TestERC8004Registrations(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "erc8004", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no erc8004 registrations found (err %v)", err)
	}
	for _, p := range paths {
		readJSONFile(t, "erc8004/"+filepath.Base(p))
	}

	research := readJSONFile(t, "erc8004/scalpstream-research.json")
	desc, _ := research["description"].(string)
	if !strings.Contains(desc, "has been withdrawn") {
		t.Errorf("ScalpStream Research description does not say it has been withdrawn: %q", desc)
	}
	for _, offer := range []string{"crypto", "options", "dividend", "USDC", "per call"} {
		if strings.Contains(strings.ToLower(desc), strings.ToLower(offer)) {
			t.Errorf("ScalpStream Research description still offers %q: %q", offer, desc)
		}
	}
	if research["active"] != false || research["x402Support"] != false {
		t.Errorf("ScalpStream Research: active=%v x402Support=%v, want false/false", research["active"], research["x402Support"])
	}
	services, _ := research["services"].([]any)
	for _, s := range services {
		m, _ := s.(map[string]any)
		if name, _ := m["name"].(string); name == "A2A" || name == "x402" {
			t.Errorf("ScalpStream Research still lists its %s service, which answers 410", name)
		}
	}

	air := readJSONFile(t, "erc8004/airscout.json")
	if d, _ := air["description"].(string); strings.Contains(d, "unavailable") || strings.Contains(d, "XRP") {
		t.Errorf("AirScout description still says it is unavailable, or offers a rail it does not take: %q", d)
	}

	for _, rel := range []string{"erc8004/fuelscout.json", "erc8004/airscout.json", "erc8004/borderscout.json", "erc8004/recallscout.json"} {
		if m := readJSONFile(t, rel); m["active"] != true {
			t.Errorf("%s: active = %v, want true", rel, m["active"])
		}
	}
}
