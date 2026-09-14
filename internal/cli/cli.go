// Package cli is the only layer in the system that touches the terminal.
//
// Pattern P1: exactly one function writes to the payload channel —
// writeStdout. Everything else goes to stderr. That makes the output contract
// verifiable by inspecting a single place, instead of by auditing the whole
// codebase.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/kleberS4/delivery/internal/anchor"
	"github.com/kleberS4/delivery/internal/cache"
	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/ref"
	"github.com/kleberS4/delivery/internal/service"
	"github.com/kleberS4/delivery/internal/source"
	"github.com/kleberS4/delivery/internal/tool"
	"github.com/kleberS4/delivery/internal/trust"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Version is set at build time.
var Version = "0.1.0-dev"

// EnvRegistryBase points delivery at a different registry. Useful for
// self-hosted mirrors, and it is what makes the search path testable offline.
const EnvRegistryBase = "DELIVERY_REGISTRY_BASE"

// Exit codes, one per failure class. These are a stable contract: the agent
// reacts to them programmatically, and changing them breaks the consumer.
const (
	ExitOK        = 0
	ExitUsage     = 1
	ExitNotFound  = 2
	ExitNotTrust  = 3
	ExitIntegrity = 4
	ExitNetwork   = 5
	ExitTTY       = 6
	ExitState     = 7
)

// Delimiters of the resource block appended by get.
const (
	resourcesOpen  = "<!-- delivery:resources -->"
	resourcesClose = "<!-- /delivery:resources -->"
)

type env struct {
	stdout  io.Writer
	stderr  io.Writer
	log     *slog.Logger
	style   styler
	verbose bool
	noColor bool
	tool    string
}

// Execute is the command-line entry point. Writers come in as parameters,
// which is what makes this layer testable without a terminal.
func Execute(args []string, stdout, stderr io.Writer) int {
	e := &env{stdout: stdout, stderr: stderr}

	root := &cobra.Command{
		Use:           "delivery",
		Short:         "Load skills on demand for AI agents",
		Long:          longHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		Args:          cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			e.style = newStyler(stderr, e.noColor)
		},
		// Bare `delivery` is an orientation moment, not an error. It shows what
		// the tool is and what to run next, instead of dumping a flag list.
		RunE: func(_ *cobra.Command, _ []string) error {
			showBanner(e)
			return nil
		},
	}
	root.SetOut(stderr) // help and usage go to stderr, never to stdout
	root.SetErr(stderr)
	root.SetArgs(args)

	root.PersistentFlags().BoolVar(&e.verbose, "verbose", false,
		"detailed diagnostics on stderr")
	root.PersistentFlags().BoolVar(&e.noColor, "no-color", false,
		"disable coloured output")
	root.PersistentFlags().StringVar(&e.tool, "tool", "claude",
		"target tool: "+strings.Join(tool.Supported(), ", "))

	root.AddCommand(
		newInitCmd(e), newTrustCmd(e), newGetCmd(e),
		newSearchCmd(e), newUntrustCmd(e),
		newInstallCmd(e), newUninstallCmd(e),
		newUpdateCmd(e), newListCmd(e),
	)

	// The styler is needed even when argument parsing fails, which happens
	// before PersistentPreRun would have run.
	e.style = newStyler(stderr, e.noColor)

	if err := root.Execute(); err != nil {
		return reportError(e, err)
	}
	return ExitOK
}

const longHelp = `delivery loads skills on demand for AI agents.

The flow has four steps:

  1. find the skill you need
  2. delivery trust <reference>   you approve the content, in your terminal
  3. the skill enters the anchor skill's index, automatically
  4. delivery get <name>          the agent loads the prompt on its own

Only content you explicitly approved ever reaches your agent.

References:
  owner/repo/id                 registry identifier (copied from search)
  gh:owner/repo/path[@ref]      GitHub repository
  https://...                   markdown at a URL
  ./path                        local file or directory`

// --- init ---

func newInitCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up local state and install the anchor skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			rep, err := service.NewInitService(deps).Init(cmd.Context())
			if err != nil {
				return err
			}
			s := e.style

			diag(e, "")
			diag(e, "%s delivery is ready for %s", s.green("✓"), s.bold(rep.Tool))
			diag(e, "")
			diag(e, "%s", s.field("state", s.dim(rep.StateRoot), 7))
			diag(e, "%s", s.field("trust", s.dim(rep.TrustFile), 7))
			diag(e, "%s", s.field("cache", s.dim(rep.CacheDir), 7))
			diag(e, "%s", s.field("anchor", s.dim(rep.AnchorPath), 7))
			diag(e, "")

			if rep.TrustedSkills == 0 {
				diag(e, "  No skills trusted yet. Start with:")
				diag(e, "    %s", s.cmd("delivery search <term>"))
				diag(e, "    %s", s.cmd("delivery trust <reference>"))
			} else {
				diag(e, "  %s skill(s) in the index", s.bold(fmt.Sprint(rep.TrustedSkills)))
			}
			diag(e, "")
			warnBudget(e, rep.AnchorStats)
			return nil
		},
	}
}

// --- trust ---

func newTrustCmd(e *env) *cobra.Command {
	var name, description string
	var yes, asSource bool

	cmd := &cobra.Command{
		Use:   "trust <reference>",
		Short: "Review and approve a skill (requires an interactive terminal)",
		Long: `Show the skill's content and record your approval.

Requires an interactive terminal: trust is always a deliberate human
decision. Without a terminal the command fails rather than approving
silently.

What gets recorded is the hash of the specific content you saw — not the
skill's name. If the origin changes later, delivery refuses to hand over
the new content until you approve it again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asSource {
				return runTrustSource(e, cmd, args[0], yes)
			}
			// TTY requirement checked before any work happens.
			if !isTerminal(os.Stdin) {
				return errs.TTYRequired(
					"run this in an interactive terminal: delivery trust "+args[0],
					"trust requires an interactive terminal")
			}

			deps, err := build(e)
			if err != nil {
				return err
			}
			svc := service.NewTrustService(deps)

			p, err := svc.Preview(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			presentPreview(e, p)

			if !yes {
				ok, err := confirm(e, "Trust this skill?")
				if err != nil {
					return err
				}
				if !ok {
					diag(e, "%s nothing changed.", e.style.grey("·"))
					return nil
				}
			}

			stats, err := svc.Confirm(cmd.Context(), p, service.ConfirmOptions{
				Name: name, Description: description,
			})
			if err != nil {
				return err
			}

			final := p.Meta.Name
			if name != "" {
				final = name
			}
			s := e.style
			diag(e, "")
			diag(e, "%s trusted. the agent can now load it with %s",
				s.green("✓"), s.cmd("delivery get "+final))
			warnBudget(e, stats)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "override the short name in the index")
	cmd.Flags().StringVar(&description, "description", "", "override the description in the index")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false,
		"skip the confirmation prompt (still requires a terminal)")
	cmd.Flags().BoolVar(&asSource, "source", false,
		"trust a whole repository, including whatever it publishes later")
	return cmd
}

func presentPreview(e *env, p *service.Preview) {
	s := e.style
	const w = 11

	diag(e, "")
	diag(e, "%s", s.heading("Skill to approve"))
	diag(e, "")
	diag(e, "%s", s.field("reference", s.ref(p.Ref.Canonical()), w))
	diag(e, "%s", s.field("name", s.bold(p.Meta.Name), w))
	diag(e, "%s", s.field("description", p.Meta.Description, w))
	diag(e, "%s", s.field("metadata", s.dim(p.Meta.Origin.String()), w))
	diag(e, "%s", s.field("hash", s.dim(truncateMiddle(p.SetHash, 32)), w))
	diag(e, "%s", s.field("document",
		fmt.Sprintf("%s bytes", thousands(len(p.Artifact.Document))), w))

	if n := len(p.Artifact.Resources); n > 0 {
		diag(e, "%s", s.field("resources",
			fmt.Sprintf("%d file(s), %s bytes", n, thousands(p.Artifact.TotalResourceSize())), w))
		for _, r := range p.Artifact.Resources {
			diag(e, "               %s", s.dim(r.RelPath))
		}
	}
	diag(e, "%s", s.field("status", situationLabel(s, p.Situation), w))

	if p.Situation == service.SituationReApproval {
		diag(e, "")
		diag(e, "  %s the content changed since your last approval", s.yellow("⚠"))
		for _, f := range p.Modified {
			diag(e, "      %s %s", s.yellow("~"), f)
		}
		for _, f := range p.Added {
			diag(e, "      %s %s", s.green("+"), f)
		}
		for _, f := range p.Removed {
			diag(e, "      %s %s", s.red("-"), f)
		}
	}

	diag(e, "")
	diag(e, "%s", s.grey(rule(" content ")))
	for _, line := range strings.Split(strings.TrimRight(string(p.Artifact.Document), "\n"), "\n") {
		diag(e, "%s", line)
	}
	diag(e, "%s", s.grey(rule(" end of content ")))
	diag(e, "")
}

// rule renders a labelled horizontal separator.
func rule(label string) string {
	const width = 66
	pad := width - displayWidth(label) - 4
	if pad < 0 {
		pad = 0
	}
	return "──" + label + strings.Repeat("─", pad)
}

func situationLabel(s styler, sit service.Situation) string {
	switch sit {
	case service.SituationNew:
		return s.green("new")
	case service.SituationAlreadyTrusted:
		return s.dim("already trusted, unchanged")
	case service.SituationReApproval:
		return s.yellow("already trusted, content changed")
	default:
		return "unknown"
	}
}

func confirm(e *env, question string) (bool, error) {
	fmt.Fprintf(e.stderr, "%s %s ", question, e.style.grey("[y/N]"))
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// --- get ---

func newGetCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|reference>",
		Short: "Write a trusted skill's prompt to stdout",
		Long: `Write the skill's content to stdout and store nothing.

Works without an interactive terminal: this is the command the agent runs
inside a session. No prompt is ever opened, under any circumstance.

Only hands over skills that were already trusted, and only after verifying
the content is exactly what was approved.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			res, err := service.NewSkillService(deps).Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitPayload(e, res)
		},
	}
}

