package anchor_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/anchor"
	"github.com/kleberS4/delivery/internal/trust"
)

// Domain generator: record sets of varying size, including empty and
// with deliberately colliding short names.
func genRecords(t *rapid.T) []trust.Record {
	n := rapid.IntRange(0, 12).Draw(t, "count")
	out := make([]trust.Record, 0, n)
	for i := 0; i < n; i++ {
		skill := rapid.StringMatching(`[a-z][a-z0-9-]{0,10}`).Draw(t, "skill")
		name := rapid.SampledFrom([]string{"pdf", "docx", skill}).Draw(t, "shortName")
		out = append(out, trust.Record{
			Ref:         "gh:acme/skills/" + skill + "-" + string(rune('a'+i)),
			ShortName:   name,
			Description: rapid.StringMatching(`[A-Za-z ]{1,40}`).Draw(t, "desc"),
			SetHash:     "sha256:deadbeef",
			TrustedAt:   time.Unix(0, 0).UTC(),
		})
	}
	return out
}

// P-05 — Idempotence: rendering the same set produces identical bytes.
func TestPropRenderDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := genRecords(t)

		first, s1 := anchor.Render(records)
		second, s2 := anchor.Render(records)

		if !bytes.Equal(first, second) {
			t.Fatal("rendering is not deterministic for the same set")
		}
		if s1 != s2 {
			t.Fatalf("stats differ: %+v != %+v", s1, s2)
		}

		// Different input order, same set: same result.
		shuffled := make([]trust.Record, len(records))
		for i, r := range records {
			shuffled[len(records)-1-i] = r
		}
		third, _ := anchor.Render(shuffled)
		if !bytes.Equal(first, third) {
			t.Fatal("rendering depended on input order")
		}
	})
}

// P-06 — Invariant: the index holds exactly the trusted set.
func TestPropIndexMatchesTrustedSet(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := genRecords(t)
		doc, stats := anchor.Render(records)
		text := string(doc)

		if stats.Entries != len(records) {
			t.Fatalf("entries = %d, records = %d", stats.Entries, len(records))
		}

		// Every trusted reference shows up.
		for _, r := range records {
			if !strings.Contains(text, r.Ref) {
				t.Fatalf("reference missing from the index: %s", r.Ref)
			}
		}

		// No extra index rows.
		rows := countIndexRows(text)
		if rows != len(records) {
			t.Fatalf("index rows = %d, records = %d", rows, len(records))
		}
	})
}

// P-13 — Invariant: the ambiguity marker is set exactly when a short name
// repeats.
func TestPropAmbiguityMarkerExact(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := genRecords(t)
		doc, _ := anchor.Render(records)
		text := string(doc)

		count := map[string]int{}
		for _, r := range records {
			count[r.ShortName]++
		}
		expectAny := false
		for _, n := range count {
			if n > 1 {
				expectAny = true
			}
		}

		hasMarker := strings.Contains(text, "⚠ duplicated name")
		if hasMarker != expectAny {
			t.Fatalf("ambiguity marker = %v, want %v (names: %v)",
				hasMarker, expectAny, count)
		}
	})
}

// Invariant: the frontmatter description is constant, whatever the trusted
// set. It is the only permanent context cost.
func TestPropFrontmatterDescriptionConstant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := genRecords(t)
		doc, _ := anchor.Render(records)

		header, _, ok := strings.Cut(string(doc), "\n---\n")
		if !ok {
			t.Fatal("anchor has no delimited frontmatter")
		}
		if !strings.Contains(header, "description: "+anchor.AnchorDescription) {
			t.Fatal("frontmatter description drifted from the constant")
		}
	})
}

// Invariant: with an empty set the anchor stays valid and instructive.
func TestEmptySetProducesUsefulAnchor(t *testing.T) {
	doc, stats := anchor.Render(nil)
	text := string(doc)

	if stats.Entries != 0 {
		t.Fatalf("entries = %d, want 0", stats.Entries)
	}
	if !strings.Contains(text, "No skills trusted yet") {
		t.Error("empty anchor does not guide the reader")
	}
	if !strings.Contains(text, "delivery trust") {
		t.Error("empty anchor does not say how to start")
	}
	if !strings.Contains(text, "name: "+anchor.AnchorName) {
		t.Error("empty anchor has no valid frontmatter")
	}
}

func countIndexRows(text string) int {
	_, table, ok := strings.Cut(text, "| name | canonical reference | description |")
	if !ok {
		return 0
	}
	n := 0
	for _, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "|---") {
			continue
		}
		if strings.HasPrefix(line, "| `") {
			n++
		}
	}
	return n
}
