package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// An older install that still lists a withdrawn tool gets an HTTP error from
// the feed, and the docs tell an installing agent which one to expect. The
// two tools fail differently: /yields is unregistered (404 Not Found) while
// /crypto is mounted on a handler that answers 410 Gone with
// "error": "withdrawn", ahead of the payment gate. A doc that promises only a
// 404 for crypto_research sends the reader looking for a status that never
// appears, so every surface that explains the withdrawal must name the 410.
func TestWithdrawnToolDocsNameTheFeedStatus(t *testing.T) {
	gone410 := regexp.MustCompile(`\b410\b`)
	only404 := regexp.MustCompile(`crypto_research\W[^|\n]{0,80}\breturns 404\s*\|`)
	for _, rel := range []string{"README.md", "llms-install.md", "CHANGELOG.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("cannot read %s: %v", rel, err)
		}
		if !gone410.Match(b) {
			t.Errorf("%s explains the crypto_research withdrawal without the feed's 410 Gone", rel)
		}
		if only404.Match(b) {
			t.Errorf("%s says crypto_research returns 404; the feed answers 410 Gone", rel)
		}
	}
}