// writeStdout is the ONLY place in the system that writes to the payload
// channel. Everything else goes through diag or through the logger, both on
// stderr. That is what makes the output contract verifiable by inspecting one
// single point.
func writeStdout(e *env, b []byte) error {
	_, err := e.stdout.Write(b)
	return err
}

// emitJSON writes a structure to the payload channel.
func emitJSON(e *env, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errs.Wrap(err, errs.ClassState, "report this problem", "serialising output")
	}
	return writeStdout(e, append(b, '\n'))
}

// emitPayload hands over a skill's verified content.
func emitPayload(e *env, res *service.GetResult) error {
	if err := writeStdout(e, res.Document); err != nil {
		return err
	}
	if len(res.Resources) == 0 {
		return nil // no resources, no block
	}

	var b strings.Builder
	if !strings.HasSuffix(string(res.Document), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(resourcesOpen)
	b.WriteString("\nSupporting files for this skill, available on disk:\n\n")
	for _, r := range res.Resources {
		fmt.Fprintf(&b, "- `%s` → `%s`\n", r.RelPath, r.Path)
	}
	b.WriteString(resourcesClose)
	b.WriteString("\n")

	return writeStdout(e, []byte(b.String()))
}

// --- wiring and output ---

func build(e *env) (service.Deps, error) {
	level := slog.LevelWarn
	if e.verbose {
		level = slog.LevelDebug
	}
	// The logger's destination is decided here, and it is always stderr. The
	// core receives a ready logger and never picks where it writes.
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: level}))

	paths, err := config.Resolve()
	if err != nil {
		return service.Deps{}, err
	}
	adapter, err := tool.ByName(e.tool)
	if err != nil {
		return service.Deps{}, err
	}
	store, err := trust.Open(paths)
	if err != nil {
		return service.Deps{}, err
	}

	limits := source.DefaultLimits()
	client := source.NewHTTPClient(limits.Timeout)
	gh := source.NewGitHubResolver(client, limits)
	registry := source.NewRegistryResolver(client, limits, gh)

	// Allows pointing at a self-hosted registry mirror, and is what makes the
	// search path testable without network access.
	if base := os.Getenv(EnvRegistryBase); base != "" {
		registry.APIBase = base
		gh.CodeloadBase = base
		gh.RawBase = base
	}

	disp := source.NewDispatcher()
	disp.Register(ref.KindGitHub, gh)
	disp.Register(ref.KindURL, source.NewURLResolver(client, limits))
	disp.Register(ref.KindRegistry, registry)
	disp.Register(ref.KindLocal, source.NewLocalResolver(limits))

	lastSearcher = registry

	return service.Deps{
		Paths:    paths,
		Store:    store,
		Cache:    cache.New(paths.ContentDir, paths.IndexDir),
		Resolver: disp,
		Anchor:   anchor.New(adapter),
		Adapter:  adapter,
		Log:      log,
	}, nil
}

