// Package trust persists human trust decisions.
//
// Together with integrity, it is the system's security-critical component.
// The split between the two is deliberate: this package
// remembers the decision, that one verifies the content. Kept apart, each is
// auditable on its own, and changing the storage format never touches
// verification.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/errs"
)

// SchemaVersion versions the on-disk format. A mismatch invalidates the file
// rather than partially interpreting it.
const SchemaVersion = 1

// Record is the record of one trust decision.
type Record struct {
	Ref         string            `json:"ref"`
	Kind        string            `json:"kind"`
	ShortName   string            `json:"shortName"`
	Description string            `json:"description"`
	MetaOrigin  string            `json:"metaOrigin"`
	SetHash     string            `json:"setHash"`
	FileHashes  map[string]string `json:"fileHashes"`
	Version     string            `json:"version,omitempty"`
	SourceKind  string            `json:"sourceKind"`
	Installed   bool              `json:"installed,omitempty"`
	TrustedAt   time.Time         `json:"trustedAt"`
}

// SourceRecord is trust granted to an entire source.
type SourceRecord struct {
	Source    string    `json:"source"`
	TrustedAt time.Time `json:"trustedAt"`
}

type fileData struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Records       map[string]Record       `json:"records"`
	Sources       map[string]SourceRecord `json:"sources"`
}

// Store is the on-disk trust storage.
type Store struct {
	path string
	data fileData
}

// Open loads the store, validating permissions before any useful read. A
// missing file yields an empty, valid store.
func Open(p config.Paths) (*Store, error) {
	if err := config.CheckPermissions(p); err != nil {
		return nil, err
	}
	s := &Store{
		path: p.TrustFile,
		data: fileData{
			SchemaVersion: SchemaVersion,
			Records:       map[string]Record{},
			Sources:       map[string]SourceRecord{},
		},
	}

	raw, err := os.ReadFile(p.TrustFile)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errs.Wrap(err, errs.ClassState,
			"check the state path and its permissions", "reading %s", p.TrustFile)
	}

	var loaded fileData
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, errs.Wrap(err, errs.ClassState,
			"fix or remove "+p.TrustFile+" and trust the skills again",
			"invalid trust file")
	}
	if loaded.SchemaVersion != SchemaVersion {
		return nil, errs.State(
			"remove "+p.TrustFile+" and trust the skills again",
			"trust file schema is %d, expected %d",
			loaded.SchemaVersion, SchemaVersion)
	}
	if loaded.Records == nil {
		loaded.Records = map[string]Record{}
	}
	if loaded.Sources == nil {
		loaded.Sources = map[string]SourceRecord{}
	}
	s.data = loaded
	return s, nil
}

// Get returns the individual trust record, if there is one.
func (s *Store) Get(canonical string) (Record, bool) {
	r, ok := s.data.Records[canonical]
	return r, ok
}

// Put stores a record and persists atomically.
func (s *Store) Put(r Record) error {
	s.data.Records[r.Ref] = r
	return s.flush()
}

// Delete removes a record and persists atomically.
func (s *Store) Delete(canonical string) error {
	delete(s.data.Records, canonical)
	return s.flush()
}

// List returns records in canonical order by reference. The ordering is a
// precondition for deterministic anchor rendering.
func (s *Store) List() []Record {
	out := make([]Record, 0, len(s.data.Records))
	for _, r := range s.data.Records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// Len returns the number of records.
func (s *Store) Len() int { return len(s.data.Records) }

// FindByShortName returns every record whose short name matches. Collision is
// allowed by design: the anchor index already carries the canonical reference,
// so the agent is never left without a way forward.
func (s *Store) FindByShortName(name string) []Record {
	var out []Record
	for _, r := range s.List() {
		if r.ShortName == name {
			out = append(out, r)
		}
	}
	return out
}

// CoveredBySource reports whether the whole source is trusted.
func (s *Store) CoveredBySource(source string) bool {
	_, ok := s.data.Sources[source]
	return ok
}

// ListSources returns the wholly trusted sources, in order.
func (s *Store) ListSources() []SourceRecord {
	out := make([]SourceRecord, 0, len(s.data.Sources))
	for _, r := range s.data.Sources {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// DeleteSource revokes trust granted to an entire source.
//
// It removes ONLY the source coverage: individual trust records survive, so
// that revoking a broad decision never undoes the specific ones the person made
// one at a time.
func (s *Store) DeleteSource(source string) error {
	delete(s.data.Sources, source)
	return s.flush()
}

// PutSource records trust in an entire source.
func (s *Store) PutSource(source string) error {
	s.data.Sources[source] = SourceRecord{Source: source, TrustedAt: time.Now().UTC()}
	return s.flush()
}

func (s *Store) flush() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("serialising trust file: %w", err)
	}
	raw = append(raw, '\n')
	if err := atomicfs.WriteFile(s.path, raw, config.TrustFilePerm); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on the state directory",
			"writing trust file")
	}
	return nil
}

// Marshal serialises the store's contents. Exposed for the round-trip property
// tests (P-04).
func Marshal(records []Record, sources []SourceRecord) ([]byte, error) {
	d := fileData{
		SchemaVersion: SchemaVersion,
		Records:       map[string]Record{},
		Sources:       map[string]SourceRecord{},
	}
	for _, r := range records {
		d.Records[r.Ref] = r
	}
	for _, s := range sources {
		d.Sources[s.Source] = s
	}
	return json.Marshal(d)
}

// Unmarshal deserialises the store's contents.
func Unmarshal(raw []byte) ([]Record, []SourceRecord, error) {
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, nil, err
	}
	records := make([]Record, 0, len(d.Records))
	for _, r := range d.Records {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Ref < records[j].Ref })

	sources := make([]SourceRecord, 0, len(d.Sources))
	for _, s := range d.Sources {
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Source < sources[j].Source })
	return records, sources, nil
}
