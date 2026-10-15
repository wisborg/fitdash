package plot

// Column is what one column of a drawing shows: the readings whose x falls
// in it, or that none does.
//
// A column holds the mean, which is what the line passes through, and the
// extremes, which a renderer may draw as a band behind it: at a few hundred
// columns for an hour's samples, a column stands for many readings, and the
// mean alone would hide how far they spread.
type Column struct {
	// Ok says a reading falls in the column. A column without one draws
	// nothing.
	Ok bool
	// Mean, Min and Max are the column's readings', meaningful only where
	// Ok.
	Mean, Min, Max float64
	// Joined says the line runs into this column from the previous column
	// with a reading: the two hold readings of one segment. A column whose
	// readings begin a segment is not joined, whatever lies before it.
	Joined bool
}

// Bin is n columns spanning x0 to x1, each holding the readings of segments
// whose x falls in it. A reading outside the span is left out. n of zero or
// less, or an empty span, is no columns.
//
// Columns between two of one segment's readings, holding none of their own,
// are left empty and the later column is joined: a sparse recording drawn
// wide is still one line. Only a break between segments leaves the line
// broken.
func Bin(segments [][]Point, x0, x1 float64, n int) []Column {
	if n <= 0 || x1 <= x0 {
		return nil
	}
	cols := make([]Column, n)
	sums := make([]float64, n)
	counts := make([]int, n)
	for _, seg := range segments {
		prev := -1
		for _, p := range seg {
			if p.X < x0 || p.X > x1 {
				// Leaving the span breaks the line, as a missing reading
				// does: what is joined to what outside it is not drawn.
				prev = -1
				continue
			}
			c := min(int((p.X-x0)/(x1-x0)*float64(n)), n-1)
			if counts[c] == 0 {
				cols[c] = Column{Ok: true, Min: p.Y, Max: p.Y}
			}
			if prev >= 0 && prev < c {
				cols[c].Joined = true
			}
			cols[c].Min, cols[c].Max = min(cols[c].Min, p.Y), max(cols[c].Max, p.Y)
			sums[c] += p.Y
			counts[c]++
			prev = c
		}
	}
	for c := range cols {
		if counts[c] > 0 {
			cols[c].Mean = sums[c] / float64(counts[c])
		}
	}
	return cols
}
