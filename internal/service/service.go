// Package service orchestrates the domain components.
//
// Services take data and return data or typed errors. No signature carries a
// writer, a file, or any presentation parameter: the core has no terminal I/O,
// and that is what makes the output contract a property of the structure
// rather than a convention (pattern P1).
//
// Operations that depend on a human decision are split in two — one that
// computes without changing state, and one that applies a decision already
// made. The interaction happens between them, outside this package
// (pattern P2).
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kleberS4/delivery/internal/anchor"
	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/cache"
	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/integrity"
	"github.com/kleberS4/delivery/internal/meta"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/source"
	"github.com/kleberS4/delivery/internal/tool"
	"github.com/kleberS4/delivery/internal/trust"
)

// Deps groups the dependencies wired manually at the entry point.
type Deps struct {
	Paths    config.Paths
	Store    *trust.Store
	Cache    *cache.Cache
	Resolver source.Resolver
	Anchor   *anchor.Generator
	Adapter  tool.Adapter
	Log      *slog.Logger
}

// --- initialisation ---

// InitService prepares the machine.
type InitService struct{ d Deps }

// NewInitService builds the initialisation service.
func NewInitService(d Deps) *InitService { return &InitService{d: d} }

// InitReport describes what initialisation wrote.
type InitReport struct {
	Tool          string
	StateRoot     string
	TrustFile     string
	CacheDir      string
	AnchorPath    string
	TrustedSkills int
	AnchorStats   anchor.Stats
}

// Init prepares local state and generates the anchor.
//
// Write access and permissions are checked before the first modification, so a
// failure leaves the system exactly as it was. Loading the existing
// store before generating the anchor is what makes the operation idempotent:
// the second run is reconciliation, not reinitialisation.
func (s *InitService) Init(_ context.Context) (InitReport, error) {
	if err := config.CheckWritable(s.d.Paths); err != nil {
		return InitReport{}, err
	}
	if err := config.CheckPermissions(s.d.Paths); err != nil {
		return InitReport{}, err
	}
	if err := config.EnsureDirs(s.d.Paths); err != nil {
		return InitReport{}, err
	}

	records := s.d.Store.List()
	stats, err := s.d.Anchor.Regenerate(records)
	if err != nil {
		return InitReport{}, err
	}

	return InitReport{
		Tool:          s.d.Adapter.Name(),
		StateRoot:     s.d.Paths.Root,
		TrustFile:     s.d.Paths.TrustFile,
		CacheDir:      s.d.Paths.CacheDir,
		AnchorPath:    s.d.Anchor.Path(),
		TrustedSkills: len(records),
		AnchorStats:   stats,
	}, nil
}

// --- trust ---

// Situation classifies the state found while preparing a trust decision.
type Situation int

const (
	SituationNew Situation = iota
	SituationAlreadyTrusted
	SituationReApproval
)

func (s Situation) String() string {
	switch s {
	case SituationNew:
		return "new"
	case SituationAlreadyTrusted:
		return "already trusted, unchanged"
	case SituationReApproval:
		return "already trusted, content changed"
	default:
		return "unknown"
	}
}

// Preview is the result of the computation that precedes the human decision.
type Preview struct {
	Ref        ref.Ref
	Artifact   *artifact.Artifact
	Meta       artifact.Metadata
	SetHash    string
	FileHashes map[string]string
	Situation  Situation
	Modified   []string
	Added      []string
	Removed    []string
}

// TrustService drives the trust decision.
type TrustService struct{ d Deps }

// NewTrustService builds the trust service.
func NewTrustService(d Deps) *TrustService { return &TrustService{d: d} }

