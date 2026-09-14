// Package source resolves a reference into a skill's content.
//
// It is one of the three extension points: a new source is a type that
// implements Resolver, registered with the dispatcher, without touching the
// core.
//
// This is also the package that processes untrusted data from the internet.
// The P5 pattern's guards — an uncompressed-byte ceiling checked during
// extraction, and rejection of entries with path escapes — live here, and both
// abort the whole extraction rather than skipping the offending entry.
package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
)

// Default bases for GitHub's services, validated against the real services.
const (
	DefaultRawBase      = "https://raw.githubusercontent.com"
	DefaultCodeloadBase = "https://codeload.github.com"
	userAgent           = "delivery/0.1 (+https://github.com/kleberS4/delivery)"
)

// Reused corrective-action messages. Kept as constants so the user always sees
// the same wording for the same situation.
const (
	actionTooLarge    = "the skill exceeds the allowed size limit"
	actionRepoTooBig  = "the repository is too large to process"
	actionRetry       = "try again"
	actionCheckURL    = "check the URL"
	actionReportSkill = "report this skill to its origin"
	actionTooManyRes  = "the skill exceeds the supporting-file limit"

	msgDocTooLarge     = "document exceeds %d bytes"
	msgUncompressedCap = "uncompressed content exceeds %d bytes"
)

// Limits gathers the size ceilings the fetch paths require.
type Limits struct {
	MaxDocument            int64
	MaxResourcesTotal      int64
	MaxResourceCount       int
	MaxArchiveCompressed   int64
	MaxArchiveUncompressed int64
	Timeout                time.Duration
}

// DefaultLimits returns the ceilings applied to every fetch.
func DefaultLimits() Limits {
	return Limits{
		MaxDocument:            1 << 20,   // 1 MB
		MaxResourcesTotal:      5 << 20,   // 5 MB
		MaxResourceCount:       50,        //
		MaxArchiveCompressed:   50 << 20,  // 50 MB
		MaxArchiveUncompressed: 250 << 20, // 250 MB
		Timeout:                30 * time.Second,
	}
}

// Resolver turns a reference into a skill's content.
type Resolver interface {
	Resolve(ctx context.Context, r ref.Ref) (*artifact.Artifact, error)
}

// Dispatcher routes each reference to the resolver for its kind.
type Dispatcher struct {
	resolvers map[ref.Kind]Resolver
}

// NewDispatcher builds an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{resolvers: make(map[ref.Kind]Resolver)}
}

// Register binds a resolver to a source kind. This is the extension point.
func (d *Dispatcher) Register(k ref.Kind, r Resolver) { d.resolvers[k] = r }

// Resolve routes to the matching resolver.
func (d *Dispatcher) Resolve(ctx context.Context, r ref.Ref) (*artifact.Artifact, error) {
	res, ok := d.resolvers[r.Kind]
	if !ok {
		return nil, errs.Usage("this source kind arrives in a future version",
			"source kind %s is not supported in this version", r.Kind)
	}
	return res.Resolve(ctx, r)
}

// NewHTTPClient builds the shared HTTP client. A redirect to a non-HTTPS
// scheme is refused: without that, a source could downgrade the connection and
// the transport guarantee would fall away silently.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to a non-HTTPS scheme: %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// GitHubResolver fetches skills from GitHub repositories.
//
// A path ending in markdown is fetched with a direct request to the raw
// content service. A directory path is fetched through the repository tarball,
// from which only the skill's subdirectory is extracted — because the raw
// content service cannot list directories.
type GitHubResolver struct {
	Client       *http.Client
	Limits       Limits
	RawBase      string
	CodeloadBase string
}

// NewGitHubResolver builds the resolver with the default bases.
func NewGitHubResolver(client *http.Client, limits Limits) *GitHubResolver {
	return &GitHubResolver{
		Client:       client,
		Limits:       limits,
		RawBase:      DefaultRawBase,
		CodeloadBase: DefaultCodeloadBase,
	}
}

