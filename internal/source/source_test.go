package source_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/source"
)

// archiveEntry describes an entry to pack in the tests.
type archiveEntry struct {
	name     string
	content  string
	typeflag byte
	linkname string
}

// buildTarGz builds an archive in the shape GitHub serves: everything under a
// single root directory.
func buildTarGz(t *testing.T, root string, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		name := e.name
		if root != "" && !strings.HasPrefix(name, "/") {
			name = root + "/" + name
		}
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(e.content)),
			Typeflag: flag,
			Linkname: e.linkname,
		}
		if flag != tar.TypeReg {
			hdr.Size = 0
		}
		if flag == tar.TypeXGlobalHeader {
			// The standard library writer demands this shape for the global
			// metadata entry — which is exactly the one GitHub emits.
			hdr.Name = e.name
			hdr.Mode = 0
			hdr.Format = tar.FormatPAX
			hdr.PAXRecords = map[string]string{"comment": e.content}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing header %q: %v", name, err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("writing content %q: %v", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newResolver(t *testing.T, handler http.Handler, limits source.Limits) (*source.GitHubResolver, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	r := source.NewGitHubResolver(srv.Client(), limits)
	r.RawBase = srv.URL
	r.CodeloadBase = srv.URL
	return r, srv.Close
}

func mustParse(t *testing.T, raw string) ref.Ref {
	t.Helper()
	r, err := ref.Parse(raw)
	if err != nil {
		t.Fatalf("invalid test reference %q: %v", raw, err)
	}
	return r
}

// P-15 — Round-trip: extracting an archive built from a known set returns
// exactly that set.
func TestArchiveExtractionRoundTrip(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "README.md", content: "outside the skill"},
		{name: "pkg/my-skill/SKILL.md", content: "# My Skill\nbody"},
		{name: "pkg/my-skill/references/guide.md", content: "supporting guide"},
		{name: "pkg/my-skill/scripts/run.sh", content: "echo hi"},
		{name: "pkg/other-skill/SKILL.md", content: "must not appear"},
	})

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}), source.DefaultLimits())
	defer closeFn()

	a, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}

	if got := string(a.Document); got != "# My Skill\nbody" {
		t.Fatalf("main document differs: %q", got)
	}
	if len(a.Resources) != 2 {
		t.Fatalf("resources = %d, want 2: %+v", len(a.Resources), a.Resources)
	}
	want := map[string]string{
		"references/guide.md": "supporting guide",
		"scripts/run.sh":      "echo hi",
	}
	for _, r := range a.Resources {
		if want[r.RelPath] != string(r.Content) {
			t.Fatalf("resource %q differs: %q", r.RelPath, r.Content)
		}
		delete(want, r.RelPath)
	}
	if len(want) != 0 {
		t.Fatalf("missing resources: %v", want)
	}
}

// P-15 (security invariant) — entries with a path escape abort the whole
// extraction rather than being skipped.
func TestArchiveWithPathEscapeIsRejected(t *testing.T) {
	cases := []struct {
		name  string
		entry archiveEntry
	}{
		{"escape with ..", archiveEntry{name: "../../../etc/passwd", content: "x"}},
		{"absolute path", archiveEntry{name: "/etc/passwd", content: "x"}},
		{"symlink", archiveEntry{name: "pkg/my-skill/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		{"hard link", archiveEntry{name: "pkg/my-skill/dur", typeflag: tar.TypeLink, linkname: "/etc/passwd"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
				{name: "pkg/my-skill/SKILL.md", content: "# ok"},
				tc.entry,
			})

			res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write(archive)
			}), source.DefaultLimits())
			defer closeFn()

			_, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
			if err == nil {
				t.Fatal("an unsafe entry was accepted")
			}
			if class, ok := errs.ClassOf(err); !ok || class != errs.ClassUsage {
				t.Fatalf("unexpected error class: %v", err)
			}
		})
	}
}

