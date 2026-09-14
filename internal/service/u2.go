package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kleberS4/delivery/internal/anchor"
	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cache"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/footer"
	"github.com/kleberS4/delivery/internal/integrity"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/source"
	"github.com/kleberS4/delivery/internal/trust"
)

// --- discovery ---

// TrustState classifies a search result's availability.
type TrustState int

const (
	TrustStateNone TrustState = iota
	TrustStateIndividual
	TrustStateBySource
)

func (t TrustState) String() string {
	switch t {
	case TrustStateIndividual:
		return "trusted"
	case TrustStateBySource:
		return "covered by source"
	default:
		return "not trusted"
	}
}

// SearchHit is a result annotated with its trust state.
type SearchHit struct {
	Entry artifact.IndexEntry
	State TrustState
}

// SearchResult gathers the results and the provenance of the index used.
type SearchResult struct {
	Hits    []SearchHit
	Stale   bool          // index served despite being stale
	Age     time.Duration // age of the index served
	FromNet bool
}

// DiscoveryService finds skills and reports their trust state.
type DiscoveryService struct {
	d        Deps
	searcher source.Searcher
}

// NewDiscoveryService builds the discovery service.
func NewDiscoveryService(d Deps, s source.Searcher) *DiscoveryService {
	return &DiscoveryService{d: d, searcher: s}
}

// Search queries the index, refreshing it when stale.
//
// A stale index with no network is served anyway, with a warning. Searching is
// an informational operation: a possibly outdated result still guides, while an
// error guides nothing.
func (s *DiscoveryService) Search(ctx context.Context, q source.Query) (*SearchResult, error) {
	now := time.Now().UTC()

	idx, found, err := s.d.Cache.Index(q.Term)
	if err != nil {
		return nil, err
	}
	if found && idx.Fresh(now) {
		return s.annotate(idx.Entries, q.Limit, false, 0, false), nil
	}

	entries, netErr := s.searcher.Search(ctx, q)
	if netErr != nil {
		if found {
			s.d.Log.Warn("registry unreachable; serving a stale index",
				"term", q.Term, "age", idx.Age(now).String())
			return s.annotate(idx.Entries, q.Limit, true, idx.Age(now), false), nil
		}
		return nil, netErr
	}

	if err := s.d.Cache.PutIndex(cache.SearchIndex{
		Query: q.Term, FetchedAt: now, Entries: entries,
	}); err != nil {
		s.d.Log.Warn("could not write the search index", "error", err)
	}
	return s.annotate(entries, q.Limit, false, 0, true), nil
}

func (s *DiscoveryService) annotate(entries []artifact.IndexEntry, limit int, stale bool, age time.Duration, fromNet bool) *SearchResult {
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	hits := make([]SearchHit, 0, len(entries))
	for _, e := range entries {
		hits = append(hits, SearchHit{Entry: e, State: s.stateOf(e)})
	}
	return &SearchResult{Hits: hits, Stale: stale, Age: age, FromNet: fromNet}
}

func (s *DiscoveryService) stateOf(e artifact.IndexEntry) TrustState {
	if r, err := ref.Parse(e.ID); err == nil {
		if _, ok := s.d.Store.Get(r.Canonical()); ok {
			return TrustStateIndividual
		}
	}
	if s.d.Store.CoveredBySource(e.Source) {
		return TrustStateBySource
	}
	return TrustStateNone
}

// --- source trust and revocation ---

// TrustSource records trust in an entire repository.
//
// The implication of reaching future content is presented by the command-line
// layer BEFORE the confirmation; this method receives a decision already made.
func (s *TrustService) TrustSource(_ context.Context, sourceKey string) (anchor.Stats, error) {
	if err := validateSourceKey(sourceKey); err != nil {
		return anchor.Stats{}, err
	}
	if err := s.d.Store.PutSource(sourceKey); err != nil {
		return anchor.Stats{}, err
	}
	return s.d.Anchor.Regenerate(s.d.Store.List())
}

// UntrustSource removes a source's coverage.
//
// Individual trust granted earlier stays intact.
func (s *TrustService) UntrustSource(_ context.Context, sourceKey string) (anchor.Stats, error) {
	if err := validateSourceKey(sourceKey); err != nil {
		return anchor.Stats{}, err
	}
	if !s.d.Store.CoveredBySource(sourceKey) {
		return anchor.Stats{}, errs.NotFound("check your sources with: delivery list",
			"source %q was not trusted", sourceKey)
	}
	if err := s.d.Store.DeleteSource(sourceKey); err != nil {
		return anchor.Stats{}, err
	}
	return s.d.Anchor.Regenerate(s.d.Store.List())
}

// UntrustResult reports what is left after revocation.
type UntrustResult struct {
	Ref            string
	StillInstalled bool
	Stats          anchor.Stats
}

