package plot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ramp is a chart of a reading rising from 0 to 100 over 100 s, a reading a
// second, without readings from gapFrom to gapTo seconds.
func ramp(gapFrom, gapTo float64) Chart {
	var ps []Point
	for i := range 101 {
		t := float64(i)
		ps = append(ps, Point{X: t, T: t, Y: t, Ok: t < gapFrom || t >= gapTo})
	}
	return Chart{
		Title:  "ramp",
		X:      ExactScale(0, 100, 5, Clock),
		Strips: []Strip{{Title: "ramp", Y: NewScale(0, 100, 4, Number), Segments: Segments(ps, time.Second)}},
	}
}

// dotsIn is how many braille dots the plot's rows set in the character
// columns from c0 up to c1, past a gutter gutter characters wide and its
// axis.
func dotsIn(lines []string, gutter, c0, c1 int) int {
	n := 0
	for _, l := range lines[1 : len(lines)-2] {
		r := []rune(l)[gutter+1:]
		for _, ch := range r[c0:c1] {
			for b := ch - brailleBlank; b > 0; b >>= 1 {
				n += int(b & 1)
			}
		}
	}
	return n
}

// The drawing is the size asked for, title on top and axis beneath, and a
// rising reading is drawn rising: its first dots low on the left, its last
// high on the right.
func TestBrailleDrawsTheLine(t *testing.T) {
	lines := ramp(1000, 1000).Braille(40, 12)
	if len(lines) != 12 {
		t.Fatalf("%d lines, want 12", len(lines))
	}
	if lines[0] != "ramp" {
		t.Errorf("title %q", lines[0])
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > 40 {
			t.Errorf("line %d is %d characters, over the 40 asked for: %q", i, n, l)
		}
	}
	// The row of the first character with a dot in column c of the plot.
	rowAt := func(c int) int {
		for r, l := range lines[1 : len(lines)-2] {
			if []rune(l)[4+c] != brailleBlank {
				return r
			}
		}
		return -1
	}
	cols := 40 - 4
	start, end := rowAt(0), rowAt(cols-1)
	if start < 0 || end < 0 || end >= start {
		t.Errorf("the ramp starts on row %d and ends on row %d; it should end higher:\n%s", start, end, strings.Join(lines, "\n"))
	}
}

// A stretch without readings is blank -- not a line across it, and not
// dots at the bottom where a zero would be.
func TestBrailleLeavesAGapBlank(t *testing.T) {
	gutter := 3 // "100"
	whole := ramp(1000, 1000).Braille(44, 12)
	gapped := ramp(40, 60).Braille(44, 12)
	cols := 44 - gutter - 1
	// The middle of the gap: 40 to 60 s of 100, a margin inside each end.
	c0, c1 := cols*43/100, cols*57/100
	if dotsIn(whole, gutter, c0, c1) == 0 {
		t.Fatal("the unbroken ramp draws nothing where the gap would be; the test is measuring the wrong place")
	}
	if n := dotsIn(gapped, gutter, c0, c1); n != 0 {
		t.Errorf("%d dots drawn where there were no readings:\n%s", n, strings.Join(gapped, "\n"))
	}
}

// The axes are labelled: the y ticks in the gutter, the x ticks beneath,
// the first at its tick, none touching another.
func TestBrailleLabelsItsAxes(t *testing.T) {
	lines := ramp(1000, 1000).Braille(60, 14)
	body := strings.Join(lines, "\n")
	for _, want := range []string{"100┤", "  0┤"} {
		if !strings.Contains(body, want) {
			t.Errorf("no y label %q:\n%s", want, body)
		}
	}
	labels := lines[len(lines)-1]
	if !strings.HasPrefix(strings.TrimLeft(labels, " "), "0:00") {
		t.Errorf("the x labels %q do not start with 0:00", labels)
	}
	if strings.Contains(labels, "0:000:") || !strings.Contains(labels, "1:00") {
		t.Errorf("the x labels %q run together or leave out 1:00", labels)
	}
	if !strings.ContainsRune(lines[len(lines)-2], '┬') {
		t.Errorf("the x axis %q marks no tick", lines[len(lines)-2])
	}
}

// A drawing too small to hold a plot is none, rather than a broken one.
func TestBrailleTooSmall(t *testing.T) {
	if lines := ramp(1000, 1000).Braille(6, 12); lines != nil {
		t.Errorf("a 6-wide drawing: %q", lines)
	}
	if lines := ramp(1000, 1000).Braille(40, 4); lines != nil {
		t.Errorf("a 4-high drawing: %q", lines)
	}
}

