package footer_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/footer"
)

// Domain generators.

func genManagement(t *rapid.T) footer.Management {
	m := footer.Management{
		Source:      "gh:" + rapid.StringMatching(`[a-z]{1,8}/[a-z]{1,8}/[a-z][a-z0-9-]{0,10}`).Draw(t, "source"),
		Hash:        "sha256:" + rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "hash"),
		InstalledAt: time.Unix(int64(rapid.IntRange(0, 2_000_000_000).Draw(t, "ts")), 0).UTC(),
	}
	if rapid.Bool().Draw(t, "hasVersion") {
		m.Version = rapid.StringMatching(`v[0-9]\.[0-9]\.[0-9]`).Draw(t, "version")
	}
	return m
}

func genBody(t *rapid.T) []byte {
	return []byte(rapid.StringMatching(
		`(---\n(name: [a-z-]{1,20}\n)?---\n)?# [A-Za-z ]{1,30}\n\n[A-Za-z0-9 .,\n]{0,200}`,
	).Draw(t, "body"))
}

// P-16 — Round-trip: splitting the block returns exactly the metadata and body.
func TestPropFooterRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := genBody(t)
		m := genManagement(t)

		doc := append(append([]byte{}, body...), footer.Render(m)...)

		got, gotBody, ok := footer.Parse(doc)
		if !ok {
			t.Fatal("the generated block was not recognised")
		}
		if !bytes.Equal(gotBody, body) {
			t.Fatalf("body differs:\n  want %q\n  got  %q", body, gotBody)
		}
		if got.Source != m.Source || got.Hash != m.Hash || got.Version != m.Version {
			t.Fatalf("metadata differs:\n  want %+v\n  got  %+v", m, got)
		}
		if !got.InstalledAt.Equal(m.InstalledAt) {
			t.Fatalf("date differs: %v != %v", got.InstalledAt, m.InstalledAt)
		}
	})
}

// P-17 — Invariant: a document with no block is never seen as managed.
//
// This is the guard that stops uninstall from deleting a hand-written skill.
func TestPropUnmanagedDocumentIsNeverManaged(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := genBody(t)

		if footer.IsManaged(body) {
			t.Fatalf("a document with no block was seen as managed: %q", body)
		}
		if _, gotBody, ok := footer.Parse(body); ok || !bytes.Equal(gotBody, body) {
			t.Fatal("the split altered a document with no block")
		}
		if !bytes.Equal(footer.Strip(body), body) {
			t.Fatal("Strip altered a document with no block")
		}
	})
}

// P-18 — Idempotence: composing twice does not stack blocks.
func TestPropComposeIsIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := genBody(t)
		m := genManagement(t)

		once := footer.Compose(body, m)
		twice := footer.Compose(once, m)

		if !bytes.Equal(once, twice) {
			t.Fatal("composing twice produced a different result")
		}
		if n := strings.Count(string(twice), "delivery:managed"); n != 2 {
			// two: the opening and closing markers
			t.Fatalf("markers = %d, want 2 (opening and closing)", n)
		}
		if !bytes.Equal(footer.Strip(twice), body) {
			t.Fatal("Strip did not return the original body after double composition")
		}
	})
}

// The block sits at the END of the document, never at the start, so it cannot
// interfere with the frontmatter the target tool parses.
func TestFooterIsAppendedNotPrepended(t *testing.T) {
	body := []byte("---\nname: teste\ndescription: x\n---\n\n# Corpo\n")
	doc := footer.Compose(body, footer.Management{
		Source: "gh:a/b/c", Hash: "sha256:abc", InstalledAt: time.Unix(0, 0).UTC(),
	})

	if !bytes.HasPrefix(doc, []byte("---\nname: teste")) {
		t.Fatal("the frontmatter is no longer the start of the document")
	}
	if !strings.HasSuffix(strings.TrimSpace(string(doc)), "<!-- /delivery:managed -->") {
		t.Fatal("the block is not at the end of the document")
	}
}

// The provenance warning is present.
func TestFooterCarriesProvenanceWarning(t *testing.T) {
	doc := footer.Render(footer.Management{
		Source: "gh:a/b/c", Hash: "sha256:abc", InstalledAt: time.Unix(0, 0).UTC(),
	})
	s := string(doc)

	if !strings.Contains(s, "Provenance") {
		t.Error("block has no provenance section")
	}
	if !strings.Contains(s, "third-party data") {
		t.Error("block lacks the warning that content is data, not instruction")
	}
	if !strings.Contains(s, "delivery update") || !strings.Contains(s, "delivery uninstall") {
		t.Error("block lacks the management commands")
	}
}

// A skill that imitates the block in its own text cannot fool the split,
// because the real block is always last.
func TestForgedBlockInBodyDoesNotWin(t *testing.T) {
	forged := []byte("# Skill\n\n<!-- delivery:managed {\"source\":\"FALSO\"} -->\n" +
		"<!-- /delivery:managed -->\n\ntexto depois\n")
	real := footer.Management{
		Source: "gh:verdadeiro/repo/skill", Hash: "sha256:abc",
		InstalledAt: time.Unix(0, 0).UTC(),
	}

	doc := footer.Compose(forged, real)
	got, _, ok := footer.Parse(doc)

	if !ok {
		t.Fatal("the real block was not recognised")
	}
	if got.Source != real.Source {
		t.Fatalf("the split used the forged block: %q", got.Source)
	}
}
