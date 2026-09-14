package atomicfs_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kleberS4/delivery/internal/atomicfs"
)

// P-14 — Invariant: after a failed write, the destination either stays intact
// or holds the complete content. Never partial.
func TestWriteFailureLeavesTargetIntact(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")

	original := []byte("original intact content")
	if err := atomicfs.WriteFile(target, original, 0o600); err != nil {
		t.Fatalf("initial write: %v", err)
	}

	// Make the directory read-only to force the temporary file creation to
	// fail — the write must not touch the existing file.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("could not restrict the directory: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	err := atomicfs.WriteFile(target, []byte("new content that must never appear"), 0o600)
	if err == nil {
		t.Skip("the filesystem allowed the write despite the restricted directory")
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("the original file vanished after the failure: %v", readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("the file was corrupted by the failed write:\n  want %q\n  got  %q",
			original, got)
	}
}

// No temporary survives a successful write.
func TestNoTemporariesLeftBehind(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")

	for i := 0; i < 5; i++ {
		if err := atomicfs.WriteFile(target, []byte("content"), 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("files were left behind in the directory: %v", names)
	}
}

// The requested permission is actually applied.
func TestPermissionIsApplied(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "trust.json")

	if err := atomicfs.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permission = %04o, want 0600", perm)
	}
}

// ReplaceDir swaps contents wholesale, with no visible intermediate state.
func TestReplaceDirIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "content")

	build := func(files map[string]string) func(string) error {
		return func(stage string) error {
			for name, content := range files {
				p := filepath.Join(stage, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					return err
				}
			}
			return nil
		}
	}

	if err := atomicfs.ReplaceDir(target, build(map[string]string{"a.md": "first"})); err != nil {
		t.Fatalf("first build: %v", err)
	}

	// The second build fails midway: the previous content must survive.
	err := atomicfs.ReplaceDir(target, func(stage string) error {
		if err := os.WriteFile(filepath.Join(stage, "b.md"), []byte("partial"), 0o600); err != nil {
			return err
		}
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("a failing build should return an error")
	}

	got, readErr := os.ReadFile(filepath.Join(target, "a.md"))
	if readErr != nil {
		t.Fatalf("previous content lost: %v", readErr)
	}
	if string(got) != "first" {
		t.Fatalf("previous content changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "b.md")); err == nil {
		t.Fatal("partial content from the failed build became visible")
	}
}
