package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
)

// DefaultRegistryBase is the registry API base, validated against the real
// service: GET /api/search?q=&limit= answers JSON without authentication.
const DefaultRegistryBase = "https://skills.sh"

// Query describes a registry search.
type Query struct {
	Term  string
	Limit int
}

// Searcher is implemented only by the registry: the other sources have no
// searchable catalogue.
type Searcher interface {
	Search(ctx context.Context, q Query) ([]artifact.IndexEntry, error)
}

// RegistryResolver consumes the skills registry.
//
// The registry is a DISCOVERY INDEX, not a content host: every listed skill
// lives in a GitHub repository. That is why this resolver has no fetch path of
// its own — it locates the skill inside the repository and reuses the tarball
// machinery built for GitHub in full.
type RegistryResolver struct {
	Client  *http.Client
	Limits  Limits
	APIBase string
	GitHub  *GitHubResolver
}

// NewRegistryResolver builds the registry resolver.
// A nil gh is filled in rather than carried: the registry resolver reaches
// GitHub for every fetch, so a nil there is a panic waiting for whoever wires
// this next, not a configuration worth honouring.
func NewRegistryResolver(client *http.Client, limits Limits, gh *GitHubResolver) *RegistryResolver {
	if gh == nil {
		gh = NewGitHubResolver(client, limits)
	}
	return &RegistryResolver{
		Client:  client,
		Limits:  limits,
		APIBase: DefaultRegistryBase,
		GitHub:  gh,
	}
}

