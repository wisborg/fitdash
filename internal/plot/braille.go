package plot

import (
	"strings"
	"unicode/utf8"
)

// Chart is what a chart draws: one strip per metric, stacked, sharing the
// x axis beneath the last of them, under one title.
//
// Several metrics are stacked rather than drawn on one plot with an axis
// each side: two scales over one line leave the reader working out which
// line reads against which, and a third has nowhere to go. A strip each
// keeps every line on its own scale, and the shared x axis keeps a moment
// in one strip directly above the same moment in the next -- which is the
// point of charting them together.
type Chart struct {
	// Title names what is drawn, against what: "power (W) against time".
	Title string
	// X is the scale the strips share; see ExactScale.
	X Scale
	// Strips are the metrics, top to bottom.
	Strips []Strip
}

// Strip is one metric of a chart: its readings and the scale they are
// drawn on.
type Strip struct {
	// Title names the metric, "power (W)": written over its strip where a
	// chart has several, and in the chart's Title where it has one.
	Title string
	// Y is the strip's scale; see NewScale.
	Y Scale
	// Segments are the readings, as Segments and Smooth leave them.
	Segments [][]Point
}

// brailleBits is the bit each dot of a braille cell sets, by row and
// column: the cell is two dots wide and four tall, and Unicode numbers the
// first six dots down the left column and then the right, and the last two
// across the bottom.
var brailleBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

const brailleBlank = rune(0x2800)

// Braille is the chart drawn in text: each strip's line in braille dots,
// two by four to a character, with its y scale's labels to its left, the
// strips one above the other beneath the title, and the x scale's labels
// beneath the last. Where there are several strips, each is headed by its
// own title. width and height are the whole drawing's in characters,
// labels and all; the strips share the height evenly, and a drawing too
// small to give each at least two rows is none.
//
// Text is the chart's lowest resolution, and the one place a renderer's
// choices are seen dot by dot, so it follows the model to the letter: a
// column's mean is its dot, a line joins two columns only where Bin joined
// them, and a column without a reading is left blank -- never dropped to
// the bottom, which would read as a zero.
func (c Chart) Braille(width, height int) []string {
	n := len(c.Strips)
	if n == 0 {
		return nil
	}
	// One gutter for every strip, the widest's, so the plots line up and a
	// moment in one is above the same moment in the next.
	gutter := 0
	for _, s := range c.Strips {
		for _, v := range s.Y.Ticks() {
			gutter = max(gutter, utf8.RuneCountInString(s.Y.Label(v)))
		}
	}
	headed := 0
	if n > 1 {
		headed = 1
	}
	// Above the strips the title, below them the axis and its labels.
	cols, rows := width-gutter-1, (height-3-n*headed)/n
	if cols < 4 || rows < 2 {
		return nil
	}
	spare := height - 3 - n*headed - n*rows
	pad := strings.Repeat(" ", gutter)
	out := []string{c.Title}
	for i, s := range c.Strips {
		if headed > 0 {
			out = append(out, pad+"│"+s.Title)
		}
		// The rows left over when the height does not divide go to the
		// first strips, a row each.
		r := rows
		if i < spare {
			r++
		}
		out = append(out, s.braille(c.X, gutter, cols, r)...)
	}
	axis, xLabels := c.xAxis(cols)
	return append(out, pad+"└"+axis, pad+" "+xLabels)
}

// braille is the strip's rows: its line drawn cols characters wide and
// rows tall against x, its y labels in a gutter gutter characters wide.
func (s Strip) braille(x Scale, gutter, cols, rows int) []string {
	w, h := cols*2, rows*4
	dots := make([][]bool, h)
	for i := range dots {
		dots[i] = make([]bool, w)
	}
	row := func(y float64) int {
		f, _ := s.Y.Frac(y)
		return int((1-f)*float64(h-1) + 0.5)
	}
	prev, prevRow := -1, 0
	for x, col := range Bin(s.Segments, x.Min, x.Max, w) {
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
	for _, v := range s.Y.Ticks() {
		r := row(v) / 4
		if labels[r] == "" {
			labels[r] = s.Y.Label(v)
		}
	}

	out := make([]string, 0, rows)
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
	return out
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
