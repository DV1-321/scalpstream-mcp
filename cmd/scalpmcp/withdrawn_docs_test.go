package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("cannot read %s: %v", rel, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// An older install that still lists a tool this release does not offer gets an
// HTTP error from the service, and the docs tell an installing agent which one
// to expect. Since 2026-10-22 the research feed's host answers 410 Gone with
// "error": "withdrawn" on every path, /yields included, and AirScout's host
// answers 503 with "error": "unavailable"; both are answered at the edge,
// before any payment. A doc that still promises a 404 sends the reader looking
// for a status that never appears.
func TestDocsNameTheStatusOfToolsNotOffered(t *testing.T) {
	gone410 := regexp.MustCompile(`\b410\b`)
	held503 := regexp.MustCompile(`\b503\b`)
	any404 := regexp.MustCompile(`\b404\b`)
	for _, rel := range []string{"README.md", "llms-install.md", "CHANGELOG.md"} {
		s := readRepoFile(t, rel)
		if !gone410.MatchString(s) {
			t.Errorf("%s explains the withdrawn tools without the 410 Gone they get", rel)
		}
		if !held503.MatchString(s) {
			t.Errorf("%s explains air_quality without the 503 it gets", rel)
		}
		if any404.MatchString(s) {
			t.Errorf("%s mentions a 404; no tool that is not offered returns one any more", rel)
		}
	}
}

// The README's Tools table and llms-install.md's tool sentence are the
// catalogue as a reader meets it, so each must name exactly the tools
// tools/list serves: a withdrawn or withheld tool named there is documented as
// available, and a served tool left out is one nobody tries.
func TestDocsListExactlyTheOfferedTools(t *testing.T) {
	want := append([]string{}, expectedTools...)
	sort.Strings(want)
	backticked := regexp.MustCompile("`([a-z_]+)`")

	readme := readRepoFile(t, "README.md")
	start := strings.Index(readme, "\n## Tools\n")
	if start < 0 {
		t.Fatal("README.md has no ## Tools section")
	}
	var table []string
	for _, line := range strings.Split(readme[start+len("\n## Tools\n"):], "\n") {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if strings.HasPrefix(line, "| `") {
			if m := backticked.FindStringSubmatch(line); m != nil {
				table = append(table, m[1])
			}
		}
	}
	sort.Strings(table)
	if strings.Join(table, ",") != strings.Join(want, ",") {
		t.Errorf("README.md Tools table lists %v, want exactly %v", table, want)
	}

	install := readRepoFile(t, "llms-install.md")
	i := strings.Index(install, "The tools are ")
	if i < 0 {
		t.Fatal(`llms-install.md has no "The tools are" sentence`)
	}
	sentence := install[i:]
	if end := strings.Index(sentence, "."); end >= 0 {
		sentence = sentence[:end]
	}
	var listed []string
	for _, m := range backticked.FindAllStringSubmatch(sentence, -1) {
		listed = append(listed, m[1])
	}
	sort.Strings(listed)
	if strings.Join(listed, ",") != strings.Join(want, ",") {
		t.Errorf("llms-install.md lists %v, want exactly %v", listed, want)
	}
}
