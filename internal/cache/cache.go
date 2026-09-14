// Package cache persists the approved set for every trusted skill.
//
// The cache is written at trust time (Q3=A), which is what lets get work
// offline from the very first use. The directory key derives from the
// reference's canonical form, not from the short name: short names can collide
// and can be changed by the user, while the canonical reference is stable and
// unique.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
)

// SchemaVersion versions the on-disk format. A mismatch invalidates the entry
// rather than trying to interpret it.
const SchemaVersion = 1

const (
	manifestName  = "manifest.json"
	documentName  = "document.md"
	resourcesName = "resources"
	filePerm      = os.FileMode(0o600)
)

// Manifest describes the stored set, kept alongside the content itself so the
// cache's integrity can be checked without consulting the trust file.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Ref           string            `json:"ref"`
	SetHash       string            `json:"setHash"`
	Files         map[string]string `json:"files"`
	StoredAt      time.Time         `json:"storedAt"`
}

// Cache stores skill sets and search indexes on disk.
type Cache struct {
	contentDir string
	indexDir   string
}

// New builds a cache rooted at the content and index directories.
func New(contentDir, indexDir string) *Cache {
	return &Cache{contentDir: contentDir, indexDir: indexDir}
}

// Key derives a stable, readable directory name from the reference.
func Key(r ref.Ref) string {
	canonical := r.Canonical()
	sum := sha256.Sum256([]byte(canonical))
	var b strings.Builder
	for _, c := range canonical {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
			b.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			b.WriteRune(c + 32)
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		slug = "skill"
	}
	return slug + "-" + hex.EncodeToString(sum[:6])
}

// Dir returns the directory where the skill is stored.
func (c *Cache) Dir(r ref.Ref) string { return filepath.Join(c.contentDir, Key(r)) }

// ResourcesDir returns a skill's supporting-files directory.
func (c *Cache) ResourcesDir(r ref.Ref) string {
	return filepath.Join(c.Dir(r), resourcesName)
}

// Put writes the set atomically: it builds in a staging area and only then
// swaps, so a failure midway never leaves the destination half written.
func (c *Cache) Put(r ref.Ref, a *artifact.Artifact, setHash string, files map[string]string) error {
	err := atomicfs.ReplaceDir(c.Dir(r), func(stage string) error {
		man := Manifest{
			SchemaVersion: SchemaVersion,
			Ref:           r.Canonical(),
			SetHash:       setHash,
			Files:         files,
			StoredAt:      time.Now().UTC(),
		}
		raw, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, manifestName), append(raw, '\n'), filePerm); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, documentName), a.Document, filePerm); err != nil {
			return err
		}
		for _, res := range a.Resources {
			dest, err := safeJoin(filepath.Join(stage, resourcesName), res.RelPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), atomicfs.DirPerm); err != nil {
				return err
			}
			if err := os.WriteFile(dest, res.Content, filePerm); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on the cache directory", "writing the skill cache")
	}
	return nil
}

// Get returns the stored set. The second return is false when there is no
// usable entry — missing, schema mismatch, or incomplete. In those cases the
// caller re-resolves from the source.
func (c *Cache) Get(r ref.Ref) (*artifact.Artifact, bool, error) {
	dir := c.Dir(r)

	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, false, nil
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil || man.SchemaVersion != SchemaVersion {
		return nil, false, nil
	}

	doc, err := os.ReadFile(filepath.Join(dir, documentName))
	if err != nil {
		return nil, false, nil
	}

	a := &artifact.Artifact{Kind: artifact.KindSkill, Document: doc}

	resRoot := filepath.Join(dir, resourcesName)
	if _, err := os.Stat(resRoot); err == nil {
		walkErr := filepath.WalkDir(resRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(resRoot, p)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			a.Resources = append(a.Resources, artifact.Resource{
				RelPath: filepath.ToSlash(rel),
				Content: content,
			})
			return nil
		})
		if walkErr != nil {
			return nil, false, nil
		}
	}

	a.Normalize()
	return a, true, nil
}

// Evict removes a skill's cache entry.
func (c *Cache) Evict(r ref.Ref) error {
	if err := os.RemoveAll(c.Dir(r)); err != nil {
		return errs.Wrap(err, errs.ClassState, "check cache permissions",
			"removing the skill cache")
	}
	return nil
}

// safeJoin guarantees the resulting path cannot escape the root, whatever the
// source declared.
func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errs.State("report this skill to its origin",
			"resource path escapes the destination directory: %q", rel)
	}
	joined := filepath.Join(root, clean)
	if !strings.HasPrefix(joined, filepath.Clean(root)+string(filepath.Separator)) {
		return "", errs.State("report this skill to its origin",
			"resource path escapes the destination directory: %q", rel)
	}
	return joined, nil
}