// Untrust revokes trust in a skill.
func (s *TrustService) Untrust(_ context.Context, input string) (*UntrustResult, error) {
	rec, err := resolveRecord(s.d.Store, input)
	if err != nil {
		return nil, err
	}
	if err := s.d.Store.Delete(rec.Ref); err != nil {
		return nil, err
	}
	r, parseErr := ref.Parse(rec.Ref)
	if parseErr == nil {
		if err := s.d.Cache.Evict(r); err != nil {
			s.d.Log.Warn("could not remove the cache", "error", err)
		}
	}
	stats, err := s.d.Anchor.Regenerate(s.d.Store.List())
	if err != nil {
		return nil, err
	}
	return &UntrustResult{Ref: rec.Ref, StillInstalled: rec.Installed, Stats: stats}, nil
}

func validateSourceKey(s string) error {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errs.Usage("use the owner/repo form", "source %q is not in owner/repo form", s)
	}
	return nil
}

// --- persistent installation ---

// InstallResult describes what was written.
type InstallResult struct {
	Ref       string
	ShortName string
	Path      string
	Resources int
	Stats     anchor.Stats
}

// Install writes the skill into the tool's directory.
//
// The recorded hash remains that of the ORIGINAL content: the footer is
// appended only to what goes to disk. Without that separation, delivery would
// invalidate its own integrity check the moment it installed.
func (s *SkillService) Install(ctx context.Context, input string) (*InstallResult, error) {
	res, err := s.Get(ctx, input)
	if err != nil {
		return nil, err
	}

	doc := footer.Compose(res.Document, footer.Management{
		Source:      res.Record.Ref,
		Version:     res.Record.Version,
		Hash:        res.Record.SetHash,
		InstalledAt: time.Now().UTC(),
	})

	if err := s.d.Adapter.WriteSkill(res.Record.ShortName, doc); err != nil {
		return nil, err
	}

	rec := res.Record
	rec.Installed = true
	if err := s.d.Store.Put(rec); err != nil {
		return nil, err
	}

	stats, err := s.d.Anchor.Regenerate(s.d.Store.List())
	if err != nil {
		return nil, err
	}

	return &InstallResult{
		Ref:       rec.Ref,
		ShortName: rec.ShortName,
		Path:      s.d.Adapter.SkillPath(rec.ShortName),
		Resources: len(res.Resources),
		Stats:     stats,
	}, nil
}

// Uninstall removes the skill from the tool's directory.
//
// It only removes files whose footer identifies delivery as the manager. A
// skill written by hand, in a directory whose name happens to match, is never
// removed: the absence of the block is proof enough that the file is not
// ours.
func (s *SkillService) Uninstall(_ context.Context, input string) (anchor.Stats, error) {
	rec, err := resolveRecord(s.d.Store, input)
	if err != nil {
		return anchor.Stats{}, err
	}

	existing, err := s.d.Adapter.ReadSkill(rec.ShortName)
	if err != nil {
		return anchor.Stats{}, err
	}
	if !footer.IsManaged(existing) {
		return anchor.Stats{}, errs.Usage(
			"remove the file by hand if that is what you intend",
			"the file for %q is not managed by delivery and will not be removed", rec.ShortName)
	}

	if err := s.d.Adapter.RemoveSkill(rec.ShortName); err != nil {
		return anchor.Stats{}, err
	}
	rec.Installed = false
	if err := s.d.Store.Put(rec); err != nil {
		return anchor.Stats{}, err
	}
	return s.d.Anchor.Regenerate(s.d.Store.List())
}

// --- maintenance ---

// ChangeItem describes a skill whose origin drifted from what was approved.
type ChangeItem struct {
	Ref       string
	ShortName string
	OldHash   string
	NewHash   string
	Modified  []string
	Added     []string
	Removed   []string
	artifact  *artifact.Artifact
}

// ChangeSet is the result of the update check.
type ChangeSet struct {
	Items       []ChangeItem
	Unreachable []string
	Unchanged   int
}

// InventoryItem is one row of the audit inventory.
type InventoryItem struct {
	Ref         string
	ShortName   string
	Description string
	MetaOrigin  string
	Via         TrustState
	Installed   bool
	SetHash     string
	TrustedAt   time.Time
}

// Inventory is the audit result.
type Inventory struct {
	Items   []InventoryItem
	Sources []trust.SourceRecord
}

// MaintenanceService keeps the trusted set healthy over time.
type MaintenanceService struct{ d Deps }

// NewMaintenanceService builds the maintenance service.
func NewMaintenanceService(d Deps) *MaintenanceService { return &MaintenanceService{d: d} }

