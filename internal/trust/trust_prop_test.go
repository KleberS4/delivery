package trust_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/trust"
)

// Domain generator: coherent records, including colliding short
// names, which are allowed by design.
func genRecord(t *rapid.T, i int) trust.Record {
	owner := rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "owner")
	repo := rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "repo")
	skill := rapid.StringMatching(`[a-z][a-z0-9-]{0,10}`).Draw(t, "skill")
	name := rapid.SampledFrom([]string{"pdf", "docx", skill}).Draw(t, "shortName")

	return trust.Record{
		Ref:         "gh:" + owner + "/" + repo + "/" + skill + "-" + itoa(i),
		Kind:        "github",
		ShortName:   name,
		Description: rapid.StringMatching(`[A-Za-z ]{1,40}`).Draw(t, "desc"),
		MetaOrigin:  "frontmatter",
		SetHash:     "sha256:" + rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "hash"),
		FileHashes: map[string]string{
			"SKILL.md": "sha256:" + rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "fileHash"),
		},
		SourceKind: "raw",
		TrustedAt:  time.Unix(int64(rapid.IntRange(0, 2_000_000_000).Draw(t, "ts")), 0).UTC(),
	}
}

func genRecordSet(t *rapid.T) []trust.Record {
	n := rapid.IntRange(0, 8).Draw(t, "recordCount")
	out := make([]trust.Record, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, genRecord(t, i))
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// P-04 — Round-trip: serialising and deserialising preserves every field.
func TestPropStoreSerializationRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := genRecordSet(t)

		raw, err := trust.Marshal(records, nil)
		if err != nil {
			t.Fatalf("serialisation failed: %v", err)
		}
		got, _, err := trust.Unmarshal(raw)
		if err != nil {
			t.Fatalf("deserialisation failed: %v", err)
		}

		want := dedupeByRef(records)
		if len(got) != len(want) {
			t.Fatalf("count mismatch: %d != %d", len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("record %d differs:\n  before %+v\n  after  %+v", i, want[i], got[i])
			}
		}
	})
}

// P-11 — Invariant: granting then revoking restores the previous state.
func TestPropTrustThenUntrustRestoresState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "delivery-trust-*")
		if err != nil {
			t.Fatalf("creating temporary directory: %v", err)
		}
		defer os.RemoveAll(dir)
		paths := config.PathsFor(dir)

		store, err := trust.Open(paths)
		if err != nil {
			t.Fatalf("opening the store: %v", err)
		}

		initial := genRecordSet(t)
		for _, r := range initial {
			if err := store.Put(r); err != nil {
				t.Fatalf("writing initial record: %v", err)
			}
		}
		before := store.List()

		extra := genRecord(t, 9999)
		for _, r := range before {
			if r.Ref == extra.Ref {
				t.Skip("reference collision in the generator")
			}
		}

		if err := store.Put(extra); err != nil {
			t.Fatalf("writing extra record: %v", err)
		}
		if store.Len() != len(before)+1 {
			t.Fatalf("count after insert: %d, want %d", store.Len(), len(before)+1)
		}

		if err := store.Delete(extra.Ref); err != nil {
			t.Fatalf("removing extra record: %v", err)
		}

		after := store.List()
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("state not restored:\n  before %d records\n  after  %d records",
				len(before), len(after))
		}
	})
}

// Invariant: the store reloaded from disk equals what was written.
func TestPropStorePersistsAcrossReopen(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "delivery-trust-*")
		if err != nil {
			t.Fatalf("creating temporary directory: %v", err)
		}
		defer os.RemoveAll(dir)
		paths := config.PathsFor(dir)

		store, err := trust.Open(paths)
		if err != nil {
			t.Fatalf("opening: %v", err)
		}
		records := genRecordSet(t)
		for _, r := range records {
			if err := store.Put(r); err != nil {
				t.Fatalf("writing: %v", err)
			}
		}
		want := store.List()

		reopened, err := trust.Open(paths)
		if err != nil {
			t.Fatalf("reopening: %v", err)
		}
		if got := reopened.List(); !reflect.DeepEqual(got, want) {
			t.Fatalf("state differs after reopen: %d != %d records", len(got), len(want))
		}
	})
}

func dedupeByRef(records []trust.Record) []trust.Record {
	byRef := map[string]trust.Record{}
	for _, r := range records {
		byRef[r.Ref] = r
	}
	out := make([]trust.Record, 0, len(byRef))
	for _, r := range byRef {
		out = append(out, r)
	}
	sortByRef(out)
	return out
}

func sortByRef(rs []trust.Record) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Ref < rs[j-1].Ref; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}
