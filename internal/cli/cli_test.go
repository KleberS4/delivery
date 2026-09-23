package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cache"
	"github.com/kleberS4/delivery/internal/cli"
	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/integrity"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/trust"
)

// run executes the command line, capturing both channels separately. This is
// how the output contract is verified from outside, as the agent sees it.
func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = cli.Execute(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// setupHome prepares isolated state and returns the paths.
func setupHome(t *testing.T) config.Paths {
	t.Helper()
	home := t.TempDir()
	claude := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv("DELIVERY_CLAUDE_HOME", claude)
	return config.PathsFor(home)
}

// seedTrusted writes a trusted skill with the given content, with no network.
func seedTrusted(t *testing.T, paths config.Paths, rawRef, shortName string, a *artifact.Artifact) trust.Record {
	t.Helper()
	if err := config.EnsureDirs(paths); err != nil {
		t.Fatal(err)
	}
	r, err := ref.Parse(rawRef)
	if err != nil {
		t.Fatal(err)
	}
	a.Normalize()
	setHash, files := integrity.HashSet(a)

	if err := cache.New(paths.ContentDir, paths.IndexDir).Put(r, a, setHash, files); err != nil {
		t.Fatal(err)
	}
	store, err := trust.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	rec := trust.Record{
		Ref:         r.Canonical(),
		Kind:        r.Kind.String(),
		ShortName:   shortName,
		Description: "test skill",
		MetaOrigin:  "frontmatter",
		SetHash:     setHash,
		FileHashes:  files,
		SourceKind:  "tarball",
		TrustedAt:   time.Unix(0, 0).UTC(),
	}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// Stdout carries exactly the document, and nothing else.
func TestGetEmitsOnlyTheDocumentOnStdout(t *testing.T) {
	paths := setupHome(t)
	doc := "# Test skill\n\nSkill instructions.\n"
	seedTrusted(t, paths, "gh:acme/skills/teste", "teste",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte(doc)})

	stdout, _, code := run(t, "get", "teste")

	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if stdout != doc {
		t.Fatalf("stdout differs:\n  want %q\n  got  %q", doc, stdout)
	}
}

// With resources, the block is appended and comes last.
func TestGetAppendsResourceBlockLast(t *testing.T) {
	paths := setupHome(t)
	doc := "# With resources\n"
	seedTrusted(t, paths, "gh:acme/skills/withresources", "resources", &artifact.Artifact{
		Kind:     artifact.KindSkill,
		Document: []byte(doc),
		Resources: []artifact.Resource{
			{RelPath: "references/guide.md", Content: []byte("guide")},
			{RelPath: "scripts/run.sh", Content: []byte("echo")},
		},
	})

	stdout, _, code := run(t, "get", "resources")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasPrefix(stdout, doc) {
		t.Fatal("stdout does not start with the skill document")
	}
	if !strings.Contains(stdout, "<!-- delivery:resources -->") {
		t.Fatal("resource block missing")
	}
	// The block names the directory; it does not enumerate. Listing every path
	// cost more context than the skill itself.
	if !strings.Contains(stdout, paths.CacheDir) {
		t.Fatal("resource block does not name the directory the files are under")
	}
	if strings.Contains(stdout, "references/guide.md") || strings.Contains(stdout, "scripts/run.sh") {
		t.Fatal("resource block enumerates individual files")
	}
	if !strings.HasSuffix(strings.TrimSpace(stdout), "<!-- /delivery:resources -->") {
		t.Fatal("the resource block is not the last content in the output")
	}
}

// With no resources, no block is appended.
func TestGetWithoutResourcesAddsNothing(t *testing.T) {
	paths := setupHome(t)
	doc := "# No resources\n"
	seedTrusted(t, paths, "gh:acme/skills/simples", "simple",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte(doc)})

	stdout, _, _ := run(t, "get", "simple")
	if strings.Contains(stdout, "delivery:resources") {
		t.Fatal("resource block appended to a skill with no resources")
	}
}

// An untrusted skill fails with its own code, empty stdout,
// and a message carrying the literal corrective command.
func TestGetUntrustedFailsWithActionAndEmptyStdout(t *testing.T) {
	setupHome(t)

	stdout, stderr, code := run(t, "get", "gh:acme/skills/desconhecida")

	if code != cli.ExitNotTrust {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitNotTrust)
	}
	if stdout != "" {
		t.Fatalf("stdout should be empty on failure, got %q", stdout)
	}
	if !strings.Contains(stderr, "delivery trust") {
		t.Fatalf("stderr lacks the corrective command: %q", stderr)
	}
}

// "not trusted" and "incorrect usage" are distinct situations.
func TestInvalidReferenceIsUsageError(t *testing.T) {
	setupHome(t)

	stdout, _, code := run(t, "get", "gh:acme/skills/../../etc/passwd")
	if code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if stdout != "" {
		t.Fatalf("stdout should be empty, got %q", stdout)
	}
}

