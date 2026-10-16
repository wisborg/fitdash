package plot

import (
	"math"
	"strings"
)

// ShadeKind is what a shaded stretch of a chart is.
type ShadeKind int

const (
	// ShadeInterval is an interval of a set: the hard part of a workout.
	ShadeInterval ShadeKind = iota
	// ShadeClimb is a climb.
	ShadeClimb
)

// Name is the kind as a chart's legend names it.
func (k ShadeKind) Name() string {
	if k == ShadeClimb {
		return "climbs"
	}
	return "intervals"
}

// glyph is the kind's mark in a braille chart's row of shades: a heavy
// rule for an interval, a slope for a climb.
func (k ShadeKind) glyph() rune {
	if k == ShadeClimb {
		return '╱'
	}
	return '━'
}

// Shade is a stretch of the x axis a chart marks: an interval, or a climb,
// from From to To in the x axis's own units.
//
// A shade is the same stretch in every strip -- it is a fact about the
// activity, not about one metric -- so it is the chart's, not a strip's.
type Shade struct {
	From, To float64
	Kind     ShadeKind
}

// shadeKinds are the kinds among shades, in ShadeKind's order: the rows
// of a braille chart's shades, and the entries of a legend.
func shadeKinds(shades []Shade) []ShadeKind {
	var kinds []ShadeKind
	for _, k := range []ShadeKind{ShadeInterval, ShadeClimb} {
		for _, s := range shades {
			if s.Kind == k {
				kinds = append(kinds, k)
				break
			}
		}
	}
	return kinds
}

// ShadeRows is how many of a braille chart's lines shades of kinds kinds
// take: a row each and a legend beneath them, or none for none.
func ShadeRows(kinds int) int {
	if kinds == 0 {
		return 0
	}
	return kinds + 1
}

// brailleShades is the chart's shades in text, beneath its x labels: a
// row for each kind, cols characters across the plot past a gutter gutter
// wide, and a legend naming each kind by its mark. A character is marked
// where a shade covers its middle, so that two shades a character or more
// apart -- two intervals and the recovery between -- stay apart; a shade
// too short to cover any middle marks the character it falls in, so that
// it is never lost.
func (c Chart) brailleShades(gutter, cols int) []string {
	kinds := shadeKinds(c.Shades)
	if len(kinds) == 0 {
		return nil
	}
	pad := strings.Repeat(" ", gutter+1)
	span := c.X.Max - c.X.Min
	var out, legend []string
	for _, k := range kinds {
		row := []rune(strings.Repeat(" ", cols))
		for _, s := range c.Shades {
			if s.Kind != k || span <= 0 {
				continue
			}
			// The characters whose middles the shade covers, or the one
			// its middle falls in where it is too short to cover any.
			from := int(math.Ceil((s.From-c.X.Min)/span*float64(cols) - 0.5))
			to := int(math.Floor((s.To-c.X.Min)/span*float64(cols) - 0.5))
			if from > to {
				from = int(((s.From+s.To)/2 - c.X.Min) / span * float64(cols))
				to = from
			}
			for x := max(from, 0); x <= min(to, cols-1); x++ {
				row[x] = k.glyph()
			}
		}
		out = append(out, pad+strings.TrimRight(string(row), " "))
		legend = append(legend, string(k.glyph())+" "+k.Name())
	}
	return append(out, pad+strings.Join(legend, "   "))
}
