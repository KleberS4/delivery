package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/kleberS4/delivery/internal/errs"
	"github.com/kleberS4/delivery/internal/service"
	"github.com/kleberS4/delivery/internal/source"

	"github.com/spf13/cobra"
)

// --- search ---

func newSearchCmd(e *env) *cobra.Command {
	var limit int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "search <term>",
		Short: "Find skills in the registry",
		Long: `Query the registry and show what exists.

Every result is marked with its state: trusted, covered by a trusted
source, or not trusted. Only trusted skills can be loaded by the agent.

The registry does not provide descriptions — those appear at approval
time, once the content is actually downloaded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, searcher, err := buildWithSearcher(e)
			if err != nil {
				return err
			}
			res, err := service.NewDiscoveryService(deps, searcher).
				Search(cmd.Context(), source.Query{Term: args[0], Limit: limit})
			if err != nil {
				return err
			}

			if jsonOut {
				return emitJSON(e, toSearchJSON(res))
			}
			renderSearch(e, args[0], res)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of results")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "structured output on stdout")
	return cmd
}

// renderSearch prints the result table.
//
// Column widths are computed from the actual data rather than hard-coded.
// A fixed %-40s breaks the moment an identifier is longer than the pad, which
// is common: registry ids routinely run past fifty characters.
func renderSearch(e *env, term string, res *service.SearchResult) {
	s := e.style

	if res.Stale {
		diag(e, "")
		diag(e, "%s registry unreachable — showing a cached index from %s ago.",
			s.yellow("⚠"), res.Age.Truncate(60_000_000_000).String())
	}
	if len(res.Hits) == 0 {
		diag(e, "")
		diag(e, "%s no skills found for %s.", s.grey("·"), s.bold(term))
		diag(e, "")
		return
	}

	refWidth, installsWidth := 0, 0
	for _, h := range res.Hits {
		if w := displayWidth(h.Entry.ID); w > refWidth {
			refWidth = w
		}
		if w := displayWidth(thousands(h.Entry.Installs)); w > installsWidth {
			installsWidth = w
		}
	}

	diag(e, "")
	diag(e, "%s", s.heading(fmt.Sprintf("%d result(s) for %q", len(res.Hits), term)))
	diag(e, "")

	for _, h := range res.Hits {
		mark, id := " ", h.Entry.ID
		switch h.State {
		case service.TrustStateIndividual:
			mark, id = s.green("✓"), s.green(id)
		case service.TrustStateBySource:
			mark, id = s.yellow("~"), s.yellow(id)
		default:
			id = s.ref(id)
		}
		// Padding is applied to the unstyled text, since escape sequences have
		// no display width and would otherwise skew every column.
		pad := strings.Repeat(" ", refWidth-displayWidth(h.Entry.ID))
		diag(e, "  %s %s%s  %s %s",
			mark, id, pad,
			s.dim(padLeft(thousands(h.Entry.Installs), installsWidth)),
			s.grey("installs"))
	}

	diag(e, "")
	diag(e, "  %s trusted   %s covered by source   %s needs approval",
		s.green("✓"), s.yellow("~"), s.grey("·"))
	diag(e, "")
	diag(e, "  Approve one with %s", s.cmd("delivery trust <identifier>"))
	diag(e, "")
}

type searchJSON struct {
	Stale bool            `json:"stale"`
	Hits  []searchHitJSON `json:"hits"`
}

type searchHitJSON struct {
	ID       string `json:"id"`
	SkillID  string `json:"skillId"`
	Source   string `json:"source"`
	Installs int    `json:"installs"`
	State    string `json:"state"`
	Trusted  bool   `json:"trusted"`
}

func toSearchJSON(res *service.SearchResult) searchJSON {
	out := searchJSON{Stale: res.Stale, Hits: make([]searchHitJSON, 0, len(res.Hits))}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, searchHitJSON{
			ID: h.Entry.ID, SkillID: h.Entry.SkillID, Source: h.Entry.Source,
			Installs: h.Entry.Installs, State: h.State.String(),
			Trusted: h.State != service.TrustStateNone,
		})
	}
	return out
}

// --- untrust ---

func newUntrustCmd(e *env) *cobra.Command {
	var asSource bool

	cmd := &cobra.Command{
		Use:   "untrust <name|reference|owner/repo>",
		Short: "Revoke trust in a skill or in a source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			svc := service.NewTrustService(deps)
			s := e.style

			if asSource {
				stats, err := svc.UntrustSource(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				diag(e, "")
				diag(e, "%s source coverage removed: %s", s.green("✓"), s.ref(args[0]))
				diag(e, "  %s", s.grey("Individually granted trust is preserved."))
				diag(e, "")
				warnBudget(e, stats)
				return nil
			}

			res, err := svc.Untrust(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			diag(e, "")
			diag(e, "%s trust revoked: %s", s.green("✓"), s.ref(res.Ref))
			if res.StillInstalled {
				diag(e, "  %s", s.grey("The file is still on disk. To remove it:"))
				diag(e, "    %s", s.cmd("delivery uninstall "+res.Ref))
			}
			diag(e, "")
			warnBudget(e, res.Stats)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asSource, "source", false, "remove coverage of a whole repository")
	return cmd
}

// --- trust --source ---

func runTrustSource(e *env, cmd *cobra.Command, sourceKey string, yes bool) error {
	if !isTerminal(os.Stdin) {
		return errs.TTYRequired(
			"run this in an interactive terminal: delivery trust --source "+sourceKey,
			"trust requires an interactive terminal")
	}

	deps, err := build(e)
	if err != nil {
		return err
	}
	s := e.style

	// The implication is presented BEFORE the confirmation. Presenting it
	// afterwards would be informing once the decision had already been made.
	diag(e, "")
	diag(e, "%s %s", s.yellow("⚠"), s.bold("Trusting an entire repository: "+sourceKey))
	diag(e, "")
	diag(e, "  This reaches skills that have %s been published in that", s.bold("not yet"))
	diag(e, "  repository, and which you will therefore not have reviewed.")
	diag(e, "  They are handed to the agent %s.", s.bold("without a pre-approved hash"))
	diag(e, "")
	diag(e, "  %s", s.grey("Trusting one skill at a time is safer, and is the normal path."))
	diag(e, "")

	if !yes {
		ok, err := confirm(e, "Trust the whole repository anyway?")
		if err != nil {
			return err
		}
		if !ok {
			diag(e, "%s nothing changed.", s.grey("·"))
			return nil
		}
	}

	stats, err := service.NewTrustService(deps).TrustSource(cmd.Context(), sourceKey)
	if err != nil {
		return err
	}
	diag(e, "")
	diag(e, "%s source trusted: %s", s.green("✓"), s.ref(sourceKey))
	diag(e, "")
	warnBudget(e, stats)
	return nil
}

// --- install / uninstall ---

func newInstallCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "install <name|reference>",
		Short: "Write a trusted skill into the tool's skills directory",
		Long: `Write the skill to disk, for recurring use.

The markdown is written exactly as it arrived, with a provenance block
appended at the end — never at the start, so the frontmatter is untouched.

The recorded hash remains that of the original content, without the block.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			res, err := service.NewSkillService(deps).Install(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			s := e.style
			diag(e, "")
			diag(e, "%s installed %s", s.green("✓"), s.bold(res.ShortName))
			diag(e, "  %s", s.dim(res.Path))
			if res.Resources > 0 {
				diag(e, "  %s", s.grey(fmt.Sprintf("%d supporting file(s) in the cache", res.Resources)))
			}
			diag(e, "")
			warnBudget(e, res.Stats)
			return nil
		},
	}
}

func newUninstallCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <name|reference>",
		Short: "Remove a skill that delivery installed",
		Long: `Only removes files that delivery manages.

A skill you wrote by hand, in a directory whose name happens to match, is
never removed: the absence of the provenance block is proof that the file
is not ours.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			stats, err := service.NewSkillService(deps).Uninstall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			s := e.style
			diag(e, "")
			diag(e, "%s removed from disk. trust is still recorded.", s.green("✓"))
			diag(e, "")
			warnBudget(e, stats)
			return nil
		},
	}
}

// --- update ---

func newUpdateCmd(e *env) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "update [name|reference]",
		Short: "Show what changed upstream and apply what you accept",
		Long: `Compare every trusted skill against the current content at its origin.

The check changes no state at all. Nothing is accepted without your decision.

Whatever you do not accept stays on the approved content, indefinitely:
declining is not postponing, it is deciding to stay where you are.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			only := ""
			if len(args) == 1 {
				only = args[0]
			}

			deps, err := build(e)
			if err != nil {
				return err
			}
			svc := service.NewMaintenanceService(deps)
			s := e.style

			cs, err := svc.CheckUpdates(cmd.Context(), only)
			if err != nil {
				return err
			}

			diag(e, "")
			for _, u := range cs.Unreachable {
				diag(e, "  %s could not check (network): %s", s.yellow("⚠"), s.ref(u))
			}
			if len(cs.Items) == 0 {
				diag(e, "%s no updates. %d skill(s) unchanged.",
					s.green("✓"), cs.Unchanged)
				diag(e, "")
				return nil
			}

			diag(e, "%s", s.heading(fmt.Sprintf("%d skill(s) changed upstream", len(cs.Items))))
			for _, it := range cs.Items {
				diag(e, "")
				diag(e, "  %s  %s", s.bold(it.ShortName), s.dim(it.Ref))
				for _, f := range it.Modified {
					diag(e, "      %s %s", s.yellow("~"), f)
				}
				for _, f := range it.Added {
					diag(e, "      %s %s", s.green("+"), f)
				}
				for _, f := range it.Removed {
					diag(e, "      %s %s", s.red("-"), f)
				}
			}
			diag(e, "")

			if !yes {
				if !isTerminal(os.Stdin) {
					return errs.TTYRequired(
						"run this in a terminal, or use: delivery update -y",
						"accepting updates requires an interactive terminal")
				}
				ok, err := confirm(e, fmt.Sprintf("Accept all %d update(s)?", len(cs.Items)))
				if err != nil {
					return err
				}
				if !ok {
					diag(e, "%s nothing changed. the agent keeps receiving the approved versions.",
						s.grey("·"))
					return nil
				}
			}

			applied, stats, err := svc.ApplyUpdates(cmd.Context(), cs, nil)
			if err != nil {
				return err
			}
			diag(e, "")
			diag(e, "%s %d update(s) applied.", s.green("✓"), applied)
			diag(e, "")
			warnBudget(e, stats)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept every update without asking")
	return cmd
}

