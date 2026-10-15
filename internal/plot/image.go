package plot

import (
	"image"
	"image/color"
	"math"

	"github.com/wisborg/fitdash/internal/panel"
)

// Image is the chart drawn as a picture width by height pixels in theme,
// its text in fonts: the line over a faint band of each column's lowest to
// highest reading, on a grid at the scales' ticks, labelled as the braille
// chart is labelled. A picture too small to hold a plot is nil.
//
// It is drawn with the dashboard's own Canvas and themes, so a chart sits
// beside a render's frames in the same type and palette. Text is sized from
// the picture's shorter side, as the dashboard's is.
func (c Chart) Image(width, height int, theme panel.Theme, fonts *panel.FaceCache) (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	px := math.Max(float64(min(width, height))/32, 10)
	cv, err := panel.NewCanvas(img, px, theme, fonts)
	if err != nil {
		return nil, err
	}
	cv.Fill(theme.Background)

	// The plot's box: room above for the title, to the left for the y
	// labels, below for the x labels, and a margin all round.
	margin := px
	gutter := 0.0
	for _, v := range c.Y.Ticks() {
		w, _, err := cv.MeasureText(c.Y.Label(v), px)
		if err != nil {
			return nil, err
		}
		gutter = math.Max(gutter, w)
	}
	plot := panel.Box{X: margin + gutter + px/2, Y: margin + px*2}
	plot.W = float64(width) - plot.X - margin
	plot.H = float64(height) - plot.Y - margin - px*2
	if plot.W < 20 || plot.H < 20 {
		return nil, nil
	}
	xAt := func(v float64) float64 {
		f, _ := c.X.Frac(v)
		return plot.X + f*plot.W
	}
	yAt := func(v float64) float64 {
		f, _ := c.Y.Frac(v)
		return plot.Y + (1-f)*plot.H
	}

	grid := faded(theme.Dim, 0.25)
	thin := math.Max(1, px/16)
	for _, v := range c.Y.Ticks() {
		y := yAt(v)
		cv.Polyline([]float64{plot.X, plot.X + plot.W}, []float64{y, y}, thin, grid)
		_ = cv.Text(c.Y.Label(v), plot.X-px/2, y, 1, 0.5, px, theme.Dim)
	}
	right := -math.MaxFloat64 // where the last x label ended
	for _, v := range c.X.Ticks() {
		x := xAt(v)
		cv.Polyline([]float64{x, x}, []float64{plot.Y, plot.Y + plot.H}, thin, grid)
		label := c.X.Label(v)
		w, _, _ := cv.MeasureText(label, px)
		// Centred on its tick, kept clear of the y labels -- the first
		// starts at the axis rather than under the lowest of them -- and
		// inside the picture, and left out if it would touch the label
		// before it.
		left := math.Min(math.Max(x-w/2, plot.X), float64(width)-margin-w)
		if left < right+px {
			continue
		}
		_ = cv.Text(label, left, plot.Y+plot.H+px*0.9, 0, 0, px, theme.Dim)
		right = left + w
	}
	axis := []float64{plot.X, plot.X, plot.X + plot.W}
	cv.Polyline(axis, []float64{plot.Y, plot.Y + plot.H, plot.Y + plot.H}, thin, theme.Dim)
	_ = cv.Text(c.Title, plot.X-gutter-px/2, margin, 0, 0, px*1.15, theme.Foreground)

	// The readings: a column a pixel, the band behind and the line over it.
	// A column without a reading draws neither, and the line runs from
	// column to column only where the model joined them.
	cols := Bin(c.Segments, c.X.Min, c.X.Max, int(plot.W))
	band := faded(theme.Accent, 0.22)
	var xs, ys []float64
	flush := func() {
		if len(xs) == 1 {
			// A lone reading is no line: a dot, so it is not lost.
			cv.Circle(xs[0], ys[0], thin*1.5, theme.Accent)
		}
		cv.Polyline(xs, ys, math.Max(1.5, px/8), theme.Accent)
		xs, ys = xs[:0], ys[:0]
	}
	for i, col := range cols {
		if !col.Ok {
			continue
		}
		x := plot.X + float64(i) + 0.5
		lo, hi := yAt(col.Min), yAt(col.Max)
		cv.Rect(panel.Box{X: x - 0.5, Y: math.Min(lo, hi), W: 1, H: math.Max(math.Abs(lo-hi), 1)}, band)
		if !col.Joined {
			flush()
		}
		xs, ys = append(xs, x), append(ys, yAt(col.Mean))
	}
	flush()
	return img, nil
}

// faded is col at alpha a, to draw over what is already there.
func faded(col color.Color, a float64) color.NRGBA {
	r, g, b, _ := col.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(math.Round(a * 255))}
}