// A steep rise between two columns is drawn as a rise, every row it
// crosses set, not as two dots far apart.
func TestLineFillsASteepRise(t *testing.T) {
	dots := make([][]bool, 10)
	for i := range dots {
		dots[i] = make([]bool, 2)
	}
	line(dots, 0, 9, 1, 0)
	for y := range 10 {
		if !dots[y][0] && !dots[y][1] {
			t.Errorf("row %d of a rise from 9 to 0 is not set", y)
		}
	}
}

// stack is two strips sharing ramp's x axis: the ramp whole, then the ramp
// without readings from gapFrom to gapTo seconds.
func stack(gapFrom, gapTo float64) Chart {
	c := ramp(1000, 1000)
	gapped := ramp(gapFrom, gapTo).Strips[0]
	gapped.Title = "gapped"
	c.Strips = append(c.Strips, gapped)
	return c
}

// Several strips are stacked under the title, each headed by its own, on
// plots that line up -- one gutter for all -- and share the height; a gap
// in one strip is blank in that strip only.
func TestBrailleStacksStrips(t *testing.T) {
	lines := stack(40, 60).Braille(44, 30)
	if len(lines) != 30 {
		t.Fatalf("%d lines, want 30", len(lines))
	}
	var heads []int
	for i, l := range lines {
		if strings.HasSuffix(l, "│ramp") || strings.HasSuffix(l, "│gapped") {
			heads = append(heads, i)
		}
		if i == 0 || i >= len(lines)-2 {
			continue
		}
		if r := []rune(l); r[3] != '│' && r[3] != '┤' {
			t.Errorf("line %d has no axis where the others do: %q", i, l)
		}
	}
	if len(heads) != 2 || heads[0] != 1 {
		t.Fatalf("strip headings on lines %v, want two, the first under the title:\n%s", heads, strings.Join(lines, "\n"))
	}
	// Each strip's rows, without its heading.
	top := append([]string{""}, lines[heads[0]+1:heads[1]]...)
	bottom := append([]string{""}, lines[heads[1]+1:]...)
	cols := 44 - 3 - 1
	c0, c1 := cols*43/100, cols*57/100
	if dotsIn(append(top, "", ""), 3, c0, c1) == 0 {
		t.Error("the whole ramp's strip is blank where only the other has a gap")
	}
	if n := dotsIn(bottom, 3, c0, c1); n != 0 {
		t.Errorf("%d dots in the gapped strip's gap:\n%s", n, strings.Join(lines, "\n"))
	}
}

// A height that cannot give every strip two rows is no drawing.
func TestBrailleTooShortForItsStrips(t *testing.T) {
	if lines := stack(1000, 1000).Braille(44, 8); lines != nil {
		t.Errorf("two strips in 8 lines: %q", lines)
	}
}

// Shades are marked beneath the x labels, a row a kind over a legend,
// within the height asked for: each where its stretch is, two a character
// or more apart kept apart, and one too short to cover a character's
// middle marked all the same.
func TestBrailleShades(t *testing.T) {
	c := ramp(1000, 1000)
	c.Shades = []Shade{
		{From: 10, To: 20, Kind: ShadeInterval}, {From: 23, To: 33, Kind: ShadeInterval},
		{From: 60.5, To: 60.9, Kind: ShadeInterval}, {From: 80, To: 90, Kind: ShadeClimb},
	}
	lines := c.Braille(44, 14)
	if len(lines) != 14 {
		t.Fatalf("%d lines, want 14", len(lines))
	}
	// The gutter is "100" and the axis: the plot's 40 characters, 2.5 s
	// each, start at the fifth.
	intervals, climbs := []rune(lines[11]), []rune(lines[12])
	at := func(row []rune, sec float64) rune {
		if i := 4 + int(sec/2.5); i < len(row) {
			return row[i]
		}
		return ' '
	}
	for _, sec := range []float64{11, 19, 24, 32, 60.7} {
		if at(intervals, sec) != '━' {
			t.Errorf("no interval marked at %.1f s: %q", sec, string(intervals))
		}
	}
	for _, sec := range []float64{5, 21.3, 45, 85} {
		if at(intervals, sec) != ' ' {
			t.Errorf("an interval marked at %.1f s: %q", sec, string(intervals))
		}
	}
	if at(climbs, 85) != '╱' || at(climbs, 50) != ' ' {
		t.Errorf("climbs %q, want one at 80 to 90 s", string(climbs))
	}
	if lines[13] != "    ━ intervals   ╱ climbs" {
		t.Errorf("legend %q", lines[13])
	}
	plain := ramp(1000, 1000).Braille(44, 14)
	if strings.Contains(strings.Join(plain, "\n"), "intervals") || len(plain) != 14 {
		t.Errorf("a chart without shades has a legend or the wrong height:\n%s", strings.Join(plain, "\n"))
	}
}
