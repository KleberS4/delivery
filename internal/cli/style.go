package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ANSI escape sequences. Kept as raw constants so the styling layer has no
// dependencies: colour is a presentation detail, not a reason to pull in code.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGrey   = "\033[90m"
)

// styler decides whether to emit colour, and applies it.
//
// Colour is disabled when the stream is not a terminal, when NO_COLOR is set
// (see no-color.org), when TERM is "dumb", or when --no-color is passed. A
// piped or redirected stream therefore always gets plain text, which keeps the
// output greppable and keeps escape sequences out of log files.
//
// extended tracks 256-colour support separately, because the burnt-yellow
// palette of the parcel mark has no equivalent in the 16-colour set.
type styler struct {
	enabled  bool
	extended bool
	// truecolor tracks 24-bit support separately from the 256-colour palette.
	// The mark's tones are chosen as RGB and only approximated by palette
	// entries, so where 24-bit is available the approximation is skipped.
	truecolor bool
}

func newStyler(w io.Writer, forceOff bool) styler {
	if forceOff {
		return styler{}
	}
	if os.Getenv("NO_COLOR") != "" {
		return styler{}
	}
	termVar := os.Getenv("TERM")
	if termVar == "dumb" || termVar == "" {
		return styler{}
	}
	f, ok := w.(*os.File)
	if !ok || !isTerminal(f) {
		return styler{}
	}
	return styler{enabled: true, extended: supports256(termVar), truecolor: supportsTruecolor()}
}

// supports256 reports whether the terminal can paint the 256-colour palette.
//
// COLORTERM is the reliable signal on modern emulators; TERM carrying
// "256color" covers the rest. Anything else falls back to shading by glyph
// density, which is why the fallback has to exist rather than being an
// afterthought.
func supports256(termVar string) bool {
	if supportsTruecolor() {
		return true
	}
	return strings.Contains(termVar, "256color")
}

// supportsTruecolor reports 24-bit support. COLORTERM is the only signal worth
// trusting here: TERM says nothing about it, and probing the terminal would
// mean writing to a stream reserved for diagnostics.
func supportsTruecolor() bool {
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return true
	}
	return false
}

func (s styler) wrap(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func (s styler) bold(t string) string   { return s.wrap(ansiBold, t) }
func (s styler) dim(t string) string    { return s.wrap(ansiDim, t) }
func (s styler) red(t string) string    { return s.wrap(ansiRed, t) }
func (s styler) green(t string) string  { return s.wrap(ansiGreen, t) }
func (s styler) yellow(t string) string { return s.wrap(ansiYellow, t) }
func (s styler) cyan(t string) string   { return s.wrap(ansiCyan, t) }
func (s styler) grey(t string) string   { return s.wrap(ansiGrey, t) }

// pixel is one square cell of the parcel mark: a palette entry for terminals
// that can paint it, and a density glyph for those that cannot. The zero value
// is transparent.
type pixel struct {
	colour int    // 256-colour palette entry, an approximation of rgb
	rgb    [3]int // the tone as chosen, used where 24-bit is available
	glyph  rune   // density, for terminals with no colour at all
	filled bool
}

// blockRun paints a horizontal run of the parcel mark, n cells wide.
//
// The mark is drawn at twice the terminal's vertical resolution, so every cell
// carries two stacked pixels. With 256 colours they are painted as an upper
// half block over a background: the foreground is the top pixel, the background
// the bottom one. That half-row of extra resolution is what lets the box's
// isometric edges slope instead of staircase.
//
// Without 256 colours the pair collapses into a single cell shaded by glyph
// density. The shape coarsens, but the three faces stay apart — which is the
// part that carries the depth, and the part a solid fill would destroy.
func (s styler) blockRun(top, bottom pixel, n int) string {
	if !top.filled && !bottom.filled {
		return strings.Repeat(" ", n)
	}
	if !s.extended {
		return s.flatRun(top, bottom, n)
	}

	switch {
	case !bottom.filled:
		return s.paint(top, pixel{}, "▀", n)
	case !top.filled:
		return s.paint(bottom, pixel{}, "▄", n)
	case top.colour == bottom.colour:
		return s.paint(top, pixel{}, "█", n)
	default:
		return s.paint(top, bottom, "▀", n)
	}
}

// flatRun is the fallback for terminals without a 256-colour palette: the two
// stacked pixels become one cell, shaded by density. The upper pixel wins a
// tie, because the silhouette's upper edge is the one the eye follows.
func (s styler) flatRun(top, bottom pixel, n int) string {
	p := top
	if !p.filled {
		p = bottom
	}
	run := strings.Repeat(string(p.glyph), n)
	if !s.enabled {
		return run
	}
	return s.wrap(ansiYellow, run)
}

// paint repeats a glyph under one foreground and, when bg is not negative, one
// background colour — a single escape for the whole run rather than one per
// cell.
func (s styler) paint(fg, bg pixel, glyph string, n int) string {
	var b strings.Builder
	if s.truecolor {
		fmt.Fprintf(&b, "\033[38;2;%d;%d;%dm", fg.rgb[0], fg.rgb[1], fg.rgb[2])
		if bg.filled {
			fmt.Fprintf(&b, "\033[48;2;%d;%d;%dm", bg.rgb[0], bg.rgb[1], bg.rgb[2])
		}
	} else {
		fmt.Fprintf(&b, "\033[38;5;%dm", fg.colour)
		if bg.filled {
			fmt.Fprintf(&b, "\033[48;5;%dm", bg.colour)
		}
	}
	b.WriteString(strings.Repeat(glyph, n))
	b.WriteString(ansiReset)
	return b.String()
}

// heading renders a section title.
func (s styler) heading(t string) string { return s.bold(t) }

// ref renders a skill reference, which is the thing users copy and paste most.
func (s styler) ref(t string) string { return s.cyan(t) }

// cmd renders a command the user is meant to run.
//
// It emits one combined sequence rather than nesting two, because nesting
// leaves a stray reset in the middle of the styled run and shows up as noise
// whenever the output is inspected raw.
func (s styler) cmd(t string) string { return s.wrap(ansiBold+ansiCyan, t) }

// --- layout helpers ---

// displayWidth counts runes rather than bytes.
//
// Padding with %-Ns counts bytes, so any non-ASCII character silently shifts a
// column. That is exactly what broke the search table, where accented words and
// long identifiers made every row land in a different place.
func displayWidth(s string) int { return utf8.RuneCountInString(s) }

// padRight pads to a rune width, and never truncates: a clipped identifier is
// worse than a ragged column, because the user cannot copy it.
func padRight(s string, width int) string {
	if n := displayWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// padLeft right-aligns to a rune width.
func padLeft(s string, width int) string {
	if n := displayWidth(s); n < width {
		return strings.Repeat(" ", width-n) + s
	}
	return s
}

// thousands formats an integer with thin separators, so that 877366 reads as
// 877,366 at a glance instead of being counted digit by digit.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// truncateMiddle shortens a long string while keeping both ends readable.
// Used for hashes, never for references.
func truncateMiddle(s string, max int) string {
	if displayWidth(s) <= max || max < 9 {
		return s
	}
	r := []rune(s)
	keep := (max - 1) / 2
	return string(r[:keep]) + "…" + string(r[len(r)-keep:])
}

// --- message helpers ---

// field renders a "label  value" line with aligned labels.
func (s styler) field(label, value string, width int) string {
	return fmt.Sprintf("  %s  %s", s.grey(padRight(label, width)), value)
}
