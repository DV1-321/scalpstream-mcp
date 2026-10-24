package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The repository carries three more install surfaces beside server.json: a
// Gemini CLI extension (gemini-extension.json, indexed by the Gemini gallery
// from the repo), a Claude Code plugin with its own one-plugin marketplace
// (.claude-plugin/), and an Agent Skill (skills/scalpstream-data/SKILL.md).
//
// Each of the manifests names a version and pins an image tag, which is the
// same drift TestBuildVersionMatchesServerJSON exists for, one surface wider:
// a gallery that installs an extension pointing at a tag that was never
// published installs nothing. So every manifest is tied to server.json here,
// and a release that bumps server.json without them fails this test.

func readJSONFile(t *testing.T, rel string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("cannot read %s: %v", rel, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v", rel, err)
	}
	return m
}

// dockerArgsOf returns the args of the one MCP server entry a manifest's
// mcpServers block declares, checking it is the docker command the README
// documents: `docker run -i --rm <image>` and nothing else, so no manifest
// can quietly grow an -e flag that forwards a key by default.
func dockerArgsOf(t *testing.T, rel string, servers any) []string {
	t.Helper()
	m, _ := servers.(map[string]any)
	entry, _ := m["scalpstream"].(map[string]any)
	if entry == nil {
		t.Fatalf("%s: mcpServers.scalpstream is missing", rel)
	}
	if entry["command"] != "docker" {
		t.Errorf("%s: command = %v, want docker", rel, entry["command"])
	}
	raw, _ := entry["args"].([]any)
	var args []string
	for _, a := range raw {
		s, _ := a.(string)
		args = append(args, s)
	}
	if len(args) != 4 || args[0] != "run" || args[1] != "-i" || args[2] != "--rm" {
		t.Errorf("%s: args = %q, want [run -i --rm <image>]", rel, args)
	}
	if _, has := entry["env"]; has {
		t.Errorf("%s: declares an env block; the packaged install must carry no secrets", rel)
	}
	return args
}

func TestGeminiExtensionMatchesServerJSON(t *testing.T) {
	version, _ := readServerJSON(t)["version"].(string)
	ext := readJSONFile(t, "gemini-extension.json")
	if ext["name"] != "scalpstream" {
		t.Errorf("name = %v, want scalpstream", ext["name"])
	}
	if ext["version"] != version {
		t.Errorf("version = %v but server.json version = %q", ext["version"], version)
	}
	if ext["contextFileName"] != "GEMINI.md" {
		t.Errorf("contextFileName = %v, want GEMINI.md", ext["contextFileName"])
	}
	if _, err := os.Stat(filepath.Join("..", "..", "GEMINI.md")); err != nil {
		t.Errorf("GEMINI.md, the declared context file, is missing: %v", err)
	}
	args := dockerArgsOf(t, "gemini-extension.json", ext["mcpServers"])
	if want := "ghcr.io/dv1-321/scalpstream-mcp:v" + version; len(args) == 4 && args[3] != want {
		t.Errorf("image = %q, want %q", args[3], want)
	}
}

func TestClaudePluginMatchesServerJSON(t *testing.T) {
	version, _ := readServerJSON(t)["version"].(string)
	want := "ghcr.io/dv1-321/scalpstream-mcp:v" + version

	plugin := readJSONFile(t, ".claude-plugin/plugin.json")
	if plugin["name"] != "scalpstream" {
		t.Errorf("plugin.json name = %v, want scalpstream", plugin["name"])
	}
	if plugin["version"] != version {
		t.Errorf("plugin.json version = %v but server.json version = %q", plugin["version"], version)
	}
	if args := dockerArgsOf(t, ".claude-plugin/plugin.json", plugin["mcpServers"]); len(args) == 4 && args[3] != want {
		t.Errorf("plugin.json image = %q, want %q", args[3], want)
	}

	market := readJSONFile(t, ".claude-plugin/marketplace.json")
	if _, ok := market["name"].(string); !ok {
		t.Error("marketplace.json has no name")
	}
	owner, _ := market["owner"].(map[string]any)
	if owner == nil || owner["name"] == "" {
		t.Error("marketplace.json has no owner.name")
	}
	plugins, _ := market["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("marketplace.json lists %d plugins, want exactly 1", len(plugins))
	}
	entry, _ := plugins[0].(map[string]any)
	if entry["name"] != "scalpstream" {
		t.Errorf("marketplace plugin name = %v, want scalpstream", entry["name"])
	}
	if entry["version"] != version {
		t.Errorf("marketplace plugin version = %v but server.json version = %q", entry["version"], version)
	}
	if src, _ := entry["source"].(string); src != "./" && src != "." {
		t.Errorf("marketplace plugin source = %q; the plugin is this repository's root", src)
	}
}

// Agent Skills format: YAML frontmatter with at least name and description,
// name matching the directory, so a skills index that reads the frontmatter
// lists it under the name the install command uses.
func TestSkillFrontmatter(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "skills", "scalpstream-data", "SKILL.md"))
	if err != nil {
		t.Fatalf("cannot read SKILL.md: %v", err)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		t.Fatal("SKILL.md does not open with a --- frontmatter fence")
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter is not closed by a --- line")
	}
	front := s[4 : 4+end]
	fields := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if fields["name"] != "scalpstream-data" {
		t.Errorf("frontmatter name = %q, want scalpstream-data (the directory name)", fields["name"])
	}
	if d := fields["description"]; d == "" {
		t.Error("frontmatter has no description")
	} else if len(d) > 1024 {
		t.Errorf("description is %d characters; the skill format caps it at 1024", len(d))
	}
}

// The packaging must carry no secret and must not point an agent at a tool the
// server does not offer: the withdrawn research tools. A hex run of 64 digits is a private key or a
// signature; neither belongs in a file a gallery republishes.
func TestPackagingCarriesNoSecretsAndNoToolsNotOffered(t *testing.T) {
	files := []string{
		"gemini-extension.json", "GEMINI.md",
		".claude-plugin/plugin.json", ".claude-plugin/marketplace.json",
		"skills/scalpstream-data/SKILL.md",
	}
	hexKey := regexp.MustCompile(`(?i)(0x)?[0-9a-f]{64}`)
	secretWords := []string{"-----BEGIN", "PRIVATE KEY-----", "api_key=", "apikey=", "token="}
	research := append([]string{}, withdrawnTools...)
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("cannot read %s: %v", rel, err)
		}
		s := string(b)
		if m := hexKey.FindString(s); m != "" {
			t.Errorf("%s contains a 64-hex-digit run (%s...)", rel, m[:8])
		}
		for _, w := range secretWords {
			if strings.Contains(s, w) {
				t.Errorf("%s contains %q", rel, w)
			}
		}
		if strings.Contains(s, "EVM_BASE_PRIVATE_KEY=") || strings.Contains(s, `"EVM_BASE_PRIVATE_KEY":`) {
			t.Errorf("%s assigns EVM_BASE_PRIVATE_KEY a value; the packaging names the variable only", rel)
		}
		if strings.HasSuffix(rel, ".md") {
			for _, name := range research {
				if strings.Contains(s, name) {
					t.Errorf("%s mentions %s, which the server does not offer", rel, name)
				}
			}
		}
	}
}
