package integrity_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/integrity"
)

// Domain generators.

var (
	genRelPath = rapid.StringMatching(`[a-z][a-z0-9_-]{0,8}(/[a-z][a-z0-9_-]{0,8}){0,2}\.(md|txt|json)`)
	genContent = rapid.SliceOfN(rapid.Byte(), 0, 512)
)

func genArtifact(t *rapid.T) *artifact.Artifact {
	n := rapid.IntRange(0, 6).Draw(t, "resourceCount")
	seen := map[string]bool{artifact.DocumentKey: true}
	res := make([]artifact.Resource, 0, n)
	for i := 0; i < n; i++ {
		p := genRelPath.Draw(t, "relPath")
		if seen[p] {
			continue
		}
		seen[p] = true
		res = append(res, artifact.Resource{RelPath: p, Content: genContent.Draw(t, "resContent")})
	}
	a := &artifact.Artifact{
		Kind:      artifact.KindSkill,
		Document:  genContent.Draw(t, "document"),
		Resources: res,
	}
	return a
}

// P-07 — Invariant: verifying content against its own hash always succeeds.
func TestPropVerifyOwnHash(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		data := genContent.Draw(t, "data")
		if err := integrity.Verify(data, integrity.HashBytes(data)); err != nil {
			t.Fatalf("verifying own hash failed: %v", err)
		}
	})
}

// P-09 — Invariant: the set hash does not depend on presentation order.
func TestPropSetHashOrderIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genArtifact(t)
		if len(a.Resources) < 2 {
			t.Skip("needs at least two resources")
		}

		h1, _ := integrity.HashSet(a)

		// Same set, reversed order.
		reversed := make([]artifact.Resource, len(a.Resources))
		for i, r := range a.Resources {
			reversed[len(a.Resources)-1-i] = r
		}
		b := &artifact.Artifact{Kind: a.Kind, Document: a.Document, Resources: reversed}
		h2, _ := integrity.HashSet(b)

		if h1 != h2 {
			t.Fatalf("hash depended on order: %s != %s", h1, h2)
		}
	})
}

// P-08 — Invariant: any change to a file changes the set hash.
func TestPropSetHashSensitiveToContentChange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genArtifact(t)
		before, _ := integrity.HashSet(a)

		mutated := &artifact.Artifact{
			Kind:      a.Kind,
			Document:  append(append([]byte{}, a.Document...), 'x'),
			Resources: a.Resources,
		}
		if after, _ := integrity.HashSet(mutated); after == before {
			t.Fatal("changing the document did not change the set hash")
		}
	})
}

// P-08 (extension) — adding a file changes the hash.
func TestPropSetHashSensitiveToAddedFile(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genArtifact(t)
		before, _ := integrity.HashSet(a)

		extra := artifact.Resource{RelPath: "zzz-new-resource.md", Content: []byte("content")}
		for _, r := range a.Resources {
			if r.RelPath == extra.RelPath {
				t.Skip("path collision in the generator")
			}
		}
		b := &artifact.Artifact{
			Kind:      a.Kind,
			Document:  a.Document,
			Resources: append(append([]artifact.Resource{}, a.Resources...), extra),
		}
		if after, _ := integrity.HashSet(b); after == before {
			t.Fatal("adding a file did not change the set hash")
		}
	})
}

// P-08 (extension) — renaming a file changes the hash, because the path is
// folded into the composition alongside the content.
func TestPropSetHashSensitiveToRename(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genArtifact(t)
		if len(a.Resources) == 0 {
			t.Skip("needs at least one resource")
		}
		before, _ := integrity.HashSet(a)

		renamed := make([]artifact.Resource, len(a.Resources))
		copy(renamed, a.Resources)
		renamed[0].RelPath = "renamed-" + renamed[0].RelPath

		b := &artifact.Artifact{Kind: a.Kind, Document: a.Document, Resources: renamed}
		if after, _ := integrity.HashSet(b); after == before {
			t.Fatal("renaming did not change the set hash")
		}
	})
}

// Changed correctly identifies modifications, additions and removals.
func TestChangedClassification(t *testing.T) {
	before := map[string]string{"a.md": "h1", "b.md": "h2", "c.md": "h3"}
	after := map[string]string{"a.md": "h1", "b.md": "OTHER", "d.md": "h4"}

	mod, add, rem := integrity.Changed(before, after)

	if len(mod) != 1 || mod[0] != "b.md" {
		t.Errorf("modified = %v, want [b.md]", mod)
	}
	if len(add) != 1 || add[0] != "d.md" {
		t.Errorf("added = %v, want [d.md]", add)
	}
	if len(rem) != 1 || rem[0] != "c.md" {
		t.Errorf("removed = %v, want [c.md]", rem)
	}
}
