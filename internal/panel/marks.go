package panel

// This file holds the geometry MarkerPanel's ribbon and ElevationPanel's
// baseline strip both need to place a highlight's block or a label's tick on
// a one-dimensional axis: widen a span that would otherwise round away to
// nothing, pull touching neighbours apart once everything is placed, clamp a
// name's anchor so it cannot run off the box it has to fit, and size a
// row's text once against the longest name in it rather than per name.
//
// None of it knows which axis it is being asked about -- time, on the
// ribbon; distance, on the profile -- and that is the point. Before this
// file existed the ribbon had one copy of each rule and the profile was
// about to grow a second, independently tuned copy of the same rule under a
// different name. A THIRD one -- after the route panel's own
// extendMarkAlongPolyline, which answers a genuinely different question over
// pixel-space polyline arc length rather than a linear axis, see route.go's
// own comment on why that one does NOT transfer -- is exactly what this
// extraction exists to prevent.

// widenToMinimum expands [x0, x1] about its own midpoint until it measures at
// least min, leaving it untouched when it already does.
//
// A span that rounds to a sliver -- a highlight lasting one frame on a long
// render, a highlight covering a few metres on a long axis -- is present in
// the data and invisible on screen once drawn at its literal width, which
// this project treats as the same silent failure as a panel that declines
// without saying so. Widening about the midpoint keeps the span centred
// where it actually falls rather than sliding it toward one edge.
//
// The widened width is never a claim about how much time or ground the
// original span covered -- it is the axis's own floor, the smallest span
// that axis is capable of expressing at all.
func widenToMinimum(x0, x1, min float64) (float64, float64) {
	if x1-x0 >= min {
		return x0, x1
	}
	mid := (x0 + x1) / 2
	return mid - min/2, mid + min/2
}

// separateSpans pulls adjacent, touching or overlapping spans apart by a
// hairline so two abutting marks read as two rather than one bar, requiring
// at least gap of background between any pair once it returns.
//
// It is a SINGLE left-to-right sweep, correct only because spans arrives
// already sorted ascending by X -- true of both callers by construction:
// MarkerPanel's blocks come from Highlights sorted by From, and
// ElevationPanel's come from a distance axis that is monotone
// non-decreasing in time (see elevation.go's own doc comment on the time-to-
// distance mapping), so only ADJACENT pairs can possibly touch. It mutates
// spans in place, adjusting each pair's shared boundary by splitting the
// shortfall between them -- the earlier span's right edge moves left by
// half, the later span's left edge moves right by half -- so neither span
// is blamed alone for the two having collided.
func separateSpans(spans []Box, gap float64) {
	for i := 1; i < len(spans); i++ {
		short := gap - (spans[i].X - (spans[i-1].X + spans[i-1].W))
		if short <= 0 {
			continue
		}
		spans[i-1].W -= short / 2
		spans[i].X += short / 2
		spans[i].W -= short / 2
	}
}

// clampAnchor bounds a text anchor centred at anchor, half as wide as the
// name it will draw, so it stays inside [lo, hi] -- the box's own inset
// edges -- falling back to center when the name is too wide to fit inside
// [lo, hi] at all (lo+half > hi-half).
//
// This is the name-anchor rule MarkerPanel wrote twice, once for a
// highlight's block and once for a label's tick, before this extraction, and
// ElevationPanel is about to need the identical rule twice more for the same
// reason: a mark near either end of a narrow box must not print its name
// half off the panel.
func clampAnchor(anchor, half, lo, hi, center float64) float64 {
	lo, hi = lo+half, hi-half
	switch {
	case lo > hi:
		return center
	case anchor < lo:
		return lo
	case anchor > hi:
		return hi
	default:
		return anchor
	}
}

// fitLongestName measures every name in names at px and, if any is
// non-empty, shrinks px so the single LONGEST one fits within avail --
// never against whichever name is active right now.
//
// Sizing per-name would make the text grow and shrink as the render moves
// from one mark to the next, which is exactly the jitter this rule exists
// to prevent (the same rule clockTemplate already follows in elapsed.go).
// fonts == nil, or no name in the set being non-empty, returns px
// unchanged.
func fitLongestName(fonts *FaceCache, names []string, px, avail float64) float64 {
	if fonts == nil {
		return px
	}
	var longest string
	var longestW float64
	for _, name := range names {
		if name == "" {
			continue
		}
		if w, _, err := fonts.Measure(name, px); err == nil && w > longestW {
			longest, longestW = name, w
		}
	}
	if longest == "" {
		return px
	}
	if fit, err := fonts.FitSize(longest, avail, px); err == nil {
		return fit
	}
	return px
}