// lastSearcher holds the registry resolver built by the most recent call to
// build, so buildWithSearcher can return it without duplicating the wiring.
var lastSearcher source.Searcher

// buildWithSearcher wires dependencies and also returns the searcher.
func buildWithSearcher(e *env) (service.Deps, source.Searcher, error) {
	deps, err := build(e)
	if err != nil {
		return service.Deps{}, nil, err
	}
	return deps, lastSearcher, nil
}

func diag(e *env, format string, args ...any) {
	fmt.Fprintf(e.stderr, format+"\n", args...)
}

func warnBudget(e *env, st anchor.Stats) {
	if !st.OverBudget {
		return
	}
	s := e.style
	diag(e, "")
	diag(e, "%s the anchor index holds %d skills and %s bytes, over the budget of %d skills and %s bytes.",
		s.yellow("⚠"), st.Entries, thousands(st.BodyBytes),
		anchor.MaxEntries, thousands(anchor.MaxBodySize))
	diag(e, "  Consider untrusting skills you no longer use, to protect the context window.")
}

// reportError turns an error class into an exit code and writes the diagnostic
// to stderr. Nothing is written to stdout here.
func reportError(e *env, err error) int {
	s := e.style

	class, ok := errs.ClassOf(err)
	if !ok {
		diag(e, "%s %v", s.wrap(ansiBold+ansiRed, "error:"), err)
		return ExitUsage
	}

	diag(e, "%s %v", s.wrap(ansiBold+ansiRed, "error ("+class.String()+"):"), err)
	if action := errs.ActionOf(err); action != "" {
		for i, line := range strings.Split(action, "\n") {
			if i == 0 {
				diag(e, "%s %s", s.grey("→"), line)
			} else {
				diag(e, "  %s", line)
			}
		}
	}

	switch class {
	case errs.ClassUsage:
		return ExitUsage
	case errs.ClassNotFound:
		return ExitNotFound
	case errs.ClassNotTrusted:
		return ExitNotTrust
	case errs.ClassIntegrityMismatch:
		return ExitIntegrity
	case errs.ClassNetwork:
		return ExitNetwork
	case errs.ClassTTYRequired:
		return ExitTTY
	case errs.ClassState:
		return ExitState
	default:
		return ExitUsage
	}
}

// isTerminal reports whether the descriptor is a real interactive terminal.
//
// The common heuristic of checking only the character-device bit is not
// enough: /dev/null has it too, and would pass as a terminal. Since this is
// the gate that guarantees trust is always a deliberate human decision, the
// check has to be the real one — a terminal ioctl.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
