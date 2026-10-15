package plot

import (
	"image"
	"image/color"
	"math"

	"github.com/wisborg/fitdash/internal/panel"
)

// Image is the chart drawn as a picture width by height pixels in theme,
// its text in fonts: each strip's line over a faint band of each column's
// lowest to highest reading, on a grid at the scales' ticks, the strips one
// above the other sharing the x axis beneath the last, labelled as the
// braille chart is labelled. A picture too small to hold a plot is nil.
//
// It is drawn with the dashboard's own Canvas and themes, so a chart sits
// beside a render's frames in the same type and palette. Text is sized from
// the picture's width, and its height shared between its strips, so that a
// stack of several reads at the size one does.
func (c Chart) Image(width, height int, theme panel.Theme, fonts *panel.FaceCache) (*image.RGBA, error) {
	n := len(c.Strips)
	if n == 0 {
		return nil, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	px := TextPx(width, height, n)
	cv, err := panel.NewCanvas(img, px, theme, fonts)
	if err != nil {
		return nil, err
	}
	cv.Fill(theme.Background)

	// The plots' box: room above for the title, to the left for the y
	// labels -- one gutter for every strip, the widest's, so their plots
	// line up -- below for the x labels, and a margin all round.
	margin := px
	gutter := 0.0
	for _, s := range c.Strips {
		for _, v := range s.Y.Ticks() {
			w, _, err := cv.MeasureText(s.Y.Label(v), px)
			if err != nil {
				return nil, err
			}
			gutter = math.Max(gutter, w)
		}
	}
	all := panel.Box{X: margin + gutter + px/2, Y: margin + px*2}
	all.W = float64(width) - all.X - margin
	all.H = float64(height) - all.Y - margin - px*2
	// Where there are several strips, each is headed by its title, in a
	// gap that clears the lowest y label of the strip above and the
	// highest of its own, each of which reaches half a line beyond its
	// strip.
	head := 0.0
	if n > 1 {
		head = px * 2.6
	}
	stripH := (all.H - float64(n)*head) / float64(n)
	if all.W < 20 || stripH < 20 {
		return nil, nil
	}
	xAt := func(v float64) float64 {
		f, _ := c.X.Frac(v)
		return all.X + f*all.W
	}

	grid := faded(theme.Dim, 0.25)
	thin := math.Max(1, px/16)
	for i, s := range c.Strips {
		box := panel.Box{X: all.X, Y: all.Y + float64(i)*(head+stripH) + head, W: all.W, H: stripH}
		if n > 1 {
			_ = cv.Text(s.Title, box.X, box.Y-head/2, 0, 0.5, px, theme.Foreground)
		}
		for _, v := range c.X.Ticks() {
			x := xAt(v)
			cv.Polyline([]float64{x, x}, []float64{box.Y, box.Y + box.H}, thin, grid)
		}
		s.draw(cv, box, c.X, px, thin, grid, theme)
	}

	bottom := all.Y + all.H
	right := -math.MaxFloat64 // where the last x label ended
	for _, v := range c.X.Ticks() {
		label := c.X.Label(v)
		w, _, _ := cv.MeasureText(label, px)
		// Centred on its tick, kept clear of the y labels -- the first
		// starts at the axis rather than under the lowest of them -- and
		// inside the picture, and left out if it would touch the label
		// before it.
		left := math.Min(math.Max(xAt(v)-w/2, all.X), float64(width)-margin-w)
		if left < right+px {
			continue
		}
		_ = cv.Text(label, left, bottom+px*0.9, 0, 0, px, theme.Dim)
		right = left + w
	}
	cv.Polyline([]float64{all.X, all.X + all.W}, []float64{bottom, bottom}, thin, theme.Dim)
	_ = cv.Text(c.Title, all.X-gutter-px/2, margin, 0, 0, px*1.15, theme.Foreground)
	return img, nil
}

// TextPx is the size of a chart's text, width by height pixels and strips
// strips tall: a thirty-second of its shorter side -- of each strip's
// share of the height, where there are several, so a stack reads at the
// size of one -- and never under ten pixels.
func TextPx(width, height, strips int) float64 {
	h := float64(height)
	if strips > 1 {
		h = h * 2 / float64(strips+1)
	}
	return math.Max(math.Min(float64(width), h)/32, 10)
}

// draw draws the strip in box against x: its gridlines and y labels, its
// axis, and its readings -- a column a pixel, the band behind and the line
// over it. A column without a reading draws neither, and the line runs
// from column to column only where the model joined them.
func (s Strip) draw(cv *panel.Canvas, box panel.Box, x Scale, px, thin float64, grid color.Color, theme panel.Theme) {
	yAt := func(v float64) float64 {
		f, _ := s.Y.Frac(v)
		return box.Y + (1-f)*box.H
	}
	for _, v := range s.Y.Ticks() {
		y := yAt(v)
		cv.Polyline([]float64{box.X, box.X + box.W}, []float64{y, y}, thin, grid)
		_ = cv.Text(s.Y.Label(v), box.X-px/2, y, 1, 0.5, px, theme.Dim)
	}
	cv.Polyline([]float64{box.X, box.X}, []float64{box.Y, box.Y + box.H}, thin, theme.Dim)

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
	for i, col := range Bin(s.Segments, x.Min, x.Max, int(box.W)) {
		if !col.Ok {
			continue
		}
		cx := box.X + float64(i) + 0.5
		lo, hi := yAt(col.Min), yAt(col.Max)
		cv.Rect(panel.Box{X: cx - 0.5, Y: math.Min(lo, hi), W: 1, H: math.Max(math.Abs(lo-hi), 1)}, band)
		if !col.Joined {
			flush()
		}
		xs, ys = append(xs, cx), append(ys, yAt(col.Mean))
	}
	flush()
}

// faded is col at alpha a, to draw over what is already there.
func faded(col color.Color, a float64) color.NRGBA {
	r, g, b, _ := col.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(math.Round(a * 255))}
}
