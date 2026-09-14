package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cli"
	"github.com/kleberS4/delivery/internal/footer"
)

const registryPayload = `{"query":"pdf","skills":[
 {"id":"acme/skills/pdf","skillId":"pdf","name":"pdf","installs":900,"source":"acme/skills"},
 {"id":"other/skills/pdf","skillId":"pdf","name":"pdf","installs":100,"source":"outra/skills"}
]}`

func startRegistry(t *testing.T, payload string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/search") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Search lists results and marks each one's trust state.
func TestSearchMarksTrustState(t *testing.T) {
	paths := setupHome(t)
	t.Setenv(cli.EnvRegistryBase, startRegistry(t, registryPayload))

	seedTrusted(t, paths, "acme/skills/pdf", "pdf",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# pdf\n")})

	stdout, stderr, code := run(t, "search", "pdf")
	if code != cli.ExitOK {
		t.Fatalf("code = %d: %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("human-readable search wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "✓ acme/skills/pdf") {
		t.Errorf("trusted skill was not marked: %q", stderr)
	}
	if !strings.Contains(stderr, "other/skills/pdf") {
		t.Errorf("untrusted skill missing: %q", stderr)
	}
}

// The structured variant goes to stdout, for programmatic consumption.
func TestSearchJSONGoesToStdout(t *testing.T) {
	setupHome(t)
	t.Setenv(cli.EnvRegistryBase, startRegistry(t, registryPayload))

	stdout, _, code := run(t, "search", "pdf", "--json")
	if code != cli.ExitOK {
		t.Fatalf("code = %d", code)
	}

	var parsed struct {
		Stale bool `json:"stale"`
		Hits  []struct {
			ID      string `json:"id"`
			Trusted bool   `json:"trusted"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if len(parsed.Hits) != 2 {
		t.Fatalf("results = %d, want 2", len(parsed.Hits))
	}
	if parsed.Hits[0].Trusted {
		t.Error("an untrusted skill was marked as trusted")
	}
}

// A stale index with no network is served anyway, with a warning.
// Searching is informational: an old result still guides.
func TestSearchServesStaleIndexWhenRegistryIsDown(t *testing.T) {
	setupHome(t)

	base := startRegistry(t, registryPayload)
	t.Setenv(cli.EnvRegistryBase, base)
	if _, _, code := run(t, "search", "pdf"); code != cli.ExitOK {
		t.Fatal("initial search failed")
	}

	// Registry unreachable: the cached index stays usable.
	t.Setenv(cli.EnvRegistryBase, "https://127.0.0.1:1")

	_, stderr, code := run(t, "search", "pdf")
	if code != cli.ExitOK {
		t.Fatalf("search with the registry down failed: %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "pdf") {
		t.Errorf("cached results were not served: %q", stderr)
	}
}

// Install writes the original content with the block at the end.
func TestInstallWritesOriginalContentWithFooter(t *testing.T) {
	paths := setupHome(t)
	doc := "---\nname: minha\ndescription: x\n---\n\n# Corpo da skill\n"
	seedTrusted(t, paths, "gh:acme/skills/minha", "mine",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte(doc)})

	if _, stderr, code := run(t, "install", "mine"); code != cli.ExitOK {
		t.Fatalf("install failed (%d): %s", code, stderr)
	}

	p := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "mine", "SKILL.md")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("the skill was not written: %v", err)
	}

	if !strings.HasPrefix(string(content), "---\nname: minha") {
		t.Error("the frontmatter is no longer the start of the file")
	}
	if !footer.IsManaged(content) {
		t.Error("installed file has no management block")
	}
	if string(footer.Strip(content)) != doc {
		t.Error("the original content was altered during installation")
	}
}

// Uninstall refuses to remove a file delivery does not manage.
func TestUninstallRefusesUnmanagedFile(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/manual", "manual",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# m\n")})

	// A hand-written file, with no management block.
	dir := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "manual")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	handwritten := "# Written by hand by the person\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(handwritten), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "uninstall", "manual")
	if code == cli.ExitOK {
		t.Fatal("uninstall removed an unmanaged file")
	}
	if stdout != "" {
		t.Fatalf("stdout contaminated: %q", stdout)
	}
	if !strings.Contains(stderr, "not managed by delivery") {
		t.Errorf("message does not explain the refusal: %q", stderr)
	}

	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || string(got) != handwritten {
		t.Fatal("the hand-written file was altered or removed")
	}
}

// The full install and uninstall cycle.
func TestInstallThenUninstall(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/ciclo", "cycle",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# c\n")})

	if _, stderr, code := run(t, "install", "cycle"); code != cli.ExitOK {
		t.Fatalf("install failed: %s", stderr)
	}
	if _, stderr, code := run(t, "uninstall", "cycle"); code != cli.ExitOK {
		t.Fatalf("uninstall failed: %s", stderr)
	}

	p := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "cycle", "SKILL.md")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("the file remained on disk after uninstall")
	}

	// Trust remains: uninstalling is not revoking.
	if _, _, code := run(t, "get", "cycle"); code != cli.ExitOK {
		t.Fatal("trust was lost when uninstalling")
	}
}

// Untrust removes the skill from the index and get starts refusing.
func TestUntrustRemovesFromIndex(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/temp", "temp",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# t\n")})

	if _, _, code := run(t, "get", "temp"); code != cli.ExitOK {
		t.Fatal("the skill should be available before untrust")
	}
	if _, stderr, code := run(t, "untrust", "temp"); code != cli.ExitOK {
		t.Fatalf("untrust failed: %s", stderr)
	}
	if _, _, code := run(t, "get", "temp"); code != cli.ExitNotTrust {
		t.Fatalf("get after untrust returned %d, want %d", code, cli.ExitNotTrust)
	}

	anchorPath := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "delivery", "SKILL.md")
	content, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "gh:acme/skills/temp") {
		t.Error("the skill stayed in the anchor index after untrust")
	}
	_ = paths
}

// The audit distinguishes trusted from installed.
func TestListShowsInstalledState(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/a", "a",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# a\n")})
	seedTrusted(t, paths, "gh:acme/skills/b", "b",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# b\n")})

	if _, _, code := run(t, "install", "a"); code != cli.ExitOK {
		t.Fatal("install failed")
	}

	stdout, stderr, code := run(t, "list")
	if code != cli.ExitOK {
		t.Fatalf("list failed (%d): %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("human-readable list wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "installed") {
		t.Errorf("installed state not shown: %q", stderr)
	}
}

func TestListEmptyGuidesTheUser(t *testing.T) {
	setupHome(t)
	_, stderr, code := run(t, "list")
	if code != cli.ExitOK {
		t.Fatalf("list failed: %d", code)
	}
	if !strings.Contains(stderr, "delivery search") || !strings.Contains(stderr, "delivery trust") {
		t.Errorf("empty inventory does not guide: %q", stderr)
	}
}

// Trust --source requires a terminal, just like trust.
func TestTrustSourceRequiresTerminal(t *testing.T) {
	setupHome(t)
	stdout, stderr, code := run(t, "trust", "--source", "acme/skills")
	if code != cli.ExitTTY {
		t.Fatalf("code = %d, want %d", code, cli.ExitTTY)
	}
	if stdout != "" {
		t.Fatalf("stdout contaminated: %q", stdout)
	}
	if !strings.Contains(stderr, "terminal") {
		t.Errorf("message does not explain the requirement: %q", stderr)
	}
}

// With no changes, update says so instead of exiting silently.
func TestUpdateWithNothingTrustedIsExplicit(t *testing.T) {
	setupHome(t)
	stdout, stderr, code := run(t, "update")
	if code != cli.ExitOK {
		t.Fatalf("update failed (%d): %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout contaminated: %q", stdout)
	}
	if !strings.Contains(stderr, "no updates") {
		t.Errorf("update was silent: %q", stderr)
	}
}

// The output contract still holds for every new command.
func TestStdoutStaysCleanAcrossU2Commands(t *testing.T) {
	setupHome(t)
	t.Setenv(cli.EnvRegistryBase, "https://127.0.0.1:1")

	cases := [][]string{
		{"search", "anything"},
		{"untrust", "missing"},
		{"install", "missing"},
		{"uninstall", "missing"},
		{"update", "missing"},
		{"trust", "--source", "invalid"},
	}

	for _, args := range cases {
		stdout, _, code := run(t, args...)
		if code == cli.ExitOK {
			t.Errorf("%v: expected failure", args)
		}
		if stdout != "" {
			t.Errorf("%v: stdout contaminated on failure: %q", args, stdout)
		}
	}
}
