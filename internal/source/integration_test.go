package source_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/source"
)

// Integration tests run against the real registry and GitHub. They are opt-in,
// because CI must not fail when someone else's repository changes — but they
// exist committed, and runnable, because the two failures that actually hurt
// this project were both found by fetching something real: GitHub's pax global
// header, and resource ceilings set low enough to reject published skills.
//
//	DELIVERY_INTEGRATION=1 go test ./internal/source/ -run TestAgainstRealRegistry
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("DELIVERY_INTEGRATION") == "" {
		t.Skip("set DELIVERY_INTEGRATION=1 to run tests that reach the network")
	}
}

func realResolver() *source.RegistryResolver {
	client := &http.Client{Timeout: 90 * time.Second}
	lim := source.DefaultLimits()
	return source.NewRegistryResolver(client, lim, source.NewGitHubResolver(client, lim))
}

// The default ceilings have to accommodate what is actually published. They
// did not: 50 files and 5 MB rejected docx, pptx, xlsx and canvas-design, all
// of them official skills. This pins the sizes that forced the correction, so
// tightening the limits again fails here rather than in someone's terminal.
func TestAgainstRealRegistryDefaultLimitsFitPublishedSkills(t *testing.T) {
	requireIntegration(t)

	// Floors sit below what each skill measured when this was written, so an
	// upstream edit does not fail the build while a silent drop in extraction
	// still does. Written as plain numbers rather than shifted constants: the
	// first attempt used 10 << 10 against a skill of 10,174 bytes and failed on
	// the rounding, not on anything real.
	cases := []struct {
		id            string
		minResources  int
		minTotalBytes int64 // measured
	}{
		{"anthropics/skills/frontend-design", 1, 8_000}, // 10,174
		{"anthropics/skills/pdf", 11, 40_000},           // 50,620
		{"anthropics/skills/claude-api", 37, 700_000},   // 870,422
		{"anthropics/skills/xlsx", 52, 900_000},         // 1,094,295
		{"anthropics/skills/docx", 60, 900_000},         // 1,121,784
		// The largest published skill: it ships fonts, and at 5.5 MB it is what
		// proved the old 5 MB ceiling wrong.
		{"anthropics/skills/canvas-design", 82, 5_000_000}, // 5,542,064
	}

	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			r, err := ref.Parse(c.id)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			a, err := realResolver().Resolve(ctx, r)
			if err != nil {
				t.Fatalf("the default limits reject a published skill: %v", err)
			}

			var total int64
			for _, res := range a.Resources {
				total += int64(len(res.Content))
			}
			if len(a.Resources) < c.minResources {
				t.Errorf("fetched %d resources, expected at least %d — the skill shrank, or extraction is dropping files",
					len(a.Resources), c.minResources)
			}
			if total < c.minTotalBytes {
				t.Errorf("fetched %d bytes of resources, expected at least %d", total, c.minTotalBytes)
			}
			if len(a.Document) == 0 {
				t.Error("no SKILL.md in the fetched set")
			}
		})
	}
}

// Supporting files keep the layout they were published with, several levels
// deep. Flattening them would break every relative reference in the body,
// which is exactly what progressive disclosure relies on.
func TestAgainstRealRegistryNestedResourcePathsSurvive(t *testing.T) {
	requireIntegration(t)

	r, err := ref.Parse("anthropics/skills/docx")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	a, err := realResolver().Resolve(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	depth := 0
	for _, res := range a.Resources {
		if strings.HasPrefix(res.RelPath, "/") || strings.Contains(res.RelPath, "..") {
			t.Errorf("resource path is not safely relative: %q", res.RelPath)
		}
		if d := strings.Count(res.RelPath, "/"); d > depth {
			depth = d
		}
	}
	if depth < 3 {
		t.Errorf("deepest resource is %d levels down, expected at least 3 — nesting is being flattened", depth)
	}
}
