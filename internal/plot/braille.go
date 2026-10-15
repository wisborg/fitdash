package plot

import (
	"strings"
	"unicode/utf8"
)

// Chart is a series ready to draw: its segments, the scales it is drawn
// on, and what it is called.
type Chart struct {
	// Title names what is drawn, against what: "Power (W) against time".
	Title string
	// X and Y are the chart's scales; see ExactScale and NewScale.
	X, Y Scale
	// Segments are the readings, as Segments and Smooth leave them.
	Segments [][]Point
}

// brailleBits is the bit each dot of a braille cell sets, by row and
// column: the cell is two dots wide and four tall, and Unicode numbers the
// first six dots down the left column and then the right, and the last two
// across the bottom.
var brailleBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

const brailleBlank = rune(0x2800)

// Braille is the chart drawn in text: the line in braille dots, two by four
// to a character, beneath the title, with the y scale's labels to its left
// and the x scale's beneath it. width and height are the whole drawing's in
// characters, labels and all; a drawing too small to hold a plot is none.
//
// Text is the chart's lowest resolution, and the one place a renderer's
// choices are seen dot by dot, so it follows the model to the letter: a
// column's mean is its dot, a line joins two columns only where Bin joined
// them, and a column without a reading is left blank -- never dropped to
// the bottom, which would read as a zero.
func (c Chart) Braille(width, height int) []string {
	yTicks := c.Y.Ticks()
	gutter := 0
	for _, v := range yTicks {
		gutter = max(gutter, utf8.RuneCountInString(c.Y.Label(v)))
	}
	// The gutter's labels, the axis, and the plot; above it the title and
	// below it the axis and its labels.
	cols, rows := width-gutter-1, height-3
	if cols < 4 || rows < 2 {
		return nil
	}
	w, h := cols*2, rows*4
	dots := make([][]bool, h)
	for i := range dots {
		dots[i] = make([]bool, w)
	}
	row := func(y float64) int {
		f, _ := c.Y.Frac(y)
		return int((1-f)*float64(h-1) + 0.5)
	}
	prev, prevRow := -1, 0
	for x, col := range Bin(c.Segments, c.X.Min, c.X.Max, w) {
		if !col.Ok {
			continue
		}
		r := row(col.Mean)
		if col.Joined && prev >= 0 {
			line(dots, prev, prevRow, x, r)
		}
		dots[r][x] = true
		prev, prevRow = x, r
	}

	// The y labels sit on the rows of character their ticks fall in; a
	// tick sharing a row with one already labelled is not labelled, rather
	// than the two written over each other.
	labels := make([]string, rows)
	for _, v := range yTicks {
		r := row(v) / 4
		if labels[r] == "" {
			labels[r] = c.Y.Label(v)
		}
	}

	out := []string{c.Title}
	for r := range rows {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", gutter-utf8.RuneCountInString(labels[r])))
		b.WriteString(labels[r])
		if labels[r] != "" {
			b.WriteRune('┤')
		} else {
			b.WriteRune('│')
		}
		for x := range cols {
			ch := brailleBlank
			for dy := range 4 {
				for dx := range 2 {
					if dots[r*4+dy][x*2+dx] {
						ch |= brailleBits[dy][dx]
					}
				}
			}
			b.WriteRune(ch)
		}
		out = append(out, b.String())
	}
	axis, xLabels := c.xAxis(cols)
	pad := strings.Repeat(" ", gutter)
	return append(out, pad+"└"+axis, pad+" "+xLabels)
}

// xAxis is the x axis drawn cols characters long, a mark at each tick, and
// the line of labels beneath it: each label centred on its tick where it
// fits, the first starting at its tick and none past the end, and any that
// would touch the one before it left out.
func (c Chart) xAxis(cols int) (axis, labels string) {
	marks := []rune(strings.Repeat("─", cols))
	text := []rune(strings.Repeat(" ", cols))
	end := -2 // the last character a label used: none yet
	for _, v := range c.X.Ticks() {
		f, _ := c.X.Frac(v)
		at := min(int(f*float64(cols*2-1))/2, cols-1)
		marks[at] = '┬'
		l := []rune(c.X.Label(v))
		start := max(at-len(l)/2, 0)
		if start+len(l) > cols {
			start = cols - len(l)
		}
		if start <= end+1 || start < 0 {
			// Touching the label before it, or wider than the axis.
			continue
		}
		copy(text[start:], l)
		end = start + len(l) - 1
	}
	return string(marks), strings.TrimRight(string(text), " ")
}

// line sets the dots from (x0, y0) to (x1, y1), x0 left of x1, so that
// every column between holds one and every row the line crosses in a
// column is set: a steep rise between two columns is drawn as the rise it
// is, not as two dots far apart.
func line(dots [][]bool, x0, y0, x1, y1 int) {
	for x := x0; x <= x1; x++ {
		// The rows the line spans across this column: from where it
		// enters to where it leaves.
		from := y0 + (y1-y0)*(2*(x-x0)-1)/(2*max(x1-x0, 1))
		to := y0 + (y1-y0)*(2*(x-x0)+1)/(2*max(x1-x0, 1))
		if x == x0 {
			from = y0
		}
		if x == x1 {
			to = y1
		}
		lo, hi := min(from, to), max(from, to)
		lo, hi = max(lo, min(y0, y1)), min(hi, max(y0, y1))
		for y := lo; y <= hi; y++ {
			dots[y][x] = true
		}
	}
}