// Resolve fetches the skill's set.
func (g *GitHubResolver) Resolve(ctx context.Context, r ref.Ref) (*artifact.Artifact, error) {
	gitRef := r.GitRef
	if gitRef == "" {
		gitRef = "HEAD"
	}
	if r.IsDocument() {
		url := fmt.Sprintf("%s/%s/%s/%s/%s", g.RawBase, r.Owner, r.Repo, gitRef, r.Path)
		doc, err := g.fetchDocument(ctx, url, r)
		if err != nil {
			return nil, err
		}
		return &artifact.Artifact{Kind: artifact.KindSkill, Document: doc}, nil
	}
	url := fmt.Sprintf("%s/%s/%s/tar.gz/%s", g.CodeloadBase, r.Owner, r.Repo, gitRef)
	return g.fetchArchive(ctx, url, r)
}

func (g *GitHubResolver) fetchDocument(ctx context.Context, url string, r ref.Ref) ([]byte, error) {
	body, err := g.get(ctx, url, r)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	doc, err := readLimited(body, g.Limits.MaxDocument)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassUsage,
			actionTooLarge,
			msgDocTooLarge, g.Limits.MaxDocument)
	}
	if len(doc) == 0 {
		return nil, errs.NotFound("check the reference", "empty document at %s", r.Raw)
	}
	return doc, nil
}

func (g *GitHubResolver) get(ctx context.Context, url string, r ref.Ref) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassUsage, "check the reference", "building the request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork,
			"check connectivity and try again", "reaching the source")
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, errs.NotFound("check the skill reference",
			"skill not found at the source: %s", r.Raw)
	case resp.StatusCode >= 400:
		resp.Body.Close()
		return nil, errs.Network("try again later",
			"source responded with status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// fetchArchive downloads and extracts the repository tarball.
func (g *GitHubResolver) fetchArchive(ctx context.Context, url string, r ref.Ref) (*artifact.Artifact, error) {
	body, err := g.get(ctx, url, r)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	// Compressed-size guard: one byte past the ceiling is already a violation.
	limited := io.LimitReader(body, g.Limits.MaxArchiveCompressed+1)
	counted := &countingReader{r: limited}

	gz, err := gzip.NewReader(counted)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry,
			"invalid archive")
	}
	defer gz.Close()

	a, err := extractSkill(tar.NewReader(gz), r.Path, g.Limits)
	if err != nil {
		return nil, err
	}
	if counted.n > g.Limits.MaxArchiveCompressed {
		return nil, errs.Usage(actionRepoTooBig,
			"archive exceeds %d bytes", g.Limits.MaxArchiveCompressed)
	}
	return a, nil
}

// extractSkill walks the archive and assembles the skill's set.
//
// Two guards are active during extraction, and both abort everything: the
// uncompressed-byte accumulator, and rejection of entries with unsafe paths.
// The size check happens during rather than after — checking at the end
// protects nothing, because by the time you know, the disk is already gone.
func extractSkill(tr *tar.Reader, skillPath string, lim Limits) (*artifact.Artifact, error) {
	skillPath = strings.Trim(path.Clean(skillPath), "/")

	var (
		total      int64
		doc        []byte
		docPath    string
		candidates = map[string][]byte{}
		resources  []artifact.Resource
		resTotal   int64
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry,
				"reading the archive")
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// pax-format metadata entries. GitHub includes a pax_global_header
			// in every tarball it generates. They are metadata, not content,
			// and pose no risk: skipped rather than aborting.
			continue
		case tar.TypeReg:
			// keep going
		default:
			// Symlink, hard link, device: abort everything. A source that ships
			// these is not a source whose remainder is worth keeping.
			return nil, errs.Usage(actionReportSkill,
				"archive contains an entry of a disallowed type: %q", hdr.Name)
		}

		clean, ok := sanitizeArchivePath(hdr.Name)
		if !ok {
			return nil, errs.Usage(actionReportSkill,
				"archive contains an unsafe path: %q", hdr.Name)
		}

		// GitHub packs everything under a single root directory; it is stripped.
		inner := stripFirstComponent(clean)
		if inner == "" {
			continue
		}

		rel, inside := relativeTo(inner, skillPath)

		// The accumulator counts EVERY entry, not just the kept ones — that is
		// what stops a small archive from expanding until the disk is full.
		remaining := lim.MaxArchiveUncompressed - total
		if remaining <= 0 {
			return nil, errs.Usage(actionRepoTooBig,
				msgUncompressedCap, lim.MaxArchiveUncompressed)
		}

		if !inside {
			n, err := io.CopyN(io.Discard, tr, remaining+1)
			total += n
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry,
					"reading an archive entry")
			}
			if total > lim.MaxArchiveUncompressed {
				return nil, errs.Usage(actionRepoTooBig,
					msgUncompressedCap, lim.MaxArchiveUncompressed)
			}
			continue
		}

		content, err := readLimitedCounted(tr, remaining, &total)
		if err != nil {
			return nil, errs.Usage(actionRepoTooBig,
				msgUncompressedCap, lim.MaxArchiveUncompressed)
		}

		switch {
		case rel == artifact.DocumentKey:
			doc, docPath = content, rel
		case !strings.Contains(rel, "/") && strings.HasSuffix(strings.ToLower(rel), ".md"):
			candidates[rel] = content
		default:
			resTotal += int64(len(content))
			if resTotal > lim.MaxResourcesTotal {
				return nil, errs.Usage(actionTooManyRes,
					"recursos auxiliares excedem %d bytes", lim.MaxResourcesTotal)
			}
			if len(resources) >= lim.MaxResourceCount {
				return nil, errs.Usage(actionTooManyRes,
					"skill tem mais de %d recursos auxiliares", lim.MaxResourceCount)
			}
			resources = append(resources, artifact.Resource{RelPath: rel, Content: content})
		}
	}

	// Tolerance: with no SKILL.md, accept a single markdown at the skill root.
	if doc == nil {
		if len(candidates) == 1 {
			for name, content := range candidates {
				doc, docPath = content, name
			}
		} else {
			return nil, errs.NotFound("check the reference: the directory needs a SKILL.md",
				"main document not found in %q", skillPath)
		}
	}
	if int64(len(doc)) > lim.MaxDocument {
		return nil, errs.Usage(actionTooLarge,
			msgDocTooLarge, lim.MaxDocument)
	}

	// Root markdowns that did not become the main document become resources.
	for name, content := range candidates {
		if name == docPath {
			continue
		}
		resources = append(resources, artifact.Resource{RelPath: name, Content: content})
	}

	a := &artifact.Artifact{Kind: artifact.KindSkill, Document: doc, Resources: resources}
	a.Normalize()
	return a, nil
}