// searchResponse mirrors exactly what the API returns. No field is invented:
// the real response carries no description, and that is stated here.
type searchResponse struct {
	Query  string `json:"query"`
	Skills []struct {
		ID       string `json:"id"`
		SkillID  string `json:"skillId"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Installs int    `json:"installs"`
	} `json:"skills"`
}

// Search queries the registry.
func (rr *RegistryResolver) Search(ctx context.Context, q Query) ([]artifact.IndexEntry, error) {
	if strings.TrimSpace(q.Term) == "" {
		return nil, errs.Usage("provide a search term", "empty term")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	u := fmt.Sprintf("%s/api/search?q=%s&limit=%d",
		rr.APIBase, url.QueryEscape(q.Term), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassUsage, "check the search term", "building the request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := rr.Client.Do(req)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork,
			"check connectivity and try again", "querying the registry")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, errs.Network(actionRetry,
			"registry responded with status %d", resp.StatusCode)
	}

	raw, err := readLimited(resp.Body, 4<<20)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry,
			"registry response exceeds the limit")
	}

	var parsed searchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork,
			"the registry response format changed; update delivery",
			"could not interpret the registry response")
	}

	out := make([]artifact.IndexEntry, 0, len(parsed.Skills))
	for _, s := range parsed.Skills {
		if s.SkillID == "" || s.Source == "" {
			continue
		}
		out = append(out, artifact.IndexEntry{
			ID:       s.ID,
			SkillID:  s.SkillID,
			Name:     s.Name,
			Source:   s.Source,
			Installs: s.Installs,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Installs > out[j].Installs })
	return out, nil
}

// Resolve turns a registry identifier into the skill's content.
//
// The identifier is NOT a repository path: its last segment is the skill's
// directory name, which has to be located inside the repository. This was
// verified against the real repository — the identifier anthropics/skills/pdf
// maps to the path skills/pdf/SKILL.md, and no API route returns that path.
func (rr *RegistryResolver) Resolve(ctx context.Context, r ref.Ref) (*artifact.Artifact, error) {
	if r.Kind != ref.KindRegistry {
		return nil, errs.Usage("use a registry identifier", "unexpected reference kind")
	}

	u := fmt.Sprintf("%s/%s/%s/tar.gz/HEAD", rr.GitHub.CodeloadBase, r.Owner, r.Repo)
	body, err := rr.GitHub.get(ctx, u, r)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	limited := io.LimitReader(body, rr.Limits.MaxArchiveCompressed+1)
	counted := &countingReader{r: limited}

	gz, err := gzip.NewReader(counted)
	if err != nil {
		return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry, "invalid archive")
	}
	defer gz.Close()

	return extractByDirName(tar.NewReader(gz), r.SkillID, rr.Limits)
}

// extractByDirName locates, inside the archive, the directory named skillID
// that contains a main document.
//
// Ambiguity becomes an error rather than an automatic choice: two directories
// with the same name in different subtrees are different skills, and picking
// one by heuristic would make delivery approve content the person never
// asked for.
func extractByDirName(tr *tar.Reader, skillID string, lim Limits) (*artifact.Artifact, error) {
	buckets := map[string]map[string][]byte{}
	var total int64

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
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeReg:
			// keep going
		default:
			return nil, errs.Usage(actionReportSkill,
				"archive contains an entry of a disallowed type: %q", hdr.Name)
		}

		clean, ok := sanitizeArchivePath(hdr.Name)
		if !ok {
			return nil, errs.Usage(actionReportSkill,
				"archive contains an unsafe path: %q", hdr.Name)
		}
		inner := stripFirstComponent(clean)
		if inner == "" {
			continue
		}

		remaining := lim.MaxArchiveUncompressed - total
		if remaining <= 0 {
			return nil, errs.Usage(actionRepoTooBig, msgUncompressedCap, lim.MaxArchiveUncompressed)
		}

		root := matchingAncestor(path.Dir(inner), skillID)
		if root == "" {
			n, err := io.CopyN(io.Discard, tr, remaining+1)
			total += n
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, errs.Wrap(err, errs.ClassNetwork, actionRetry,
					"reading an archive entry")
			}
			if total > lim.MaxArchiveUncompressed {
				return nil, errs.Usage(actionRepoTooBig, msgUncompressedCap, lim.MaxArchiveUncompressed)
			}
			continue
		}

		content, err := readLimitedCounted(tr, remaining, &total)
		if err != nil {
			return nil, errs.Usage(actionRepoTooBig, msgUncompressedCap, lim.MaxArchiveUncompressed)
		}
		if buckets[root] == nil {
			buckets[root] = map[string][]byte{}
		}
		buckets[root][strings.TrimPrefix(inner, root+"/")] = content
	}

	candidates := make([]string, 0, len(buckets))
	for dir, files := range buckets {
		if hasMainDocument(files) {
			candidates = append(candidates, dir)
		}
	}
	sort.Strings(candidates)

	switch len(candidates) {
	case 0:
		return nil, errs.NotFound(
			"check the identifier, or use the gh:owner/repo/path form",
			"the repository has no skill directory named %q", skillID)
	case 1:
		return buildFromFiles(buckets[candidates[0]], lim)
	default:
		var b strings.Builder
		for _, c := range candidates {
			fmt.Fprintf(&b, "\n  gh:<owner>/<repo>/%s", c)
		}
		return nil, errs.Usage(
			"pick one of these paths using the explicit gh: form:"+b.String(),
			"the repository has %d directories named %q", len(candidates), skillID)
	}
}

// matchingAncestor returns the nearest ancestor whose final name is skillID.
func matchingAncestor(dir, skillID string) string {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if path.Base(d) == skillID {
			return d
		}
	}
	return ""
}

func hasMainDocument(files map[string][]byte) bool {
	if _, ok := files[artifact.DocumentKey]; ok {
		return true
	}
	n := 0
	for name := range files {
		if !strings.Contains(name, "/") && strings.HasSuffix(strings.ToLower(name), ".md") {
			n++
		}
	}
	return n == 1
}

// buildFromFiles assembles the set from a skill directory's files, applying
// the same ceilings as the U1 path.
func buildFromFiles(files map[string][]byte, lim Limits) (*artifact.Artifact, error) {
	var doc []byte
	docName := ""

	if d, ok := files[artifact.DocumentKey]; ok {
		doc, docName = d, artifact.DocumentKey
	} else {
		for name, content := range files {
			if !strings.Contains(name, "/") && strings.HasSuffix(strings.ToLower(name), ".md") {
				doc, docName = content, name
				break
			}
		}
	}
	if doc == nil {
		return nil, errs.NotFound("check the reference", "main document not found")
	}
	if int64(len(doc)) > lim.MaxDocument {
		return nil, errs.Usage(actionTooLarge, msgDocTooLarge, lim.MaxDocument)
	}

	var (
		resources []artifact.Resource
		resTotal  int64
	)
	names := make([]string, 0, len(files))
	for name := range files {
		if name != docName {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		content := files[name]
		resTotal += int64(len(content))
		if resTotal > lim.MaxResourcesTotal {
			return nil, errs.Usage(actionTooManyRes,
				"supporting files exceed %d bytes", lim.MaxResourcesTotal)
		}
		if len(resources) >= lim.MaxResourceCount {
			return nil, errs.Usage(actionTooManyRes,
				"skill has more than %d supporting files", lim.MaxResourceCount)
		}
		resources = append(resources, artifact.Resource{RelPath: name, Content: content})
	}

	a := &artifact.Artifact{Kind: artifact.KindSkill, Document: doc, Resources: resources}
	a.Normalize()
	return a, nil
}
