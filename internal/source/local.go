package source

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
)

// LocalResolver fetches skills from the local filesystem.
//
// It exists so you can develop your own skills before publishing them. It does
// not take part in search: there is no catalogue to query.
type LocalResolver struct {
	Limits Limits
}

// NewLocalResolver builds the local resolver.
func NewLocalResolver(limits Limits) *LocalResolver { return &LocalResolver{Limits: limits} }

// Resolve reads the skill from disk.
//
// Containment is validated against the REAL path, after resolving symlinks.
// Validating only the textual form would miss a symlink inside the directory
// pointing outside it.
func (l *LocalResolver) Resolve(_ context.Context, r ref.Ref) (*artifact.Artifact, error) {
	if r.Kind != ref.KindLocal {
		return nil, errs.Usage("use a local path", "unexpected reference kind")
	}

	root, err := filepath.EvalSymlinks(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.NotFound("check the path", "path not found: %s", r.Path)
		}
		return nil, errs.Wrap(err, errs.ClassState, "check the path and its permissions",
			"resolving %s", r.Path)
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassState, "check the path", "reading %s", root)
	}

	if !info.IsDir() {
		doc, err := l.readFile(root)
		if err != nil {
			return nil, err
		}
		return &artifact.Artifact{Kind: artifact.KindSkill, Document: doc}, nil
	}
	return l.readDir(root)
}

func (l *LocalResolver) readFile(p string) ([]byte, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassState, "check the path", "reading %s", p)
	}
	if info.Size() > l.Limits.MaxDocument {
		return nil, errs.Usage(actionTooLarge, msgDocTooLarge, l.Limits.MaxDocument)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassState, "check permissions", "reading %s", p)
	}
	if len(b) == 0 {
		return nil, errs.NotFound("check the path", "empty document: %s", p)
	}
	return b, nil
}

func (l *LocalResolver) readDir(root string) (*artifact.Artifact, error) {
	files := map[string][]byte{}
	var total int64

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// Containment validated against each file's real path: a symlink inside
		// the directory must not drag in content from outside.
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil // broken symlink: ignored
		}
		if !strings.HasPrefix(real, root+string(filepath.Separator)) && real != root {
			return errs.Usage(actionReportSkill,
				"the directory contains a path that escapes it: %q", p)
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > l.Limits.MaxResourcesTotal+l.Limits.MaxDocument {
			return errs.Usage(actionTooManyRes,
				"the directory exceeds %d bytes", l.Limits.MaxResourcesTotal+l.Limits.MaxDocument)
		}
		if len(files) >= l.Limits.MaxResourceCount+1 {
			return errs.Usage(actionTooManyRes,
				"the directory has more than %d files", l.Limits.MaxResourceCount+1)
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		if _, ok := errs.ClassOf(err); ok {
			return nil, err
		}
		return nil, errs.Wrap(err, errs.ClassState, "check the path and its permissions",
			"reading directory %s", root)
	}

	if len(files) == 0 {
		return nil, errs.NotFound("check the path", "empty directory: %s", root)
	}
	return buildFromFiles(files, l.Limits)
}
