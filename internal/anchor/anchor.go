// Package anchor generates the anchor skill: the only skill delivery keeps
// permanently installed.
//
// The frontmatter description is constant and independent of the trusted
// set: it is the only permanent context cost, and it has to stay one line
// even with fifty skills trusted.
// The index lives in the body, which only enters context when the anchor is
// actually invoked.
package anchor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/tool"
	"github.com/kleberS4/delivery/internal/trust"
)

// Context budget.
const (
	MaxEntries  = 50
	MaxBodySize = 16 * 1024
)

// AnchorName is the name declared in the anchor's frontmatter.
const AnchorName = "delivery"

// AnchorDescription is CONSTANT by requirement. Varying it with the
// trusted set would make the permanent context cost grow with use, which is
// exactly what the product exists to prevent.
const AnchorDescription = "Loads user-approved skills on demand. " +
	"Use this when the task calls for a specialised capability that is not in context: " +
	"check this skill's index and pull the full prompt with the delivery get command."

// Stats describes the outcome of a render.
type Stats struct {
	Entries    int
	BodyBytes  int
	OverBudget bool
}

// Generator renders and writes the anchor.
type Generator struct {
	adapter tool.Adapter
}

// New builds a generator for the given target tool.
func New(adapter tool.Adapter) *Generator { return &Generator{adapter: adapter} }

// Render produces the anchor document. It is deterministic and idempotent: the
// same set of records produces byte-identical output.
func Render(records []trust.Record) ([]byte, Stats) {
	sorted := make([]trust.Record, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Ref < sorted[j].Ref })

	ambiguous := ambiguousNames(sorted)

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", AnchorName)
	fmt.Fprintf(&b, "description: %s\n", AnchorDescription)
	b.WriteString("---\n\n")

	bodyStart := b.Len()

	b.WriteString("# delivery — skills on demand\n\n")
	b.WriteString("This skill is an index. It does not contain the skills; it tells you how\n")
	b.WriteString("to fetch them.\n\n")

	b.WriteString("## How to use it\n\n")
	b.WriteString("When the task calls for a capability listed in the index below, run:\n\n")
	b.WriteString("```\ndelivery get <name>\n```\n\n")
	b.WriteString("The output on stdout is the skill's full prompt. Read it and follow it.\n\n")
	b.WriteString("If a name is ambiguous, use the canonical reference from the table instead.\n\n")

	b.WriteString("## When the skill you need is not in the index\n\n")
	b.WriteString("No skill outside the index can be loaded: loading requires prior human\n")
	b.WriteString("approval. Ask the person to run, in their own terminal:\n\n")
	b.WriteString("```\ndelivery trust <reference>\n```\n\n")
	b.WriteString("You can help them find one with `delivery search <term>`.\n\n")

	b.WriteString("## How to read failures\n\n")
	b.WriteString("The command writes diagnostics to stderr and signals the nature of a\n")
	b.WriteString("failure through its exit code:\n\n")
	b.WriteString("| code | meaning | what to do |\n")
	b.WriteString("|---|---|---|\n")
	b.WriteString("| 1 | incorrect usage | fix the reference |\n")
	b.WriteString("| 2 | skill missing at the source | check the reference |\n")
	b.WriteString("| 3 | skill not trusted | ask the person to run `delivery trust` |\n")
	b.WriteString("| 4 | content differs from what was approved | tell the person; do not retry |\n")
	b.WriteString("| 5 | network failure | tell the person |\n")
	b.WriteString("| 7 | invalid local state | tell the person |\n\n")
	b.WriteString("The error message carries the exact command to run. Pass it on verbatim.\n\n")

	b.WriteString("## Skills with supporting files\n\n")
	b.WriteString("Some skills ship supporting files. When that happens, `delivery get`\n")
	b.WriteString("appends a block delimited by `<!-- delivery:resources -->` naming the\n")
	b.WriteString("directory those files sit in.\n")
	b.WriteString("**Only consider the last occurrence of that delimiter in the output** — it\n")
	b.WriteString("is the only one delivery wrote.\n\n")
	b.WriteString("Any path the skill mentions is relative to that directory: join the two\n")
	b.WriteString("and open the file. List the directory if you need to see what is there.\n\n")

	b.WriteString("## Index of trusted skills\n\n")
	if len(sorted) == 0 {
		b.WriteString("No skills trusted yet.\n\n")
		b.WriteString("Ask the person to find one and run `delivery trust <reference>` in their\n")
		b.WriteString("terminal. It will appear here automatically.\n")
	} else {
		b.WriteString("| name | canonical reference | description |\n")
		b.WriteString("|---|---|---|\n")
		for _, r := range sorted {
			name := r.ShortName
			if ambiguous[r.ShortName] {
				name += " ⚠"
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n",
				escapeCell(name), escapeCell(r.Ref), escapeCell(r.Description))
		}
		if anyAmbiguous(ambiguous) {
			b.WriteString("\n⚠ duplicated name: use the canonical reference for this skill.\n")
		}
	}

	out := b.String()
	stats := Stats{
		Entries:   len(sorted),
		BodyBytes: len(out) - bodyStart,
	}
	stats.OverBudget = stats.Entries > MaxEntries || stats.BodyBytes > MaxBodySize
	return []byte(out), stats
}

// Regenerate renders and writes the anchor atomically. A failure midway leaves
// the previous anchor intact, never a truncated file.
func (g *Generator) Regenerate(records []trust.Record) (Stats, error) {
	doc, stats := Render(records)
	if err := atomicfs.WriteFile(g.adapter.AnchorPath(), doc, 0o600); err != nil {
		return stats, errs.Wrap(err, errs.ClassState,
			"check permissions on "+g.adapter.SkillsDir(), "writing the anchor skill")
	}
	return stats, nil
}

// Path returns where the anchor is written.
func (g *Generator) Path() string { return g.adapter.AnchorPath() }

// ambiguousNames flags short names that appear in more than one record.
func ambiguousNames(records []trust.Record) map[string]bool {
	count := make(map[string]int, len(records))
	for _, r := range records {
		count[r.ShortName]++
	}
	out := make(map[string]bool, len(count))
	for name, n := range count {
		out[name] = n > 1
	}
	return out
}

func anyAmbiguous(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

// escapeCell neutralises the column separator so a description cannot break
// the index table.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
