// Package ref parses, validates and canonicalises skill references.
//
// It is the first operation of every method that receives a reference, and it
// owns the input validation that implies: it rejects path escapes, demands
// HTTPS, and bounds length and character set.
//
// Final syntax, settled early so no form changes meaning between versions:
//
//	gh:owner/repo/path[@ref]      GitHub repository
//	https://...                   markdown at a URL
//	./ ../ / ~                    local path
//	owner/repo/id                 registry identifier
package ref

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kleberS4/delivery/internal/errs"
)

// Kind identifies a reference's source type.
type Kind int

const (
	KindGitHub Kind = iota
	KindURL
	KindRegistry
	KindLocal
)

func (k Kind) String() string {
	switch k {
	case KindGitHub:
		return "github"
	case KindURL:
		return "url"
	case KindRegistry:
		return "registry"
	case KindLocal:
		return "local"
	default:
		return "unknown"
	}
}

// Input bounds.
const (
	MaxRefLen     = 512
	MaxSegmentLen = 128
	MaxSegments   = 32
)

// GitHubPrefix marks a reference to a GitHub repository.
const GitHubPrefix = "gh:"

// Ref is a validated skill reference.
type Ref struct {
	Kind    Kind
	Owner   string // KindGitHub, KindRegistry
	Repo    string // KindGitHub, KindRegistry
	Path    string // KindGitHub and KindLocal, already cleaned
	GitRef  string // KindGitHub, optional
	SkillID string // KindRegistry: the skill's directory name
	URL     string // KindURL, already normalised
	Raw     string // original input, kept for error messages
}

const actionRefSyntax = "use gh:owner/repo/path[@ref] for GitHub, or https://... for a URL"

// Parse classifies and validates a reference. Input that does not match
// exactly one pattern is rejected rather than resolved by guesswork: guessing
// here would grant trust to the wrong skill.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, errs.Usage(actionRefSyntax, "empty reference")
	}
	if len(s) > MaxRefLen {
		return Ref{}, errs.Usage(actionRefSyntax,
			"reference is %d characters, over the limit of %d", len(s), MaxRefLen)
	}

	switch {
	case strings.HasPrefix(s, GitHubPrefix):
		return parseGitHub(s)

	case strings.HasPrefix(s, "https://"):
		return parseURL(s)

	case strings.HasPrefix(s, "http://"):
		return Ref{}, errs.Usage("use https:// — plain HTTP is not accepted",
			"the http scheme is not accepted: %q", s)

	case strings.HasPrefix(s, "./"), strings.HasPrefix(s, "../"),
		strings.HasPrefix(s, "/"), strings.HasPrefix(s, "~"):
		return parseLocal(s)

	default:
		return parseRegistry(s)
	}
}

// parseRegistry reads the bare owner/repo/id form.
//
// A registry identifier has exactly three segments, and the last one is NOT a
// repository path: it is the skill's directory name, which has to be located
// inside the repository at resolve time.
func parseRegistry(s string) (Ref, error) {
	segs, err := cleanSegments(s)
	if err != nil {
		return Ref{}, err
	}
	if len(segs) != 3 {
		return Ref{}, errs.Usage(
			"registry identifiers look like owner/repo/id; for a path inside a "+
				"repository use gh:"+s,
			"reference %q not recognised", s)
	}
	return Ref{
		Kind:    KindRegistry,
		Owner:   strings.ToLower(segs[0]),
		Repo:    strings.ToLower(segs[1]),
		SkillID: segs[2],
		Raw:     s,
	}, nil
}

// parseLocal reads a filesystem path.
//
// Final containment is validated later, against the real path and after
// resolving symlinks — validating only the textual form would miss a
// symlink pointing outside the directory.
func parseLocal(s string) (Ref, error) {
	expanded := s
	if strings.HasPrefix(s, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Ref{}, errs.Wrap(err, errs.ClassUsage, actionRefSyntax,
				"could not expand %q", s)
		}
		expanded = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(s, "~"), "/"))
	}

	abs, err := filepath.Abs(expanded)
	if err != nil {
		return Ref{}, errs.Wrap(err, errs.ClassUsage, actionRefSyntax,
			"invalid path: %q", s)
	}
	return Ref{Kind: KindLocal, Path: filepath.Clean(abs), Raw: s}, nil
}

func parseGitHub(s string) (Ref, error) {
	body := strings.TrimPrefix(s, GitHubPrefix)

	gitRef := ""
	if i := strings.LastIndex(body, "@"); i >= 0 {
		gitRef = body[i+1:]
		body = body[:i]
		if gitRef == "" {
			return Ref{}, errs.Usage(actionRefSyntax, "empty git ref after @ in %q", s)
		}
		if err := validateGitRef(gitRef); err != nil {
			return Ref{}, err
		}
	}

	segs, err := cleanSegments(body)
	if err != nil {
		return Ref{}, err
	}
	if len(segs) < 3 {
		return Ref{}, errs.Usage(actionRefSyntax,
			"a GitHub reference needs owner, repo and path: %q", s)
	}

	return Ref{
		Kind:   KindGitHub,
		Owner:  strings.ToLower(segs[0]),
		Repo:   strings.ToLower(segs[1]),
		Path:   strings.Join(segs[2:], "/"),
		GitRef: gitRef,
		Raw:    s,
	}, nil
}