// --- list ---

func newListCmd(e *env) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show what is trusted and installed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := build(e)
			if err != nil {
				return err
			}
			inv := service.NewMaintenanceService(deps).List(cmd.Context())

			if jsonOut {
				return emitJSON(e, toInventoryJSON(inv))
			}
			renderInventory(e, inv)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "structured output on stdout")
	return cmd
}

func renderInventory(e *env, inv *service.Inventory) {
	s := e.style

	if len(inv.Items) == 0 && len(inv.Sources) == 0 {
		diag(e, "")
		diag(e, "%s nothing trusted yet.", s.grey("·"))
		diag(e, "")
		diag(e, "  Find one with  %s", s.cmd("delivery search <term>"))
		diag(e, "  Then approve   %s", s.cmd("delivery trust <identifier>"))
		diag(e, "")
		return
	}

	if len(inv.Items) > 0 {
		nameWidth, refWidth := 0, 0
		for _, it := range inv.Items {
			if w := displayWidth(it.ShortName); w > nameWidth {
				nameWidth = w
			}
			if w := displayWidth(it.Ref); w > refWidth {
				refWidth = w
			}
		}

		diag(e, "")
		diag(e, "%s", s.heading(fmt.Sprintf("Trusted skills (%d)", len(inv.Items))))
		diag(e, "")
		for _, it := range inv.Items {
			var tags []string
			if it.Installed {
				tags = append(tags, s.green("installed"))
			}
			if it.MetaOrigin == "derived" {
				tags = append(tags, s.grey("derived metadata"))
			}
			suffix := ""
			if len(tags) > 0 {
				suffix = "  " + strings.Join(tags, s.grey(", "))
			}
			pad := strings.Repeat(" ", refWidth-displayWidth(it.Ref))
			diag(e, "  %s  %s%s%s",
				s.bold(padRight(it.ShortName, nameWidth)), s.ref(it.Ref), pad, suffix)
		}
	}

	if len(inv.Sources) > 0 {
		diag(e, "")
		diag(e, "%s", s.heading(fmt.Sprintf("Trusted sources (%d)", len(inv.Sources))))
		diag(e, "")
		for _, src := range inv.Sources {
			diag(e, "  %s %s  %s", s.yellow("~"), s.ref(src.Source),
				s.grey("reaches this repository's future skills"))
		}
	}
	diag(e, "")
}

type inventoryJSON struct {
	Skills  []inventoryItemJSON `json:"skills"`
	Sources []string            `json:"trustedSources"`
}

type inventoryItemJSON struct {
	Ref         string `json:"ref"`
	ShortName   string `json:"shortName"`
	Description string `json:"description"`
	MetaOrigin  string `json:"metaOrigin"`
	Installed   bool   `json:"installed"`
	SetHash     string `json:"setHash"`
	TrustedAt   string `json:"trustedAt"`
}

func toInventoryJSON(inv *service.Inventory) inventoryJSON {
	out := inventoryJSON{Skills: make([]inventoryItemJSON, 0, len(inv.Items))}
	for _, it := range inv.Items {
		out.Skills = append(out.Skills, inventoryItemJSON{
			Ref: it.Ref, ShortName: it.ShortName, Description: it.Description,
			MetaOrigin: it.MetaOrigin, Installed: it.Installed,
			SetHash: it.SetHash, TrustedAt: it.TrustedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	for _, src := range inv.Sources {
		out.Sources = append(out.Sources, src.Source)
	}
	return out
}