// Content differing from what was approved fails, delivering nothing.
func TestIntegrityMismatchFailsClosed(t *testing.T) {
	paths := setupHome(t)
	rec := seedTrusted(t, paths, "gh:acme/skills/adulterada", "tampered",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# original\n")})

	// Tamper with the cache from outside, as a hostile process on disk would.
	r, err := ref.Parse(rec.Ref)
	if err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(cache.New(paths.ContentDir, paths.IndexDir).Dir(r), "document.md")
	if err := os.WriteFile(docPath, []byte("# INJECTED CONTENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "get", "tampered")

	if code != cli.ExitIntegrity {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitIntegrity)
	}
	if stdout != "" {
		t.Fatalf("unverified content leaked to stdout: %q", stdout)
	}
	if strings.Contains(stdout, "INJECTED") {
		t.Fatal("tampered content reached the payload channel")
	}
	if !strings.Contains(stderr, "re-approve") {
		t.Fatalf("stderr lacks re-approval guidance: %q", stderr)
	}
}

// An ambiguous short name fails while listing the candidates.
func TestAmbiguousShortNameListsCandidates(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/primeira", "pdf",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# a\n")})
	seedTrusted(t, paths, "gh:other/skills/second", "pdf",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# b\n")})

	stdout, stderr, code := run(t, "get", "pdf")

	if code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if stdout != "" {
		t.Fatalf("stdout should be empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "gh:acme/skills/primeira") ||
		!strings.Contains(stderr, "gh:other/skills/second") {
		t.Fatalf("candidates not listed: %q", stderr)
	}
}

// The canonical reference always works, even with an ambiguous name.
func TestCanonicalReferenceAlwaysResolves(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/primeira", "pdf",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# a\n")})
	seedTrusted(t, paths, "gh:other/skills/second", "pdf",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# b\n")})

	stdout, _, code := run(t, "get", "gh:acme/skills/primeira")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if stdout != "# a\n" {
		t.Fatalf("document differs: %q", stdout)
	}
}

// Trust requires an interactive terminal. Tests have no TTY.
func TestTrustWithoutTerminalFails(t *testing.T) {
	setupHome(t)

	stdout, stderr, code := run(t, "trust", "gh:acme/skills/qualquer")

	if code != cli.ExitTTY {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitTTY)
	}
	if stdout != "" {
		t.Fatalf("stdout should be empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "terminal") {
		t.Fatalf("stderr does not explain the terminal requirement: %q", stderr)
	}
}

// Init creates state and the anchor, writing nothing to stdout.
func TestInitCreatesAnchorAndKeepsStdoutClean(t *testing.T) {
	paths := setupHome(t)

	stdout, stderr, code := run(t, "init")

	if code != cli.ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("init wrote to stdout: %q", stdout)
	}

	anchorPath := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "delivery", "SKILL.md")
	content, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatalf("the anchor was not created: %v", err)
	}
	if !strings.Contains(string(content), "name: delivery") {
		t.Error("anchor has no valid frontmatter")
	}
	if _, err := os.Stat(paths.ContentDir); err != nil {
		t.Errorf("cache directory was not created: %v", err)
	}
}

// Init is idempotent and preserves trusted skills.
func TestInitIsIdempotent(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/preservada", "preserved",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# p\n")})

	for i := 0; i < 3; i++ {
		if _, stderr, code := run(t, "init"); code != cli.ExitOK {
			t.Fatalf("init %d failed (%d): %s", i, code, stderr)
		}
	}

	store, err := trust.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Fatalf("records after three inits = %d, want 1", store.Len())
	}

	anchorPath := filepath.Join(os.Getenv("DELIVERY_CLAUDE_HOME"), "skills", "delivery", "SKILL.md")
	content, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "gh:acme/skills/preservada") {
		t.Error("a trusted skill vanished from the index after reinitialisation")
	}
}

// The output contract holds in every error situation.
func TestStdoutStaysCleanOnEveryFailure(t *testing.T) {
	setupHome(t)

	cases := [][]string{
		{"get", "inexistente"},
		{"get", "gh:a/b/../c"},
		{"trust", "gh:a/b/c"},
		{"no-such-command"},
		{"get"},
	}

	for _, args := range cases {
		stdout, _, code := run(t, args...)
		if code == cli.ExitOK {
			t.Errorf("%v: expected failure, got success", args)
		}
		if stdout != "" {
			t.Errorf("%v: stdout contaminated on failure: %q", args, stdout)
		}
	}
}

// The whole reason the block names a directory instead of listing paths: its
// size must not track the number of files. A skill of sixty files once paid
// 12 KB of listing against a 7 KB body, on every get.
func TestResourceBlockSizeDoesNotTrackFileCount(t *testing.T) {
	block := func(t *testing.T, n int) string {
		t.Helper()
		paths := setupHome(t)
		res := make([]artifact.Resource, 0, n)
		for i := 0; i < n; i++ {
			res = append(res, artifact.Resource{
				RelPath: fmt.Sprintf("scripts/nested/deeply/file%03d.py", i),
				Content: []byte("x"),
			})
		}
		seedTrusted(t, paths, "gh:acme/skills/many", "many", &artifact.Artifact{
			Kind:      artifact.KindSkill,
			Document:  []byte("# Many\n"),
			Resources: res,
		})
		stdout, _, code := run(t, "get", "many")
		if code != cli.ExitOK {
			t.Fatalf("exit code = %d", code)
		}
		i := strings.LastIndex(stdout, "<!-- delivery:resources -->")
		if i < 0 {
			t.Fatal("resource block missing")
		}
		return stdout[i:]
	}

	small := block(t, 2)
	large := block(t, 300)

	// Only the file count differs between the two, so the difference has to be
	// a handful of characters, not kilobytes.
	if d := len(large) - len(small); d > 8 {
		t.Errorf("block grew %d bytes going from 2 files to 300; it must name the directory, not the files\n--- 2 files:\n%s\n--- 300 files:\n%s",
			d, small, large)
	}
	if strings.Contains(large, "file299.py") {
		t.Error("the block enumerated individual files")
	}
}