// URLResolver fetches a skill from a standalone URL.
//
// A URL points at a file, and there is no directory to enumerate: skills
// fetched by URL never have supporting files.
type URLResolver struct {
	Client *http.Client
	Limits Limits
}

// NewURLResolver builds the URL resolver.
func NewURLResolver(client *http.Client, limits Limits) *URLResolver {
	return &URLResolver{Client: client, Limits: limits}
}

// Resolve fetches the document the URL points at.
func (u *URLResolver) Resolve(ctx context.Context, r ref.Ref) (*artifact.Artifact, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassUsage, actionCheckURL, "building the request")
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork,
			"check connectivity and try again", "reaching %s", r.URL)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errs.NotFound(actionCheckURL, "document not found: %s", r.URL)
	case resp.StatusCode >= 400:
		return nil, errs.Network("try again later",
			"source responded with status %d", resp.StatusCode)
	}

	doc, err := readLimited(resp.Body, u.Limits.MaxDocument)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassUsage,
			actionTooLarge,
			msgDocTooLarge, u.Limits.MaxDocument)
	}
	if len(doc) == 0 {
		return nil, errs.NotFound(actionCheckURL, "empty document at %s", r.URL)
	}
	return &artifact.Artifact{Kind: artifact.KindSkill, Document: doc}, nil
}

// --- helpers ---

// sanitizeArchivePath validates an archive entry. Returns false for absolute
// paths, directory escapes, or NUL bytes.
func sanitizeArchivePath(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return "", false
	}
	if strings.Contains(name, "\\") {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", false
		}
	}
	return clean, true
}

func stripFirstComponent(p string) string {
	i := strings.Index(p, "/")
	if i < 0 {
		return ""
	}
	return p[i+1:]
}

// relativeTo returns the path relative to a directory prefix.
func relativeTo(p, prefix string) (string, bool) {
	if prefix == "" || prefix == "." {
		return p, true
	}
	if p == prefix {
		return path.Base(p), true
	}
	if strings.HasPrefix(p, prefix+"/") {
		return strings.TrimPrefix(p, prefix+"/"), true
	}
	return "", false
}

// readLimited reads at most max bytes and fails if there are more.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("exceeds %d bytes", max)
	}
	return b, nil
}

func readLimitedCounted(r io.Reader, max int64, total *int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	*total += int64(len(b))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("exceeds %d bytes", max)
	}
	return b, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