// CheckUpdates compares each trusted skill's content against its recorded hash.
//
// IT CHANGES NO STATE AT ALL. A network failure on one skill
// marks it unverifiable and does not stop the others from being checked:
// aborting everything would turn a local problem into paralysis.
func (m *MaintenanceService) CheckUpdates(ctx context.Context, only string) (*ChangeSet, error) {
	records := m.d.Store.List()
	if only != "" {
		rec, err := resolveRecord(m.d.Store, only)
		if err != nil {
			return nil, err
		}
		records = []trust.Record{rec}
	}

	cs := &ChangeSet{}
	for _, rec := range records {
		r, err := ref.Parse(rec.Ref)
		if err != nil {
			cs.Unreachable = append(cs.Unreachable, rec.Ref)
			continue
		}

		a, err := m.d.Resolver.Resolve(ctx, r)
		if err != nil {
			m.d.Log.Debug("skill not verifiable", "ref", rec.Ref, "error", err)
			cs.Unreachable = append(cs.Unreachable, rec.Ref)
			continue
		}
		a.Normalize()

		newHash, files := integrity.HashSet(a)
		if newHash == rec.SetHash {
			cs.Unchanged++
			continue
		}

		mod, add, rem := integrity.Changed(rec.FileHashes, files)
		cs.Items = append(cs.Items, ChangeItem{
			Ref: rec.Ref, ShortName: rec.ShortName,
			OldHash: rec.SetHash, NewHash: newHash,
			Modified: mod, Added: add, Removed: rem,
			artifact: a,
		})
	}
	return cs, nil
}

// ApplyUpdates accepts the selected changes.
//
// Unselected items keep their old hash, and get keeps serving the approved
// version indefinitely. Declining an update is not postponing it: it is
// deciding to stay on the approved content.
func (m *MaintenanceService) ApplyUpdates(_ context.Context, cs *ChangeSet, selected map[string]bool) (int, anchor.Stats, error) {
	applied := 0

	for _, item := range cs.Items {
		if selected != nil && !selected[item.Ref] {
			continue
		}
		r, err := ref.Parse(item.Ref)
		if err != nil {
			continue
		}
		rec, ok := m.d.Store.Get(item.Ref)
		if !ok {
			continue
		}

		_, files := integrity.HashSet(item.artifact)
		if err := m.d.Cache.Put(r, item.artifact, item.NewHash, files); err != nil {
			return applied, anchor.Stats{}, err
		}

		rec.SetHash = item.NewHash
		rec.FileHashes = files
		if err := m.d.Store.Put(rec); err != nil {
			return applied, anchor.Stats{}, err
		}

		if rec.Installed {
			doc := footer.Compose(item.artifact.Document, footer.Management{
				Source: rec.Ref, Version: rec.Version,
				Hash: rec.SetHash, InstalledAt: time.Now().UTC(),
			})
			if err := m.d.Adapter.WriteSkill(rec.ShortName, doc); err != nil {
				return applied, anchor.Stats{}, err
			}
		}
		applied++
	}

	stats, err := m.d.Anchor.Regenerate(m.d.Store.List())
	return applied, stats, err
}

// List produces the audit inventory.
func (m *MaintenanceService) List(_ context.Context) *Inventory {
	records := m.d.Store.List()
	items := make([]InventoryItem, 0, len(records))

	for _, rec := range records {
		via := TrustStateIndividual
		items = append(items, InventoryItem{
			Ref: rec.Ref, ShortName: rec.ShortName, Description: rec.Description,
			MetaOrigin: rec.MetaOrigin, Via: via, Installed: rec.Installed,
			SetHash: rec.SetHash, TrustedAt: rec.TrustedAt,
		})
	}
	return &Inventory{Items: items, Sources: m.d.Store.ListSources()}
}

// resolveRecord turns a short name or reference into a trust record.
// Shared by the services that operate on already-trusted skills.
func resolveRecord(store *trust.Store, input string) (trust.Record, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return trust.Record{}, errs.Usage("provide the name or reference", "empty argument")
	}

	r, parseErr := ref.Parse(input)
	if parseErr == nil {
		if rec, ok := store.Get(r.Canonical()); ok {
			return rec, nil
		}
		return trust.Record{}, errs.NotTrusted("check with: delivery list",
			"skill %q is not trusted", r.Canonical())
	}
	if ref.LooksLikeReference(input) {
		return trust.Record{}, parseErr
	}

	matches := store.FindByShortName(input)
	switch len(matches) {
	case 0:
		return trust.Record{}, errs.NotTrusted("check with: delivery list",
			"no trusted skill named %q", input)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&b, "\n  %s", m.Ref)
		}
		return trust.Record{}, errs.Usage("use the canonical reference of one of these:"+b.String(),
			"the name %q matches %d skills", input, len(matches))
	}
}
