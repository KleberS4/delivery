package ref_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/ref"
)

// P-01 — Round-trip: parsing the canonical form returns the original reference.
func TestPropCanonicalRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := genAnyRaw(t)

		r, err := ref.Parse(raw)
		if err != nil {
			t.Skipf("generated reference was rejected: %v", err)
		}

		again, err := ref.Parse(r.String())
		if err != nil {
			t.Fatalf("canonical form %q does not parse back: %v", r.String(), err)
		}
		if !again.Equal(r) {
			t.Fatalf("round-trip mismatch:\n  original %#v\n  reparsed %#v", r, again)
		}
		if again.Canonical() != r.Canonical() {
			t.Fatalf("canonical form unstable: %q != %q", again.Canonical(), r.Canonical())
		}
	})
}

// P-02 — Invariant: typing variations that denote the same skill produce the
// same canonical form.
func TestPropCanonicalStableUnderTypingVariation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := genGitHubRaw(t)

		base, err := ref.Parse(raw)
		if err != nil {
			t.Skipf("generated reference was rejected: %v", err)
		}

		// Variations that do not change meaning: duplicated slashes, a trailing
		// slash, "." segments, and upper case in owner/repo.
		variants := []string{
			strings.Replace(raw, "/", "//", 1),
			insertBeforeGitRef(raw, "/"),
			insertBeforeGitRef(raw, "/./"),
			upperOwnerRepo(raw),
		}

		for _, v := range variants {
			got, err := ref.Parse(v)
			if err != nil {
				t.Fatalf("variation %q was rejected: %v", v, err)
			}
			if got.Canonical() != base.Canonical() {
				t.Fatalf("variation %q produced a different canonical form:\n  %q\n  %q",
					v, got.Canonical(), base.Canonical())
			}
		}
	})
}

func insertBeforeGitRef(raw, suffix string) string {
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		return raw[:i] + suffix + raw[i:]
	}
	return raw + suffix
}

func upperOwnerRepo(raw string) string {
	body := strings.TrimPrefix(raw, "gh:")
	parts := strings.SplitN(body, "/", 3)
	if len(parts) < 3 {
		return raw
	}
	return "gh:" + strings.ToUpper(parts[0]) + "/" + strings.ToUpper(parts[1]) + "/" + parts[2]
}

// P-12 — Security invariant: every reference with a path escape or an invalid
// scheme is rejected.
func TestPropRejectsUnsafeReferences(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		owner := genSegment.Draw(t, "owner")
		repo := genSegment.Draw(t, "repo")
		seg := genSegment.Draw(t, "seg")

		// Note: local paths (/etc/..., ../..., ~/...) are NOT in this list.
		// From U2 on they are valid references, and containment moved to
		// resolve time, against the real path and after resolving symlinks
		// — validating only the textual form would miss a symlink
		// pointing outside. The guarantee is covered by
		// TestLocalDirectoryRejectsSymlinkEscape.
		unsafe := []string{
			"gh:" + owner + "/" + repo + "/../" + seg,
			"gh:" + owner + "/" + repo + "/" + seg + "/..",
			"gh:" + owner + "/" + repo + "/" + seg + "/../../etc/passwd",
			"http://exemplo.com/" + seg + ".md",
			"https://exemplo.com/../" + seg + ".md",
			owner + "/" + repo + "/../" + seg,
		}

		for _, u := range unsafe {
			if _, err := ref.Parse(u); err == nil {
				t.Fatalf("unsafe reference was accepted: %q", u)
			}
		}
	})
}

// P-21 — Round-trip: the bare form is the registry identifier, and its
// canonical form parses back as one.
func TestPropBareFormIsRegistry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		owner := genSegment.Draw(t, "owner")
		repo := genSegment.Draw(t, "repo")
		id := genSegment.Draw(t, "id")
		bare := owner + "/" + repo + "/" + id

		r, err := ref.Parse(bare)
		if err != nil {
			t.Fatalf("bare form %q should be a registry identifier: %v", bare, err)
		}
		if r.Kind != ref.KindRegistry {
			t.Fatalf("kind = %v, want registry", r.Kind)
		}
		if r.SkillID != id {
			t.Fatalf("skillID = %q, want %q", r.SkillID, id)
		}

		again, err := ref.Parse(r.String())
		if err != nil || !again.Equal(r) {
			t.Fatalf("registry round-trip failed: %v", err)
		}
	})
}

// A form with more than three segments is not a registry identifier, and the
// message points at the explicit gh: form.
func TestBareFormWithExtraSegmentsIsRejected(t *testing.T) {
	_, err := ref.Parse("owner/repo/caminho/profundo")
	if err == nil {
		t.Fatal("a four-segment form should be rejected")
	}
	if !strings.Contains(err.Error(), "not recognised") {
		t.Fatalf("unexpected message: %v", err)
	}
}

// P-22 — Local paths produce an absolute, normalised reference with a stable
// round-trip.
func TestPropLocalRefIsAbsoluteAndStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seg := genSegment.Draw(t, "seg")

		for _, raw := range []string{"./" + seg, "/tmp/" + seg, "../" + seg} {
			r, err := ref.Parse(raw)
			if err != nil {
				t.Fatalf("local path %q was rejected: %v", raw, err)
			}
			if r.Kind != ref.KindLocal {
				t.Fatalf("kind of %q = %v, want local", raw, r.Kind)
			}
			if !strings.HasPrefix(r.Path, "/") {
				t.Fatalf("local path is not absolute: %q", r.Path)
			}
			again, err := ref.Parse(r.String())
			if err != nil || !again.Equal(r) {
				t.Fatalf("local round-trip failed for %q: %v", raw, err)
			}
		}
	})
}
