package tool_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/tool"
)

// Where each tool actually reads its skills from. Getting this wrong writes a
// well-formed skill somewhere nothing looks, which fails silently: install
// reports success and the skill never appears.
func TestEachToolResolvesToItsOwnSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(tool.EnvClaudeHome, "")
	t.Setenv(tool.EnvCodexHome, "")

	cases := []struct {
		tool string
		want string
	}{
		{"claude", filepath.Join(home, ".claude", "skills")},
		{"claude-code", filepath.Join(home, ".claude", "skills")},
		{"", filepath.Join(home, ".claude", "skills")},
		// Codex reads personal skills from ~/.agents/skills, not from
		// ~/.codex: CODEX_HOME holds config and credentials, never skills.
		{"codex", filepath.Join(home, ".agents", "skills")},
	}

	for _, c := range cases {
		a, err := tool.ByName(c.tool)
		if err != nil {
			t.Fatalf("ByName(%q): %v", c.tool, err)
		}
		if got := a.SkillsDir(); got != c.want {
			t.Errorf("ByName(%q).SkillsDir() = %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestCodexIsNotResolvedUnderCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(tool.EnvCodexHome, "")

	a, err := tool.ByName("codex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.SkillsDir(), ".codex") {
		t.Errorf("codex skills resolved under .codex (%q); Codex does not read skills from there",
			a.SkillsDir())
	}
}

func TestSupportedNamesAllResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	names := tool.Supported()
	if len(names) == 0 {
		t.Fatal("Supported() is empty")
	}
	for _, n := range names {
		a, err := tool.ByName(n)
		if err != nil {
			t.Errorf("%q is advertised by Supported() but does not resolve: %v", n, err)
			continue
		}
		if a.Name() != n {
			t.Errorf("ByName(%q).Name() = %q; the flag value and the adapter disagree", n, a.Name())
		}
	}
}

func TestUnknownToolIsAUsageErrorNamingTheAlternatives(t *testing.T) {
	_, err := tool.ByName("emacs")
	if err == nil {
		t.Fatal("an unknown tool resolved")
	}

	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not classified: %T", err)
	}
	if e.Class != errs.ClassUsage {
		t.Errorf("class = %v, want %v", e.Class, errs.ClassUsage)
	}
	// The message has to carry the way out, because it reaches an agent that
	// cannot guess the accepted values.
	for _, n := range tool.Supported() {
		if !strings.Contains(e.Action, n) {
			t.Errorf("action %q does not mention the supported tool %q", e.Action, n)
		}
	}
}

// Every adapter has to survive the same round trip, or install and uninstall
// behave differently depending on the target tool.
func TestWriteReadRemoveRoundTripPerTool(t *testing.T) {
	for _, name := range tool.Supported() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(tool.EnvClaudeHome, root)
			t.Setenv(tool.EnvCodexHome, root)

			a, err := tool.ByName(name)
			if err != nil {
				t.Fatal(err)
			}

			body := []byte("---\nname: demo\n---\n\nbody\n")
			if err := a.WriteSkill("demo", body); err != nil {
				t.Fatalf("WriteSkill: %v", err)
			}

			got, err := a.ReadSkill("demo")
			if err != nil {
				t.Fatalf("ReadSkill: %v", err)
			}
			// Adapters must not reformat: the bytes written are the bytes read.
			if string(got) != string(body) {
				t.Errorf("round trip altered the content:\n got %q\nwant %q", got, body)
			}

			if err := a.RemoveSkill("demo"); err != nil {
				t.Fatalf("RemoveSkill: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(a.SkillPath("demo"))); !os.IsNotExist(err) {
				t.Error("RemoveSkill left the skill's directory behind")
			}
		})
	}
}

// A missing skill is ClassNotFound, not a state error: uninstall and read have
// to tell "you never installed this" from "your disk is broken".
func TestReadingAMissingSkillIsNotFound(t *testing.T) {
	t.Setenv(tool.EnvClaudeHome, t.TempDir())

	a, err := tool.ByName("claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadSkill("absent"); err == nil {
		t.Fatal("reading a skill that was never installed succeeded")
	} else {
		var e *errs.Error
		if !errors.As(err, &e) || e.Class != errs.ClassNotFound {
			t.Errorf("class = %v, want %v", e.Class, errs.ClassNotFound)
		}
	}
}