// Regression: every tarball GitHub generates starts with a pax_global_header.
// The first version treated any type other than a regular file as an attack
// and aborted, which broke against 100% of real repositories. The bug only
// surfaced in the smoke test against the real GitHub — no synthetic test would
// have found it, because none of them generated the entry.
func TestPaxGlobalHeaderIsIgnored(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "/pax_global_header", content: "52 comment=abc123\n", typeflag: tar.TypeXGlobalHeader},
		{name: "pkg/my-skill/SKILL.md", content: "# Works"},
	})

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}), source.DefaultLimits())
	defer closeFn()

	a, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
	if err != nil {
		t.Fatalf("pax_global_header made extraction fail: %v", err)
	}
	if string(a.Document) != "# Works" {
		t.Fatalf("document differs: %q", a.Document)
	}
}

// The uncompressed-size guard interrupts extraction rather than examining it
// after the data has already been consumed.
func TestArchiveExceedingUncompressedLimitIsRejected(t *testing.T) {
	big := strings.Repeat("a", 200_000)
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "pkg/my-skill/SKILL.md", content: "# ok"},
		{name: "pkg/my-skill/grande.txt", content: big},
	})

	limits := source.DefaultLimits()
	limits.MaxArchiveUncompressed = 50_000

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}), limits)
	defer closeFn()

	_, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
	if err == nil {
		t.Fatal("an archive over the uncompressed limit was accepted")
	}
	if !strings.Contains(err.Error(), "uncompressed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A document over the limit is refused.
func TestDocumentExceedingLimitIsRejected(t *testing.T) {
	limits := source.DefaultLimits()
	limits.MaxDocument = 100

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 500)))
	}), limits)
	defer closeFn()

	_, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/big.md"))
	if err == nil {
		t.Fatal("a document over the limit was accepted")
	}
}

// Absence at the source is NotFound, not a network failure — the agent has to
// tell the two apart.
func TestMissingSkillIsNotFound(t *testing.T) {
	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}), source.DefaultLimits())
	defer closeFn()

	_, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/missing.md"))
	if class, ok := errs.ClassOf(err); !ok || class != errs.ClassNotFound {
		t.Fatalf("error class = %v, want ClassNotFound (%v)", err, errs.ClassNotFound)
	}
}

// A directory with no main document is NotFound, with a useful action.
func TestDirectoryWithoutMainDocumentIsNotFound(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "pkg/my-skill/a.md", content: "one"},
		{name: "pkg/my-skill/b.md", content: "another"},
	})

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}), source.DefaultLimits())
	defer closeFn()

	_, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
	if class, ok := errs.ClassOf(err); !ok || class != errs.ClassNotFound {
		t.Fatalf("error class = %v, want ClassNotFound", err)
	}
	if errs.ActionOf(err) == "" {
		t.Error("error has no corrective action")
	}
}

// A single markdown at the skill root is accepted as the main document.
func TestSingleMarkdownIsAcceptedAsMainDocument(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "pkg/my-skill/instrucoes.md", content: "# Only one"},
		{name: "pkg/my-skill/data/table.json", content: "{}"},
	})

	res, closeFn := newResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}), source.DefaultLimits())
	defer closeFn()

	a, err := res.Resolve(context.Background(), mustParse(t, "gh:acme/skills/pkg/my-skill"))
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if string(a.Document) != "# Only one" {
		t.Fatalf("main document differs: %q", a.Document)
	}
	if len(a.Resources) != 1 || a.Resources[0].RelPath != "data/table.json" {
		t.Fatalf("unexpected resources: %+v", a.Resources)
	}
}

// A standalone URL never carries supporting files.
func TestURLResolverNeverHasResources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("# Skill from a URL"))
	}))
	defer srv.Close()

	r := ref.Ref{Kind: ref.KindURL, URL: srv.URL + "/skill.md", Raw: srv.URL + "/skill.md"}
	a, err := source.NewURLResolver(srv.Client(), source.DefaultLimits()).Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if len(a.Resources) != 0 {
		t.Fatalf("the URL returned resources: %+v", a.Resources)
	}
}

// The dispatcher refuses source kinds that are not supported yet.
func TestDispatcherRejectsUnsupportedKind(t *testing.T) {
	d := source.NewDispatcher()
	_, err := d.Resolve(context.Background(), ref.Ref{Kind: ref.KindRegistry})
	if class, ok := errs.ClassOf(err); !ok || class != errs.ClassUsage {
		t.Fatalf("unexpected error: %v", err)
	}
}