// Preview resolves the skill, computes its hash and classifies the situation.
// No disk write happens here.
func (s *TrustService) Preview(ctx context.Context, raw string) (*Preview, error) {
	r, err := ref.Parse(raw)
	if err != nil {
		return nil, err
	}

	a, err := s.d.Resolver.Resolve(ctx, r)
	if err != nil {
		return nil, err
	}
	a.Normalize()
	a.Meta = meta.Extract(a.Document, r)

	setHash, files := integrity.HashSet(a)

	p := &Preview{
		Ref: r, Artifact: a, Meta: a.Meta,
		SetHash: setHash, FileHashes: files,
		Situation: SituationNew,
	}

	if existing, ok := s.d.Store.Get(r.Canonical()); ok {
		if existing.SetHash == setHash {
			p.Situation = SituationAlreadyTrusted
		} else {
			p.Situation = SituationReApproval
			p.Modified, p.Added, p.Removed = integrity.Changed(existing.FileHashes, files)
		}
		// Preserve the name and description the user already chose.
		if existing.MetaOrigin == artifact.OriginUserOverride.String() {
			p.Meta.Name = existing.ShortName
			p.Meta.Description = existing.Description
			p.Meta.Origin = artifact.OriginUserOverride
		}
	}
	return p, nil
}

// ConfirmOptions carries the optional overrides of the human decision.
type ConfirmOptions struct {
	Name        string
	Description string
}

// Confirm applies a decision already made.
//
// The cache is written before the trust record. An interruption between the
// two leaves cached content with no recorded trust — harmless, because without
// a record get refuses anyway. The reverse order would leave trust with no
// content. Failing towards the harmless side is the correct behaviour.
func (s *TrustService) Confirm(_ context.Context, p *Preview, opts ConfirmOptions) (anchor.Stats, error) {
	m := p.Meta
	if opts.Name != "" {
		m.Name = meta.NormalizeName(opts.Name)
		m.Origin = artifact.OriginUserOverride
	}
	if opts.Description != "" {
		m.Description = opts.Description
		m.Origin = artifact.OriginUserOverride
	}
	if m.Name == "" {
		return anchor.Stats{}, errs.Usage("pass --name", "empty short name")
	}

	if err := s.d.Cache.Put(p.Ref, p.Artifact, p.SetHash, p.FileHashes); err != nil {
		return anchor.Stats{}, err
	}

	rec := trust.Record{
		Ref:         p.Ref.Canonical(),
		Kind:        p.Ref.Kind.String(),
		ShortName:   m.Name,
		Description: m.Description,
		MetaOrigin:  m.Origin.String(),
		SetHash:     p.SetHash,
		FileHashes:  p.FileHashes,
		Version:     p.Artifact.Version,
		SourceKind:  sourceKindOf(p.Ref),
		TrustedAt:   time.Now().UTC(),
	}
	if err := s.d.Store.Put(rec); err != nil {
		return anchor.Stats{}, err
	}

	return s.d.Anchor.Regenerate(s.d.Store.List())
}

func sourceKindOf(r ref.Ref) string {
	if r.Kind == ref.KindGitHub && !r.IsDocument() {
		return "tarball"
	}
	return "raw"
}

// --- delivery ---

// ResourceRef describes a supporting file available on disk.
type ResourceRef struct {
	RelPath string
	Path    string
}

// GetResult is the verified set, ready to hand over.
type GetResult struct {
	Record    trust.Record
	Document  []byte
	Resources []ResourceRef
	FromCache bool
}

// SkillService hands over skill content.
type SkillService struct{ d Deps }

// NewSkillService builds the delivery service.
func NewSkillService(d Deps) *SkillService { return &SkillService{d: d} }

// Get is the product's hot path.
//
// Three ordering invariants, each with a concrete reason: trust is checked
// before any network access, so an untrusted skill fails instantly; integrity
// is checked before anything is handed over, so no unverified byte reaches the
// agent's context; and no branch opens a prompt, so the absence of a terminal
// never stalls the session.
func (s *SkillService) Get(ctx context.Context, input string) (*GetResult, error) {
	rec, err := s.resolveName(input)
	if err != nil {
		return nil, err
	}

	// A skill covered only by source trust has no approved hash: nobody
	// reviewed that specific content. It is resolved from the origin on every
	// use and handed over without an integrity comparison — which is exactly
	// what trusting a whole source means, and the reason trust --source states
	// the implication before confirming.
	if rec.SetHash == "" {
		return s.getBySourceTrust(ctx, rec)
	}

	r, err := ref.Parse(rec.Ref)
	if err != nil {
		return nil, errs.State("remove the record and trust the skill again",
			"trust record has an invalid reference: %q", rec.Ref)
	}

	a, fromCache, err := s.d.Cache.Get(r)
	if err != nil {
		return nil, err
	}
	if !fromCache {
		s.d.Log.Debug("cache missing, re-resolving from the origin", "ref", rec.Ref)
		a, err = s.d.Resolver.Resolve(ctx, r)
		if err != nil {
			return nil, err
		}
		a.Normalize()
	}

	action := fmt.Sprintf("review and re-approve with: delivery trust %s", rec.Ref)
	if err := integrity.VerifySet(a, rec.SetHash, action); err != nil {
		return nil, err
	}

	if !fromCache {
		if err := s.d.Cache.Put(r, a, rec.SetHash, rec.FileHashes); err != nil {
			s.d.Log.Warn("could not repopulate the cache", "error", err)
		}
	}

	res := make([]ResourceRef, 0, len(a.Resources))
	base := s.d.Cache.ResourcesDir(r)
	for _, rr := range a.Resources {
		res = append(res, ResourceRef{RelPath: rr.RelPath, Path: base + "/" + rr.RelPath})
	}

	return &GetResult{Record: rec, Document: a.Document, Resources: res, FromCache: fromCache}, nil
}

