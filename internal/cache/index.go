package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
)

// IndexTTL is how long a search index stays fresh.
//
// Twenty-four hours: the registry is fed by a periodic GitHub crawl, so
// refreshing more often would not yield newer data, and refreshing much less
// often would hide recently published skills.
const IndexTTL = 24 * time.Hour

// IndexSchemaVersion versions the on-disk index format.
const IndexSchemaVersion = 1

// SearchIndex is a search result, stored on disk.
//
// The index is per SEARCH TERM, not a full catalogue: the API offers no bulk
// dump route, and downloading tens of thousands of entries to answer one search
// would contradict the product's whole purpose.
type SearchIndex struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Query         string                `json:"query"`
	FetchedAt     time.Time             `json:"fetchedAt"`
	Entries       []artifact.IndexEntry `json:"entries"`
}

// Fresh reports whether the index is still within its TTL.
func (s SearchIndex) Fresh(now time.Time) bool {
	return now.Sub(s.FetchedAt) < IndexTTL
}

// Age returns how long ago the index was fetched.
func (s SearchIndex) Age(now time.Time) time.Duration { return now.Sub(s.FetchedAt) }

func indexKey(query string) string {
	norm := strings.ToLower(strings.TrimSpace(query))
	sum := sha256.Sum256([]byte(norm))

	var b strings.Builder
	for _, c := range norm {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 32 {
		slug = strings.Trim(slug[:32], "-")
	}
	if slug == "" {
		slug = "search"
	}
	return slug + "-" + hex.EncodeToString(sum[:6]) + ".json"
}

func (c *Cache) indexPath(query string) string {
	return filepath.Join(c.indexDir, indexKey(query))
}

// Index returns the stored index for a term.
//
// A stale index is NOT discarded: it comes back with found true plus its
// freshness, so the caller can serve it with a warning when the network is
// unavailable.
func (c *Cache) Index(query string) (idx SearchIndex, found bool, err error) {
	raw, readErr := os.ReadFile(c.indexPath(query))
	if readErr != nil {
		return SearchIndex{}, false, nil
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return SearchIndex{}, false, nil
	}
	if idx.SchemaVersion != IndexSchemaVersion {
		return SearchIndex{}, false, nil
	}
	return idx, true, nil
}

// PutIndex writes the index atomically.
func (c *Cache) PutIndex(idx SearchIndex) error {
	idx.SchemaVersion = IndexSchemaVersion
	if idx.FetchedAt.IsZero() {
		idx.FetchedAt = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return errs.Wrap(err, errs.ClassState, "check the cache", "serialising the index")
	}
	if err := atomicfs.WriteFile(c.indexPath(idx.Query), append(raw, '\n'), filePerm); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on the cache directory", "writing the search index")
	}
	return nil
}

// ClearIndex removes every stored search index.
func (c *Cache) ClearIndex() error {
	if err := os.RemoveAll(c.indexDir); err != nil {
		return errs.Wrap(err, errs.ClassState, "check cache permissions",
			"clearing search indexes")
	}
	return nil
}
