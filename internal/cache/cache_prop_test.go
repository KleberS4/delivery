package cache_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cache"
	"github.com/kleberS4/delivery/internal/integrity"
	"github.com/kleberS4/delivery/internal/ref"
)

// Domain generator: sets with varying nesting, the empty set, and
// contents of diverse sizes.
func genArtifact(t *rapid.T) *artifact.Artifact {
	n := rapid.IntRange(0, 6).Draw(t, "resourceCount")
	seen := map[string]bool{}
	res := make([]artifact.Resource, 0, n)
	for i := 0; i < n; i++ {
		p := rapid.StringMatching(`[a-z]{1,6}(/[a-z]{1,6}){0,2}\.(md|txt|json)`).Draw(t, "relPath")
		if seen[p] {
			continue
		}
		seen[p] = true
		res = append(res, artifact.Resource{
			RelPath: p,
			Content: rapid.SliceOfN(rapid.Byte(), 0, 256).Draw(t, "content"),
		})
	}
	a := &artifact.Artifact{
		Kind:      artifact.KindSkill,
		Document:  rapid.SliceOfN(rapid.Byte(), 1, 512).Draw(t, "document"),
		Resources: res,
	}
	a.Normalize()
	return a
}

func genRef(t *rapid.T) ref.Ref {
	raw := "gh:" +
		rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "owner") + "/" +
		rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "repo") + "/" +
		rapid.StringMatching(`[a-z][a-z0-9-]{0,10}`).Draw(t, "skill")
	r, err := ref.Parse(raw)
	if err != nil {
		t.Fatalf("generated reference is invalid %q: %v", raw, err)
	}
	return r
}

// P-10 — Round-trip: writing then reading back preserves document, resources
// and paths.
func TestPropCacheRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "delivery-cache-*")
		if err != nil {
			t.Fatalf("creating temporary directory: %v", err)
		}
		defer os.RemoveAll(dir)

		c := cache.New(dir, filepath.Join(dir, "index"))
		r := genRef(t)
		a := genArtifact(t)
		setHash, files := integrity.HashSet(a)

		if err := c.Put(r, a, setHash, files); err != nil {
			t.Fatalf("writing cache: %v", err)
		}

		got, ok, err := c.Get(r)
		if err != nil {
			t.Fatalf("reading cache: %v", err)
		}
		if !ok {
			t.Fatal("the written entry was not found")
		}

		if !bytes.Equal(got.Document, a.Document) {
			t.Fatal("document differs after the round-trip")
		}
		if len(got.Resources) != len(a.Resources) {
			t.Fatalf("resources = %d, want %d", len(got.Resources), len(a.Resources))
		}
		for i := range a.Resources {
			if got.Resources[i].RelPath != a.Resources[i].RelPath {
				t.Fatalf("path differs: %q != %q",
					got.Resources[i].RelPath, a.Resources[i].RelPath)
			}
			if !bytes.Equal(got.Resources[i].Content, a.Resources[i].Content) {
				t.Fatalf("content differs at %q", a.Resources[i].RelPath)
			}
		}

		// The hash of the set read back equals the one written: this is the
		// equality get checks before handing anything over.
		if again, _ := integrity.HashSet(got); again != setHash {
			t.Fatalf("hash differs after the round-trip: %s != %s", again, setHash)
		}
	})
}

// Invariant: the cache key derives from the canonical reference and is stable.
func TestPropCacheKeyStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genRef(t)
		if cache.Key(r) != cache.Key(r) {
			t.Fatal("cache key is unstable between calls")
		}

		other := genRef(t)
		if r.Canonical() != other.Canonical() && cache.Key(r) == cache.Key(other) {
			t.Fatalf("distinct references collided on the key: %s", cache.Key(r))
		}
	})
}

// A missing entry reports "not found" rather than an error.
func TestMissingEntryIsNotAnError(t *testing.T) {
	dir, err := os.MkdirTemp("", "delivery-cache-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := ref.Parse("gh:acme/skills/ausente")
	if err != nil {
		t.Fatal(err)
	}

	_, ok, err := cache.New(dir, filepath.Join(dir, "index")).Get(r)
	if err != nil {
		t.Fatalf("a missing entry returned an error: %v", err)
	}
	if ok {
		t.Fatal("a missing entry was reported as present")
	}
}