func parseURL(s string) (Ref, error) {
	u, err := url.Parse(s)
	if err != nil {
		return Ref{}, errs.Wrap(err, errs.ClassUsage, actionRefSyntax, "invalid URL: %q", s)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return Ref{}, errs.Usage("use https://", "only HTTPS is accepted: %q", s)
	}
	if u.Host == "" {
		return Ref{}, errs.Usage(actionRefSyntax, "URL has no host: %q", s)
	}
	if strings.Contains(u.Path, "..") {
		return Ref{}, errs.Usage(actionRefSyntax, "URL contains a path escape: %q", s)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimSuffix(u.Host, ":443")
	u.Fragment = ""

	return Ref{Kind: KindURL, URL: u.String(), Raw: s}, nil
}

// cleanSegments splits on "/", drops empty segments and validates each one.
// Dropping empties is what makes the canonical form stable under duplicated or
// trailing slashes.
func cleanSegments(p string) ([]string, error) {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return nil, errs.Usage(actionRefSyntax, "path escapes are not allowed: %q", p)
		}
		if len(seg) > MaxSegmentLen {
			return nil, errs.Usage(actionRefSyntax,
				"segment is %d characters, over the limit of %d", len(seg), MaxSegmentLen)
		}
		if !validSegment(seg) {
			return nil, errs.Usage(actionRefSyntax, "segment has an invalid character: %q", seg)
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return nil, errs.Usage(actionRefSyntax, "reference has no usable segments")
	}
	if len(out) > MaxSegments {
		return nil, errs.Usage(actionRefSyntax,
			"reference has %d segments, over the limit of %d", len(out), MaxSegments)
	}
	return out, nil
}

func validSegment(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func validateGitRef(s string) error {
	if len(s) > MaxSegmentLen {
		return errs.Usage(actionRefSyntax, "git ref is over the %d character limit", MaxSegmentLen)
	}
	if strings.Contains(s, "..") {
		return errs.Usage(actionRefSyntax, "git ref contains a path escape: %q", s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || !validSegment(seg) {
			return errs.Usage(actionRefSyntax, "invalid git ref: %q", s)
		}
	}
	return nil
}

// LooksLikeReference reports whether the input has the shape of a reference,
// even an invalid one.
//
// It distinguishes "malformed reference" from "unknown short name" — two
// situations that produce different error classes, and that the agent needs to
// tell apart. A short name never contains a slash, because
// normalisation turns slashes into hyphens.
func LooksLikeReference(s string) bool {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, GitHubPrefix),
		strings.HasPrefix(s, "https://"),
		strings.HasPrefix(s, "http://"),
		strings.HasPrefix(s, "./"),
		strings.HasPrefix(s, "../"),
		strings.HasPrefix(s, "/"),
		strings.HasPrefix(s, "~"),
		strings.Contains(s, "/"):
		return true
	default:
		return false
	}
}

// String returns the canonical form, which Parse accepts back.
func (r Ref) String() string {
	switch r.Kind {
	case KindGitHub:
		s := GitHubPrefix + r.Owner + "/" + r.Repo + "/" + r.Path
		if r.GitRef != "" {
			s += "@" + r.GitRef
		}
		return s
	case KindURL:
		return r.URL
	case KindRegistry:
		return r.Owner + "/" + r.Repo + "/" + r.SkillID
	case KindLocal:
		return r.Path
	default:
		return r.Raw
	}
}

// Canonical returns the stable key used in the trust store and the cache. It
// does not depend on how the original input was typed.
func (r Ref) Canonical() string { return r.String() }

// Equal compares two references, ignoring the original input.
func (r Ref) Equal(other Ref) bool {
	return r.Kind == other.Kind &&
		r.Owner == other.Owner &&
		r.Repo == other.Repo &&
		r.Path == other.Path &&
		r.GitRef == other.GitRef &&
		r.SkillID == other.SkillID &&
		r.URL == other.URL
}

// SourceKey returns the groupable source identity (owner/repo). The second
// return is false when the kind has no groupable source.
func (r Ref) SourceKey() (string, bool) {
	if r.Kind == KindGitHub || r.Kind == KindRegistry {
		return r.Owner + "/" + r.Repo, true
	}
	return "", false
}

// IsDocument reports whether the path points at a markdown file rather than a
// directory. The classification comes from the reference itself: probing the
// source to discover something the reference already states would be cost
// without return.
func (r Ref) IsDocument() bool {
	if r.Kind == KindURL {
		return true
	}
	if r.Kind != KindGitHub && r.Kind != KindLocal {
		return false
	}
	lower := strings.ToLower(r.Path)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// BaseName returns the last path segment, used to derive the short name when
// there is no frontmatter.
func (r Ref) BaseName() string {
	switch r.Kind {
	case KindRegistry:
		return trimMarkdownSuffix(r.SkillID)
	case KindLocal:
		return trimMarkdownSuffix(filepath.Base(r.Path))
	case KindGitHub:
		segs := strings.Split(r.Path, "/")
		return trimMarkdownSuffix(segs[len(segs)-1])
	case KindURL:
		u, err := url.Parse(r.URL)
		if err != nil {
			return ""
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		return trimMarkdownSuffix(segs[len(segs)-1])
	default:
		return ""
	}
}

func trimMarkdownSuffix(s string) string {
	for _, suf := range []string{".markdown", ".md", ".MD"} {
		if strings.HasSuffix(s, suf) {
			return s[:len(s)-len(suf)]
		}
	}
	return s
}
