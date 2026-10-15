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
		Title:    "ramp",
		X:        ExactScale(0, 100, 5, Clock),
		Y:        NewScale(0, 100, 4, Number),
		Segments: Segments(ps, time.Second),
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
