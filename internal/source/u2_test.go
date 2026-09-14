package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/source"
)

// --- registry ---

const searchPayload = `{"query":"pdf","searchType":"fuzzy","searchVersion":"legacy",
"skills":[
 {"id":"anthropics/skills/pdf","skillId":"pdf","name":"pdf","installs":194278,"source":"anthropics/skills"},
 {"id":"openai/skills/pdf","skillId":"pdf","name":"pdf","installs":12283,"source":"openai/skills"}
]}`

func TestRegistrySearchParsesRealShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("q") == "" {
			t.Error("search issued without a term")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(searchPayload))
	}))
	defer srv.Close()

	rr := source.NewRegistryResolver(srv.Client(), source.DefaultLimits(), nil)
	rr.APIBase = srv.URL

	entries, err := rr.Search(context.Background(), source.Query{Term: "pdf", Limit: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("results = %d, want 2", len(entries))
	}
	if entries[0].Installs < entries[1].Installs {
		t.Error("results did not come sorted by popularity")
	}
	if entries[0].ID != "anthropics/skills/pdf" || entries[0].SkillID != "pdf" {
		t.Fatalf("first result differs: %+v", entries[0])
	}
	// The registry provides no description — the field does not exist on the
	// returned type, and that is deliberate: inventing one would be worse than
	// having none.
}

func TestRegistrySearchRequiresTerm(t *testing.T) {
	rr := source.NewRegistryResolver(http.DefaultClient, source.DefaultLimits(), nil)
	if _, err := rr.Search(context.Background(), source.Query{Term: "  "}); err == nil {
		t.Fatal("an empty term was accepted")
	}
}

// The registry identifier is NOT a path: resolution locates the directory
// named skillId inside the repository.
func TestRegistryResolvesByDirectoryName(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "/pax_global_header", content: "52 comment=x\n", typeflag: 'g'},
		{name: "README.md", content: "raiz"},
		{name: "skills/pdf/SKILL.md", content: "# PDF"},
		{name: "skills/pdf/reference.md", content: "reference"},
		{name: "skills/docx/SKILL.md", content: "# DOCX"},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	gh := source.NewGitHubResolver(srv.Client(), source.DefaultLimits())
	gh.CodeloadBase = srv.URL
	rr := source.NewRegistryResolver(srv.Client(), source.DefaultLimits(), gh)

	r := mustParse(t, "anthropics/skills/pdf")
	if r.Kind != ref.KindRegistry {
		t.Fatalf("kind = %v, want registry", r.Kind)
	}

	a, err := rr.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if string(a.Document) != "# PDF" {
		t.Fatalf("document differs: %q", a.Document)
	}
	if len(a.Resources) != 1 || a.Resources[0].RelPath != "reference.md" {
		t.Fatalf("unexpected resources: %+v", a.Resources)
	}
}

func TestRegistryUnknownSkillIsNotFound(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "skills/pdf/SKILL.md", content: "# PDF"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	gh := source.NewGitHubResolver(srv.Client(), source.DefaultLimits())
	gh.CodeloadBase = srv.URL
	rr := source.NewRegistryResolver(srv.Client(), source.DefaultLimits(), gh)

	_, err := rr.Resolve(context.Background(), mustParse(t, "anthropics/skills/missing"))
	if class, ok := errs.ClassOf(err); !ok || class != errs.ClassNotFound {
		t.Fatalf("class = %v, want NotFound", err)
	}
}

// Ambiguity becomes an error rather than an automatic choice: two directories
// with the same name are different skills, and picking one by heuristic would
// make delivery approve content the person never asked for.
func TestRegistryAmbiguousDirectoryListsCandidates(t *testing.T) {
	archive := buildTarGz(t, "skills-HEAD", []archiveEntry{
		{name: "a/pdf/SKILL.md", content: "# um"},
		{name: "b/pdf/SKILL.md", content: "# outro"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	gh := source.NewGitHubResolver(srv.Client(), source.DefaultLimits())
	gh.CodeloadBase = srv.URL
	rr := source.NewRegistryResolver(srv.Client(), source.DefaultLimits(), gh)

	_, err := rr.Resolve(context.Background(), mustParse(t, "acme/repo/pdf"))
	if err == nil {
		t.Fatal("ambiguity was resolved automatically")
	}
	if class, _ := errs.ClassOf(err); class != errs.ClassUsage {
		t.Fatalf("class = %v, want Usage", err)
	}
	action := errs.ActionOf(err)
	if !strings.Contains(action, "a/pdf") || !strings.Contains(action, "b/pdf") {
		t.Fatalf("candidates not listed in the action: %q", action)
	}
}

// --- local source ---

func TestLocalResolverReadsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skill.md")
	if err := os.WriteFile(p, []byte("# Local"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := source.NewLocalResolver(source.DefaultLimits()).
		Resolve(context.Background(), mustParse(t, p))
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if string(a.Document) != "# Local" {
		t.Fatalf("document differs: %q", a.Document)
	}
	if len(a.Resources) != 0 {
		t.Fatal("a standalone file should have no resources")
	}
}

func TestLocalResolverReadsDirectory(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(filepath.Join(skill, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# Mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "refs", "guide.md"), []byte("guide"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := source.NewLocalResolver(source.DefaultLimits()).
		Resolve(context.Background(), mustParse(t, skill))
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if string(a.Document) != "# Mine" {
		t.Fatalf("document differs: %q", a.Document)
	}
	if len(a.Resources) != 1 || a.Resources[0].RelPath != "refs/guide.md" {
		t.Fatalf("unexpected resources: %+v", a.Resources)
	}
}

// P-22 — Containment is validated against the REAL path, after resolving
// symlinks. Validating only the textual form would miss a symlink inside the
// directory pointing outside it.
func TestLocalDirectoryRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()

	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("content that must not leak"), 0o600); err != nil {
		t.Fatal(err)
	}

	skill := filepath.Join(base, "skill")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# S"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(skill, "leak.txt")); err != nil {
		t.Skipf("the system does not allow symlinks: %v", err)
	}

	_, err := source.NewLocalResolver(source.DefaultLimits()).
		Resolve(context.Background(), mustParse(t, skill))
	if err == nil {
		t.Fatal("a symlink pointing outside the directory was accepted")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLocalMissingPathIsNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := source.NewLocalResolver(source.DefaultLimits()).
		Resolve(context.Background(), mustParse(t, filepath.Join(dir, "missing")))
	if class, ok := errs.ClassOf(err); !ok || class != errs.ClassNotFound {
		t.Fatalf("class = %v, want NotFound", err)
	}
}
