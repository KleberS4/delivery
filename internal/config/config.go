// Package config resolves the paths of local state and checks permissions.
//
// The trust file's permission check happens on load, before any useful read. A
// trust file writable by others is a direct escalation vector: whoever can
// write it can grant trust on your behalf.
package config

import (
	"os"
	"path/filepath"

	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
)

// EnvHome overrides the state root. Used in tests and in unconventional
// installations.
const EnvHome = "DELIVERY_HOME"

// TrustFilePerm is the permission required of the trust file.
const TrustFilePerm os.FileMode = 0o600

// Paths groups the local state locations.
type Paths struct {
	Root       string
	ConfigFile string
	TrustFile  string
	CacheDir   string
	ContentDir string
	IndexDir   string
	TmpDir     string
}

// Resolve determines the paths following operating-system conventions.
func Resolve() (Paths, error) {
	root := os.Getenv(EnvHome)
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, errs.Wrap(err, errs.ClassState,
				"set "+EnvHome+" to a writable directory",
				"could not determine the user configuration directory")
		}
		root = filepath.Join(base, "delivery")
	}
	return pathsFrom(root), nil
}

// PathsFor builds the paths from an explicit root.
func PathsFor(root string) Paths { return pathsFrom(root) }

func pathsFrom(root string) Paths {
	cache := filepath.Join(root, "cache")
	return Paths{
		Root:       root,
		ConfigFile: filepath.Join(root, "config.json"),
		TrustFile:  filepath.Join(root, "trust.json"),
		CacheDir:   cache,
		ContentDir: filepath.Join(cache, "content"),
		IndexDir:   filepath.Join(cache, "index"),
		TmpDir:     filepath.Join(root, "tmp"),
	}
}

// EnsureDirs creates the directory structure. It is idempotent.
func EnsureDirs(p Paths) error {
	for _, d := range []string{p.Root, p.CacheDir, p.ContentDir, p.IndexDir, p.TmpDir} {
		if err := atomicfs.MkdirAll(d); err != nil {
			return errs.Wrap(err, errs.ClassState,
				"check that the path is writable", "creating %s", d)
		}
	}
	return nil
}

// CheckWritable verifies write access before any modification, so a permission
// failure leaves the system exactly as it was.
func CheckWritable(p Paths) error {
	parent := filepath.Dir(p.Root)
	if _, err := os.Stat(p.Root); err == nil {
		parent = p.Root
	}
	f, err := os.CreateTemp(parent, ".delivery-check-*")
	if err != nil {
		return errs.Wrap(err, errs.ClassState,
			"grant write permission on "+parent, "%s is not writable", parent)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// CheckPermissions refuses to operate when the trust file has permissions
// broader than required.
func CheckPermissions(p Paths) error {
	info, err := os.Stat(p.TrustFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // does not exist yet: it will be created with the right mode
		}
		return errs.Wrap(err, errs.ClassState,
			"check the state path", "reading %s", p.TrustFile)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return errs.State(
			"run: chmod 600 "+p.TrustFile,
			"trust file has permissions that are too open (%04o): "+
				"another user could grant trust on your behalf", mode)
	}
	return nil
}
