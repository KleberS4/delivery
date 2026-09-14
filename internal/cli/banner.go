package cli

import (
	"fmt"
	"strings"

	"github.com/kleberS4/delivery/internal/anchor"
	"github.com/kleberS4/delivery/internal/config"
	"github.com/kleberS4/delivery/internal/trust"
)

// parcelGrid is the mark `delivery` draws when run with no arguments: a parcel
// drawn as a box in isometric projection, sealed with packing tape.
//
//	T  top face, catching the light
//	L  left face, lit
//	R  right face, in shadow
//	#  packing tape, over the top and down the front edge
//	.  nothing
//
// Depth comes from shading three faces differently — that, and nothing else, is
// what makes a flat grid of blocks read as a solid. The tape carries the idea:
// a sealed box, and nothing travels unless it was sealed.
//
// The grid is **square pixels at twice the terminal's vertical resolution**.
// Terminal cells are roughly twice as tall as they are wide, so each row here is
// half a cell, and a row is as tall as a column is wide. That makes the geometry
// honest: the box is a real isometric projection, with the top face's edges
// stepping two columns per row, and renderParcel pairs the rows back into cells.
// Drawn at whole-cell resolution instead, those diagonals step a full row at a
// time and the box reads as a staircase.
var parcelGrid = []string{
	"........##........",
	"......TT##TT......",
	"....TTTT##TTTT....",
	"..TTTTTT##TTTTTT..",
	"TTTTTTTT##TTTTTTTT",
	"LLTTTTTT##TTTTTTRR",
	"LLLLTTTT##TTTTRRRR",
	"LLLLLLTT##TTRRRRRR",
	"LLLLLLLL##RRRRRRRR",
	".LLLLLLL##RRRRRRR.",
	"...LLLLL##RRRRR...",
	".....LLL##RRR.....",
	".......L##R.......",
	"..................",
}

// The burnt-yellow palette, in 256-colour indices, ordered by brightness so
// that each face steps evenly away from the one beside it.
//
//	223  #ffd7af  pale cream, the tape
//	180  #d7af87  warm tan, the top face
//	172  #d78700  burnt yellow, the lit left face
//	130  #af5f00  deep burnt, the shadowed right face
const (
	colTape  = 223
	colTop   = 180
	colLeft  = 172
	colRight = 130
)

// Glyphs for terminals that cannot paint the palette.
//
// The tones have to stay apart without colour or the box flattens into a blob,
// so each one also carries a density. The ramp follows the same brightness
// order as the palette: solid for the tape, sparsest for the face in shadow.
const (
	glyphTape  = '█'
	glyphTop   = '▓'
	glyphLeft  = '▒'
	glyphRight = '░'
)

// tagline states the product's thesis in the subject's own terms.
const tagline = "Skills arrive when the task needs them, not before."

// toneOf maps a grid cell to the pixel it paints.
func toneOf(cell byte) pixel {
	switch cell {
	case 'T':
		return pixel{colour: colTop, glyph: glyphTop, filled: true}
	case 'L':
		return pixel{colour: colLeft, glyph: glyphLeft, filled: true}
	case 'R':
		return pixel{colour: colRight, glyph: glyphRight, filled: true}
	case '#':
		return pixel{colour: colTape, glyph: glyphTape, filled: true}
	default:
		return pixel{}
	}
}

// renderParcel turns the grid into terminal lines, two grid rows per line.
//
// Consecutive cells carrying the same pair of pixels are emitted as one styled
// run rather than one per cell. An eighteen-cell row would otherwise carry
// eighteen escape sequences, which bloats the output and makes it unreadable
// the moment anyone inspects it raw.
func renderParcel(s styler) []string {
	out := make([]string, 0, len(parcelGrid)/2)

	for r := 0; r+1 < len(parcelGrid); r += 2 {
		upper, lower := parcelGrid[r], parcelGrid[r+1]

		var b strings.Builder
		for i := 0; i < len(upper); {
			j := i
			for j < len(upper) && upper[j] == upper[i] && lower[j] == lower[i] {
				j++
			}
			b.WriteString(s.blockRun(toneOf(upper[i]), toneOf(lower[i]), j-i))
			i = j
		}
		out = append(out, b.String())
	}
	return out
}

