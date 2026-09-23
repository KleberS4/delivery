package cli

import (
	"strings"
	"testing"
)

// The monochrome fallback is the path most likely to rot, because it only
// shows up on terminals the author is not using. These tests pin it down.

func TestBlockRunWithoutColourKeepsDensityGlyphs(t *testing.T) {
	s := styler{}

	got := s.blockRun(toneOf('T'), toneOf('T'), 3)

	if got != "▓▓▓" {
		t.Fatalf("blockRun = %q, want the raw glyphs", got)
	}
	if strings.Contains(got, "\033") {
		t.Fatal("escape sequence emitted with colour disabled")
	}
}

// Without 256 colours the faces cannot be painted, so they have to stay apart
// by glyph. Collapsing them to one fill here would flatten the box into a blob
// and take the depth with it.
func TestBlockRunWith16ColoursKeepsTheFacesApart(t *testing.T) {
	s := styler{enabled: true, extended: false}

	seen := map[string]string{}
	for _, cell := range []byte{'T', 'L', 'R', '#'} {
		p := toneOf(cell)
		got := s.blockRun(p, p, 2)

		if !strings.Contains(got, strings.Repeat(string(p.glyph), 2)) {
			t.Fatalf("cell %q: glyph was replaced: %q", cell, got)
		}
		if prev, ok := seen[got]; ok {
			t.Fatalf("cells %q and %q are indistinguishable without 256 colours", prev, string(cell))
		}
		seen[got] = string(cell)
	}
}

// A cell whose two stacked pixels differ becomes an upper half block with the
// lower pixel painted behind it. That half-row of resolution is what lets the
// isometric edges slope; without it the box staircases.
func TestBlockRunPaintsSplitCellsAsHalfBlocks(t *testing.T) {
	s := styler{enabled: true, extended: true}

	got := s.blockRun(toneOf('T'), toneOf('L'), 2)

	if !strings.Contains(got, "\033[38;5;180m") {
		t.Errorf("top face not in the foreground: %q", got)
	}
	if !strings.Contains(got, "\033[48;5;172m") {
		t.Errorf("left face not painted as the background: %q", got)
	}
	if !strings.Contains(got, "▀▀") {
		t.Errorf("half blocks missing: %q", got)
	}
}

// Both pixels the same tone needs no background, and a pixel with nothing under
// it must not paint one — a background there would square off the silhouette
// against the terminal.
func TestBlockRunOmitsBackgroundWhereThereIsNothingBehind(t *testing.T) {
	s := styler{enabled: true, extended: true}

	solid := s.blockRun(toneOf('R'), toneOf('R'), 4)
	if strings.Contains(solid, "\033[48;5;") {
		t.Errorf("a uniform cell painted a background: %q", solid)
	}
	if !strings.Contains(solid, "████") {
		t.Errorf("uniform cell not painted solid: %q", solid)
	}

	edge := s.blockRun(toneOf('L'), toneOf('.'), 2)
	if strings.Contains(edge, "\033[48;5;") {
		t.Errorf("the silhouette edge painted a background: %q", edge)
	}
	if !strings.Contains(edge, "▀▀") {
		t.Errorf("edge not drawn as an upper half block: %q", edge)
	}
}

// One escape per run, not one per cell.
func TestParcelCoalescesColourRuns(t *testing.T) {
	lines := renderParcel(styler{enabled: true, extended: true})

	cells := len(parcelGrid[0])
	for i, row := range lines {
		if n := strings.Count(row, "\033[38;5;"); n > cells/2 {
			t.Errorf("row %d carries %d colour openings for %d cells — runs are not coalescing:\n%q",
				i, n, cells, row)
		}
	}
}

// Two grid rows per terminal row, and every row the same width.
func TestParcelPairsGridRowsIntoCells(t *testing.T) {
	if len(parcelGrid)%2 != 0 {
		t.Fatalf("the grid has %d rows; it is paired into cells and must be even", len(parcelGrid))
	}

	width := len(parcelGrid[0])
	for i, row := range parcelGrid {
		if len(row) != width {
			t.Errorf("grid row %d is %d cells wide, want %d", i, len(row), width)
		}
	}

	if got, want := len(renderParcel(styler{})), len(parcelGrid)/2; got != want {
		t.Errorf("rendered %d terminal rows, want %d", got, want)
	}
}

