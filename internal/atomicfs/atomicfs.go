// Package atomicfs implements atomic file writes.
//
// Pattern P6: write to a temporary file inside the destination directory,
// sync, then rename over the target. The temporary lives in the same directory
// so source and destination are guaranteed to be on the same filesystem — a
// cross-filesystem rename degrades into a copy and loses atomicity with no
// signal at all.
//
// On any error path the temporary is removed and the destination keeps its
// previous, intact content.
package atomicfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// DirPerm is the permission applied to created directories: user-only access.
const DirPerm os.FileMode = 0o700

// WriteFile writes data to path atomically.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".delivery-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Temporary removed on every error path.
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("setting temporary file permissions: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}
	return nil
}

// MkdirAll creates a directory tree with user-only permissions.
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, DirPerm); err != nil {
		return fmt.Errorf("creating directory %s: %w", path, err)
	}
	return nil
}

// ReplaceDir atomically replaces a directory's contents. It builds the new
// version in a sibling staging directory and only then swaps, so a failure
// midway never leaves the destination half written.
func ReplaceDir(path string, build func(stagingDir string) error) (err error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, DirPerm); err != nil {
		return fmt.Errorf("creating directory %s: %w", parent, err)
	}

	staging, err := os.MkdirTemp(parent, ".delivery-stage-*")
	if err != nil {
		return fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(staging)
		}
	}()

	if err = os.Chmod(staging, DirPerm); err != nil {
		return fmt.Errorf("setting staging directory permissions: %w", err)
	}
	if err = build(staging); err != nil {
		return err
	}

	// Move the old one aside before putting the new one in place, so the
	// window in which the destination does not exist is as short as possible.
	backup := ""
	if _, statErr := os.Stat(path); statErr == nil {
		backup = path + ".delivery-old"
		os.RemoveAll(backup)
		if err = os.Rename(path, backup); err != nil {
			return fmt.Errorf("moving previous directory aside: %w", err)
		}
	}

	if err = os.Rename(staging, path); err != nil {
		if backup != "" {
			os.Rename(backup, path) // best-effort restore
		}
		return fmt.Errorf("putting new directory in place: %w", err)
	}
	if backup != "" {
		os.RemoveAll(backup)
	}
	return nil
}