// showBanner renders the no-argument screen.
//
// It writes to stderr like every other diagnostic: stdout stays reserved for
// payload even here, where nothing is being delivered.
//
// It never fails. A bare invocation is where someone goes to get their
// bearings, so unreadable state degrades into the getting-started screen rather
// than into an error.
func showBanner(e *env) {
	s := e.style

	count, bodyBytes, ok := indexSummary()
	hasIndex := ok && count > 0

	// The text sits beside the mark, and is centred against it rather than
	// aligned to the top: a three-line block hanging off the lid reads as
	// unfinished, while a centred one reads as placed.
	beside := []string{
		s.bold("delivery") + "  " + s.grey(Version),
		s.grey(tagline),
	}
	if hasIndex {
		beside = append(beside, fmt.Sprintf("%s in the index, using %s of the anchor's %s budget",
			s.bold(pluralSkills(count)), s.bold(kilobytes(bodyBytes)),
			kilobytes(anchor.MaxBodySize)))
	} else {
		beside = append(beside, s.grey("nothing in the index yet"))
	}

	art := renderParcel(s)
	offset := (len(art) - len(beside)) / 2

	diag(e, "")
	for i, line := range art {
		j := i - offset
		if j >= 0 && j < len(beside) {
			// The mark's own padding holds the text column in place, so the
			// trailing spaces stay on the rows that carry text.
			diag(e, "  %s   %s", line, beside[j])
			continue
		}
		diag(e, "  %s", strings.TrimRight(line, " "))
	}
	diag(e, "")

	if hasIndex {
		renderCommands(e, [][2]string{
			{"delivery list", "see what you have"},
			{"delivery search <term>", "find more"},
			{"delivery update", "check what changed upstream"},
		})
	} else {
		renderCommands(e, [][2]string{
			{"delivery init", "set up state and install the anchor skill"},
			{"delivery search <term>", "find a skill"},
			{"delivery trust <reference>", "approve it, here in your terminal"},
		})
	}

	diag(e, "")
	diag(e, "  %s lists every command.", s.cmd("delivery --help"))
	diag(e, "")
}

// renderCommands prints a command column with the descriptions aligned.
//
// Widths come from the data, and padding is applied to the unstyled text:
// escape sequences have zero display width, so padding styled text would skew
// every row.
func renderCommands(e *env, rows [][2]string) {
	width := 0
	for _, r := range rows {
		if w := displayWidth(r[0]); w > width {
			width = w
		}
	}
	for _, r := range rows {
		pad := strings.Repeat(" ", width-displayWidth(r[0]))
		diag(e, "    %s%s   %s", e.style.cmd(r[0]), pad, e.style.grey(r[1]))
	}
}

// indexSummary reads the trusted set without writing anything.
//
// A bare invocation must not create or modify state — someone typing the
// command alone is looking, not acting. The anchor is rendered in memory only,
// to measure it.
func indexSummary() (count, bodyBytes int, ok bool) {
	paths, err := config.Resolve()
	if err != nil {
		return 0, 0, false
	}
	store, err := trust.Open(paths)
	if err != nil {
		return 0, 0, false
	}
	records := store.List()
	_, stats := anchor.Render(records)
	return len(records), stats.BodyBytes, true
}

func pluralSkills(n int) string {
	if n == 1 {
		return "1 skill"
	}
	return fmt.Sprintf("%d skills", n)
}

// kilobytes formats a byte count for a reader who cares about the order of
// magnitude, not the exact number. A round figure loses its decimal, so the
// budget reads as "16 KB" rather than "16.0 KB".
func kilobytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	kb := float64(n) / 1024
	if kb == float64(int(kb)) {
		return fmt.Sprintf("%d KB", int(kb))
	}
	return fmt.Sprintf("%.1f KB", kb)
}
