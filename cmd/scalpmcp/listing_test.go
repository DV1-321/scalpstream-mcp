package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// The README's tool table is what directories copy and what a reader sees
// before installing anything. It drifted once already: product_recalls shipped
// in v0.1.6 and the table kept listing eight tools while the server registered
// nine. Nothing failed, because nothing compared the two.
//
// This compares them both ways: every registered tool has a row, and every row
// names a tool that is actually registered.
func TestReadmeToolTableMatchesRegisteredTools(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	row := regexp.MustCompile("^\\|\\s*`([A-Za-z0-9_]+)`\\s*\\|")
	inReadme := map[string]bool{}
	inTools := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			inTools = line == "## Tools"
			continue
		}
		if !inTools {
			continue
		}
		if m := row.FindStringSubmatch(line); m != nil {
			inReadme[m[1]] = true
		}
	}
	if len(inReadme) == 0 {
		t.Fatal("found no tool rows under a '## Tools' heading in README.md")
	}

	registered := map[string]bool{}
	for _, tl := range buildTools(defaultEndpoints()) {
		registered[tl.Name] = true
	}

	var missing, stale []string
	for name := range registered {
		if !inReadme[name] {
			missing = append(missing, name)
		}
	}
	for name := range inReadme {
		if !registered[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("registered but not in the README tool table: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("in the README tool table but not registered: %v", stale)
	}
}

// The MCP Registry schema caps description and title at 100 characters. The
// registry publish is the LAST step of publish.yml, after the image has been
// pushed under the release tag, so an over-long description would fail a
// release halfway: image out, no registry entry. Catch it on every push.
func TestServerJSONFitsRegistryLimits(t *testing.T) {
	s := readServerJSON(t)
	for _, field := range []string{"description", "title"} {
		v, _ := s[field].(string)
		if v == "" {
			t.Errorf("server.json %s is empty", field)
			continue
		}
		if n := utf8.RuneCountInString(v); n > 100 {
			t.Errorf("server.json %s is %d characters; the registry schema allows 100", field, n)
		}
	}
}
