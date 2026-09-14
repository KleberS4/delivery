package cli_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cli"
)

// Bare `delivery` is an orientation screen, not an error, and it obeys the
// output contract like every other command: stdout stays reserved for payload.
func TestBareInvocationDrawsParcelAndKeepsStdoutClean(t *testing.T) {
	setupHome(t)

	stdout, stderr, code := run(t)

	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stderr)
	}
	if stdout != "" {
		t.Fatalf("the banner wrote to stdout: %q", stdout)
	}
	// The row where the box shows everything at once: the lit top face above
	// both side faces, the left one lighter than the right, and the tape down
	// the middle. Three different shades are what make a grid of blocks read as
	// a solid, so this line is the depth claim in a single string.
	if !strings.Contains(stderr, "▒▒▒▒▓▓▓▓██▓▓▓▓░░░░") {
		t.Errorf("the box's three faces were not shaded apart:\n%s", stderr)
	}
	if !strings.Contains(stderr, "▓▓▓▓▓▓▓▓██▓▓▓▓▓▓▓▓") {
		t.Errorf("the top face was not drawn:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Skills arrive when the task needs them") {
		t.Error("the tagline is missing")
	}
}

// An empty index is an invitation to act: it names the commands that get you
// started, not the ones you cannot use yet.
func TestBareInvocationWithEmptyIndexGuidesTheUser(t *testing.T) {
	setupHome(t)

	_, stderr, code := run(t)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	if !strings.Contains(stderr, "nothing in the index yet") {
		t.Errorf("empty state not reported:\n%s", stderr)
	}
	for _, want := range []string{"delivery init", "delivery search", "delivery trust"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("%q missing from the getting-started commands", want)
		}
	}
	if strings.Contains(stderr, "delivery update") {
		t.Error("offered update while nothing is trusted")
	}
}

// With skills trusted, the screen reports the index and the context budget it
// consumes — the number that matters most about this product.
func TestBareInvocationReportsIndexAndBudget(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/one", "one",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# one\n")})
	seedTrusted(t, paths, "gh:acme/skills/two", "two",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# two\n")})

	_, stderr, code := run(t)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	if !strings.Contains(stderr, "2 skills in the index") {
		t.Errorf("index count not reported:\n%s", stderr)
	}
	if !strings.Contains(stderr, "of the anchor's 16 KB budget") {
		t.Errorf("context budget not reported:\n%s", stderr)
	}
	for _, want := range []string{"delivery list", "delivery update"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("%q missing from the commands", want)
		}
	}
	if strings.Contains(stderr, "nothing in the index yet") {
		t.Error("reported an empty index while two skills are trusted")
	}
}

// Singular and plural read correctly. Small thing, but "1 skills" is the kind
// of detail that makes a tool feel unfinished.
func TestBareInvocationUsesSingularForOneSkill(t *testing.T) {
	paths := setupHome(t)
	seedTrusted(t, paths, "gh:acme/skills/only", "only",
		&artifact.Artifact{Kind: artifact.KindSkill, Document: []byte("# only\n")})

	_, stderr, _ := run(t)
	if !strings.Contains(stderr, "1 skill in the index") {
		t.Errorf("singular form not used:\n%s", stderr)
	}
}

// Running the command alone is looking, not acting: it must not create or
// modify any state.
func TestBareInvocationWritesNothing(t *testing.T) {
	paths := setupHome(t)

	if _, _, code := run(t); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	if _, err := os.Stat(paths.TrustFile); err == nil {
		t.Error("the bare invocation created the trust file")
	}
	if _, err := os.Stat(paths.CacheDir); err == nil {
		t.Error("the bare invocation created the cache directory")
	}
	anchorDir := os.Getenv("DELIVERY_CLAUDE_HOME")
	if entries, err := os.ReadDir(anchorDir); err == nil && len(entries) > 0 {
		t.Errorf("the bare invocation wrote into the tool directory: %d entries", len(entries))
	}
}

// Unreadable state degrades into the getting-started screen rather than into an
// error: someone running the bare command is trying to get their bearings.
func TestBareInvocationSurvivesUnreadableState(t *testing.T) {
	paths := setupHome(t)

	// A trust file with broken content and permissions that Open refuses.
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.TrustFile, []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t)

	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d — a bare invocation must not fail\n%s",
			code, cli.ExitOK, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout contaminated: %q", stdout)
	}
	if !strings.Contains(stderr, "nothing in the index yet") {
		t.Errorf("did not fall back to the getting-started screen:\n%s", stderr)
	}
}

// An unknown command is still an error, and still keeps stdout clean.
func TestUnknownCommandStillFails(t *testing.T) {
	setupHome(t)

	stdout, _, code := run(t, "no-such-command")
	if code == cli.ExitOK {
		t.Fatal("an unknown command succeeded")
	}
	if stdout != "" {
		t.Fatalf("stdout contaminated: %q", stdout)
	}
}
