package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// erc8004/*.json are the agentURIs of on-chain registrations, served raw from
// main, so they go public the moment a branch merges. Each must stay valid
// JSON. The ScalpStream Research one must not offer a withdrawn crypto board,
// must not quote the old 0.02 price (the feed charges about 0.01 and its 402
// quote is authoritative), and points at /.well-known/x402 for the rails
// rather than listing a subset of them.
func TestERC8004Registrations(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "erc8004", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no erc8004 registrations found (err %v)", err)
	}
	for _, p := range paths {
		readJSONFile(t, "erc8004/"+filepath.Base(p))
	}
	m := readJSONFile(t, "erc8004/scalpstream-research.json")
	desc, _ := m["description"].(string)
	lower := strings.ToLower(desc)
	if strings.Contains(lower, "crypto") {
		t.Errorf("description offers a crypto board, withdrawn in 0.1.10: %q", desc)
	}
	if strings.Contains(desc, "0.02") {
		t.Errorf("description quotes 0.02 per call; the feed charges about 0.01: %q", desc)
	}
	if !strings.Contains(desc, "/.well-known/x402") {
		t.Errorf("description does not point at /.well-known/x402 for the payment rails: %q", desc)
	}
}
