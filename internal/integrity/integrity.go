// Package integrity computes and verifies the hash of a skill set.
//
// This package reports a mismatch; it does not decide what to do about it. The
// decision to fail belongs to the service, and it is always to fail.
//
// The stored value carries the algorithm identifier as a prefix. That allows
// migrating to another algorithm later without invalidating existing records:
// the verifier picks the algorithm from the prefix.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/errs"
)

// Prefix identifies the algorithm inside the stored value.
const Prefix = "sha256:"

// HashBytes computes the hash of a standalone piece of content.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return Prefix + hex.EncodeToString(sum[:])
}

// HashSet computes the set hash along with the per-file hash map.
//
// The composition gives three guarantees, covered by properties P-08 and P-09:
//
//   - It does not depend on presentation order, thanks to the canonical sort
//     by path.
//   - It is sensitive to any change, including a rename, because the path is
//     folded into the composition alongside the hash.
//   - It cannot collide through ambiguous concatenation, because every field
//     is length-prefixed and separated by a byte that cannot occur in a path.
func HashSet(a *artifact.Artifact) (setHash string, files map[string]string) {
	files = make(map[string]string, len(a.Resources)+1)
	files[artifact.DocumentKey] = HashBytes(a.Document)
	for _, r := range a.Resources {
		files[r.RelPath] = HashBytes(r.Content)
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		fh := files[p]
		fmt.Fprintf(h, "%d\x00%s\x00%d\x00%s\x00", len(p), p, len(fh), fh)
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), files
}

// Verify compares standalone content against an expected hash.
func Verify(data []byte, expected string) error {
	got := HashBytes(data)
	if got != expected {
		return errs.Integrity(
			"review the content and re-approve it with: delivery trust",
			"integrity mismatch: expected %s, got %s", short(expected), short(got))
	}
	return nil
}

// VerifySet compares a set against its recorded hash.
func VerifySet(a *artifact.Artifact, expected string, action string) error {
	got, _ := HashSet(a)
	if got != expected {
		return errs.Integrity(action,
			"content differs from what was approved: expected %s, got %s",
			short(expected), short(got))
	}
	return nil
}

// Changed compares two per-file hash maps and reports which paths were
// modified, added or removed. It is what makes a re-approval informative
// instead of merely saying that something changed.
func Changed(before, after map[string]string) (modified, added, removed []string) {
	for p, h := range after {
		old, ok := before[p]
		switch {
		case !ok:
			added = append(added, p)
		case old != h:
			modified = append(modified, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(modified)
	sort.Strings(added)
	sort.Strings(removed)
	return modified, added, removed
}

// short abbreviates a hash for display, keeping the algorithm prefix.
func short(h string) string {
	if !strings.HasPrefix(h, Prefix) {
		return h
	}
	hexPart := strings.TrimPrefix(h, Prefix)
	if len(hexPart) <= 12 {
		return h
	}
	return Prefix + hexPart[:12]
}