// getBySourceTrust hands over a skill covered only by source trust.
func (s *SkillService) getBySourceTrust(ctx context.Context, rec trust.Record) (*GetResult, error) {
	r, err := ref.Parse(rec.Ref)
	if err != nil {
		return nil, errs.State("untrust the source and trust the skill individually",
			"invalid reference: %q", rec.Ref)
	}
	a, err := s.d.Resolver.Resolve(ctx, r)
	if err != nil {
		return nil, err
	}
	a.Normalize()
	s.d.Log.Warn("skill handed over through source trust, with no approved hash",
		"ref", rec.Ref, "source", rec.SourceKind)

	res := make([]ResourceRef, 0, len(a.Resources))
	base := s.d.Cache.ResourcesDir(r)
	for _, rr := range a.Resources {
		res = append(res, ResourceRef{RelPath: rr.RelPath, Path: base + "/" + rr.RelPath})
	}
	if len(a.Resources) > 0 {
		setHash, files := integrity.HashSet(a)
		if err := s.d.Cache.Put(r, a, setHash, files); err != nil {
			s.d.Log.Warn("could not write the cache", "error", err)
		}
	}
	return &GetResult{Record: rec, Document: a.Document, Resources: res}, nil
}

// resolveName turns the supplied text into a trust record.
//
// Order matters: trying the canonical reference first prevents a short name
// that happens to look like a reference from being resolved down the wrong
// path.
func (s *SkillService) resolveName(input string) (trust.Record, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return trust.Record{}, errs.Usage("provide the skill name or reference",
			"empty argument")
	}

	r, parseErr := ref.Parse(input)
	if parseErr == nil {
		if rec, ok := s.d.Store.Get(r.Canonical()); ok {
			return rec, nil
		}
		if key, ok := r.SourceKey(); ok && s.d.Store.CoveredBySource(key) {
			return trust.Record{
				Ref:        r.Canonical(),
				Kind:       r.Kind.String(),
				ShortName:  r.BaseName(),
				SourceKind: "fonte confiada",
			}, nil
		}
		return trust.Record{}, errs.NotTrusted(
			"ask the person to run, in their terminal: delivery trust "+r.Canonical(),
			"skill %q has not been trusted", r.Canonical())
	}

	// Input shaped like a reference that fails validation is a usage error,
	// not an unknown short name. Reinterpreting it as a name would hide the
	// real problem behind a misleading message.
	if ref.LooksLikeReference(input) {
		return trust.Record{}, parseErr
	}

	matches := s.d.Store.FindByShortName(meta.NormalizeName(input))
	switch len(matches) {
	case 0:
		return trust.Record{}, errs.NotTrusted(
			"ask the person to run, in their terminal: delivery trust <reference>",
			"no trusted skill named %q", input)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&b, "\n  delivery get %s", m.Ref)
		}
		return trust.Record{}, errs.Usage(
			"use the canonical reference of one of these:"+b.String(),
			"the name %q matches %d trusted skills", input, len(matches))
	}
}

// List returns the inventory of trusted skills.
func (s *SkillService) List() []trust.Record { return s.d.Store.List() }
