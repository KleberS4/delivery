package meta_test

import (
	"bytes"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/meta"
	"github.com/kleberS4/delivery/internal/ref"
)

// Domain generator: markdown documents with and without frontmatter,
// with malformed frontmatter, with and without a heading.
func genDocument(t *rapid.T) []byte {
	body := rapid.StringMatching(`(# [A-Za-z ]{1,30}\n)?([A-Za-z0-9 .,\n]{0,200})`).Draw(t, "body")

	switch rapid.IntRange(0, 3).Draw(t, "shape") {
	case 0: // no frontmatter
		return []byte(body)
	case 1: // complete frontmatter
		name := rapid.StringMatching(`[a-z][a-z0-9-]{0,20}`).Draw(t, "name")
		desc := rapid.StringMatching(`[A-Za-z][A-Za-z0-9 ]{0,60}`).Draw(t, "desc")
		return []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body)
	case 2: // unterminated fence: not valid frontmatter
		return []byte("---\nname: something\n" + body)
	default: // empty frontmatter
		return []byte("---\n---\n" + body)
	}
}

// P-03 — Round-trip: the frontmatter split preserves bytes.
//
// The property is true by construction, because the split keeps the raw bytes
// instead of re-serialising from a map. This test guards that decision against
// a future refactor that would undo it.
func TestPropFrontmatterSplitRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc := genDocument(t)

		raw, body, _ := meta.SplitFrontmatter(doc)

		joined := append(append([]byte{}, raw...), body...)
		if !bytes.Equal(joined, doc) {
			t.Fatalf("round-trip did not preserve bytes:\n  original %q\n  got      %q", doc, joined)
		}
	})
}

// Invariant: extraction never returns an empty name or description, whatever
// the document's quality. No skill is rejected for lacking metadata.
func TestPropExtractNeverEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc := genDocument(t)
		r, err := ref.Parse("gh:acme/skills/minha-skill.md")
		if err != nil {
			t.Fatalf("invalid test reference: %v", err)
		}
		m := meta.Extract(doc, r)

		if strings.TrimSpace(m.Name) == "" {
			t.Fatal("empty name")
		}
		if strings.TrimSpace(m.Description) == "" {
			t.Fatal("empty description")
		}
		if len(m.Name) > meta.MaxNameLen {
			t.Fatalf("name of %d characters exceeds the limit", len(m.Name))
		}
	})
}

// Invariant: the normalised name is idempotent and respects the allowed
// character set.
func TestPropNormalizeNameIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.String().Draw(t, "raw")
		once := meta.NormalizeName(in)
		twice := meta.NormalizeName(once)

		if once != twice {
			t.Fatalf("normalisation is not idempotent: %q -> %q -> %q", in, once, twice)
		}
		for _, r := range once {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
				r == '-' || r == '.' || r == '_'
			if !ok {
				t.Fatalf("character outside the allowed set in %q: %q", once, r)
			}
		}
		if strings.HasPrefix(once, "-") || strings.HasSuffix(once, "-") {
			t.Fatalf("normalised name has a hyphen at an edge: %q", once)
		}
	})
}