func TestSupports256Detection(t *testing.T) {
	cases := []struct {
		term      string
		colorterm string
		want      bool
	}{
		{"xterm-256color", "", true},
		{"screen-256color", "", true},
		{"xterm", "truecolor", true},
		{"xterm", "24bit", true},
		{"xterm", "", false},
		{"vt100", "", false},
	}

	for _, c := range cases {
		t.Setenv("COLORTERM", c.colorterm)
		if got := supports256(c.term); got != c.want {
			t.Errorf("supports256(%q) with COLORTERM=%q = %v, want %v",
				c.term, c.colorterm, got, c.want)
		}
	}
}

// The tones are chosen as RGB; the palette entries only approximate them. A
// terminal that can paint 24-bit gets them exactly, and never a palette index.
func TestTruecolourPaintsExactTones(t *testing.T) {
	s := styler{enabled: true, extended: true, truecolor: true}

	got := s.blockRun(toneOf('T'), toneOf('L'), 2)

	if !strings.Contains(got, "\033[38;2;240;192;138m") {
		t.Errorf("top face not painted as 24-bit RGB: %q", got)
	}
	if !strings.Contains(got, "\033[48;2;224;154;82m") {
		t.Errorf("left face not painted as a 24-bit background: %q", got)
	}
	if strings.Contains(got, "38;5;") || strings.Contains(got, "48;5;") {
		t.Errorf("a palette index survived into the 24-bit run: %q", got)
	}
}

// Three degradations, each one step coarser, and every one has to stay correct
// because a terminal only ever exercises the tier it supports.
func TestEachColourTierDegradesCorrectly(t *testing.T) {
	cases := []struct {
		name   string
		s      styler
		want   string
		reject string
	}{
		{"truecolor", styler{enabled: true, extended: true, truecolor: true}, "38;2;", "38;5;"},
		{"256 colours", styler{enabled: true, extended: true}, "38;5;", "38;2;"},
		{"16 colours", styler{enabled: true}, ansiYellow, "38;"},
		{"no colour", styler{}, "▓", "\033"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.s.blockRun(toneOf('T'), toneOf('T'), 2)
			if !strings.Contains(got, c.want) {
				t.Errorf("missing %q: %q", c.want, got)
			}
			if strings.Contains(got, c.reject) {
				t.Errorf("a higher tier leaked in (%q): %q", c.reject, got)
			}
		})
	}
}

// The four tones must stay ordered by relative luminance. That ordering is
// what makes the faces read apart rather than merge, and it is the same order
// the density glyphs follow, so colour and monochrome describe one solid.
func TestTonesAreOrderedByLuminance(t *testing.T) {
	lum := func(p pixel) float64 {
		return (0.2126*float64(p.rgb[0]) + 0.7152*float64(p.rgb[1]) + 0.0722*float64(p.rgb[2])) / 255
	}
	// Darkest to lightest: shadowed face, lit face, top, tape.
	order := []byte{'R', 'L', 'T', '#'}
	glyphs := []rune{glyphRight, glyphLeft, glyphTop, glyphTape}

	for i := 1; i < len(order); i++ {
		prev, cur := toneOf(order[i-1]), toneOf(order[i])
		if lum(cur) <= lum(prev) {
			t.Errorf("%q (%.3f) is not lighter than %q (%.3f)",
				order[i], lum(cur), order[i-1], lum(prev))
		}
		if d := lum(cur) - lum(prev); d < 0.08 {
			t.Errorf("%q and %q are only %.3f apart; the faces will merge",
				order[i-1], order[i], d)
		}
	}
	for i, cell := range order {
		if toneOf(cell).glyph != glyphs[i] {
			t.Errorf("tone %q carries glyph %q; the density ramp must follow the same order as the luminance ramp",
				cell, toneOf(cell).glyph)
		}
	}
}
