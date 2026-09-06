package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readServerJSON(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "server.json"))
	if err != nil {
		t.Fatalf("cannot read server.json: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("server.json is not valid JSON: %v", err)
	}
	return s
}

// A user meets this server's version three times: the registry entry
// (server.json), the image tag they pull, and the serverInfo returned by
// initialize. They are only worth anything if they agree.
//
// They did not. v0.1.4 published an image whose initialize reported 0.1.3,
// because the release workflow computed VERSION and then never passed it to the
// linker, so buildVersion kept its hardcoded fallback — and the fallback was
// stale because the workflow rewrote server.json in CI without committing it,
// leaving the repo permanently one release behind. Two independent drifts, both
// invisible until the published image was actually pulled and run.
//
// The workflow now stamps buildVersion and refuses to publish a tag that
// disagrees with server.json. This test covers the leg the workflow cannot:
// that the in-repo fallback matches the in-repo manifest, so a build made
// WITHOUT the ldflag — `go install`, `go build`, any local run — still reports
// the right number.
func TestBuildVersionMatchesServerJSON(t *testing.T) {
	s := readServerJSON(t)
	want, _ := s["version"].(string)
	if want == "" {
		t.Fatal("server.json has no version")
	}
	if buildVersion != want {
		t.Errorf("buildVersion = %q but server.json version = %q\n"+
			"An unstamped build (go install) would report the wrong version. "+
			"Update both, in the same commit as the release.", buildVersion, want)
	}
}

// server.json pins an exact image tag rather than a floating one, so the
// manifest and the artifact it names have to move together.
func TestServerJSONPinsTheMatchingImageTag(t *testing.T) {
	s := readServerJSON(t)
	version, _ := s["version"].(string)
	pkgs, _ := s["packages"].([]any)
	if len(pkgs) == 0 {
		t.Fatal("server.json declares no packages")
	}
	found := false
	for _, p := range pkgs {
		m, _ := p.(map[string]any)
		if m["registryType"] != "oci" {
			continue
		}
		found = true
		id, _ := m["identifier"].(string)
		want := "ghcr.io/dv1-321/scalpstream-mcp:v" + version
		if id != want {
			t.Errorf("oci identifier = %q, want %q", id, want)
		}
		if strings.HasSuffix(id, ":latest") {
			t.Error("oci identifier must pin an exact tag, not :latest")
		}
	}
	if !found {
		t.Error("server.json declares no oci package")
	}
}