// The anchor is the one skill delivery installs by itself, so its path has to
// sit inside the tool's own skills directory or the tool will never load it.
func TestAnchorPathSitsInsideTheSkillsDirectory(t *testing.T) {
	for _, name := range tool.Supported() {
		root := t.TempDir()
		t.Setenv(tool.EnvClaudeHome, root)
		t.Setenv(tool.EnvCodexHome, root)

		a, err := tool.ByName(name)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(a.SkillsDir(), tool.AnchorSkillName, "SKILL.md")
		if a.AnchorPath() != want {
			t.Errorf("%s: AnchorPath() = %q, want %q", name, a.AnchorPath(), want)
		}
	}
}

// Writing a skill must not leave it readable by other users: the anchor's index
// lists everything the person trusted.
func TestWrittenSkillIsPrivate(t *testing.T) {
	t.Setenv(tool.EnvClaudeHome, t.TempDir())

	a, err := tool.ByName("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteSkill("demo", []byte("x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(a.SkillPath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("skill written with mode %04o; group and others must have no access", perm)
	}
}

// A skill's supporting files go beside its SKILL.md, reproducing the published
// layout. Without that, a relative reference in the body — references/api.md —
// points at nothing once the skill is installed.
func TestResourcesLandBesideTheSkill(t *testing.T) {
	for _, name := range tool.Supported() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(tool.EnvClaudeHome, root)
			t.Setenv(tool.EnvCodexHome, root)

			a, err := tool.ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.WriteSkill("demo", []byte("body")); err != nil {
				t.Fatal(err)
			}
			if err := a.WriteResource("demo", "references/api.md", []byte("ref")); err != nil {
				t.Fatalf("WriteResource: %v", err)
			}

			dir := filepath.Dir(a.SkillPath("demo"))
			got, err := os.ReadFile(filepath.Join(dir, "references", "api.md"))
			if err != nil {
				t.Fatalf("resource not written beside the skill: %v", err)
			}
			if string(got) != "ref" {
				t.Errorf("content = %q, want %q", got, "ref")
			}
		})
	}
}

// The fetch layer rejects path escapes already. This is the last point before
// bytes land in the user's tool directory, so it does not take that on trust.
func TestResourcePathEscapesAreRefused(t *testing.T) {
	root := t.TempDir()
	t.Setenv(tool.EnvClaudeHome, root)

	a, err := tool.ByName("claude")
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"../escaped.md",
		"../../etc/cron.d/evil",
		"references/../../escaped.md",
		"/etc/passwd",
		"",
		"a\x00b",
		`..\windows`,
	} {
		err := a.WriteResource("demo", bad, []byte("payload"))
		if err == nil {
			t.Errorf("WriteResource accepted an escaping path: %q", bad)
			continue
		}
		var e *errs.Error
		if !errors.As(err, &e) || e.Class != errs.ClassIntegrityMismatch {
			t.Errorf("%q: class = %v, want %v", bad, e.Class, errs.ClassIntegrityMismatch)
		}
	}

	// Nothing may have been created outside the skill's directory.
	if _, err := os.Stat(filepath.Join(root, "escaped.md")); !os.IsNotExist(err) {
		t.Error("a refused resource was written anyway")
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "escaped.md")); !os.IsNotExist(err) {
		t.Error("a refused resource escaped into the skills directory")
	}
}

// Nested paths keep their shape: a skill shipping scripts/lib/util.py must not
// end up with the file flattened into the skill's root.
func TestNestedResourcePathsArePreserved(t *testing.T) {
	root := t.TempDir()
	t.Setenv(tool.EnvClaudeHome, root)

	a, _ := tool.ByName("claude")
	if err := a.WriteResource("demo", "scripts/lib/util.py", []byte("x")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(a.SkillPath("demo"))
	if _, err := os.Stat(filepath.Join(dir, "scripts", "lib", "util.py")); err != nil {
		t.Errorf("nested path not preserved: %v", err)
	}
}
