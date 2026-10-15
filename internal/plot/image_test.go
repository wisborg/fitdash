package plot

import (
	"image"
	"image/color"
	"testing"

	"github.com/wisborg/fitdash/internal/panel"
)

func chartImage(t *testing.T, c Chart, w, h int) *image.RGBA {
	t.Helper()
	fonts, err := panel.NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	img, err := c.Image(w, h, panel.LightTheme(), fonts)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// near says a and b are within a few levels of each other in every
// channel: the line's edges are blended into what is behind them.
func near(a, b color.Color, tol int) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) bool { return int(x>>8)-int(y>>8) <= tol && int(y>>8)-int(x>>8) <= tol }
	return d(ar, br) && d(ag, bg) && d(ab, bb)
}

// accentIn counts the pixels of the line's colour in the columns from x0 up
// to x1.
func accentIn(img *image.RGBA, x0, x1 int) int {
	return accentWithin(img, x0, x1, 0, img.Bounds().Dy())
}

// accentWithin counts them in the rows from y0 up to y1 too.
func accentWithin(img *image.RGBA, x0, x1, y0, y1 int) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if near(img.At(x, y), panel.LightTheme().Accent, 12) {
				n++
			}
		}
	}
	return n
}

// The picture is the size asked for, on the theme's background, with the
// line drawn in its accent.
func TestImageDrawsTheLine(t *testing.T) {
	img := chartImage(t, ramp(1000, 1000), 800, 300)
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 300 {
		t.Fatalf("a picture %v, want 800 by 300", b)
	}
	if !near(img.At(799, 299), panel.LightTheme().Background, 0) {
		t.Errorf("the corner is %v, not the background", img.At(799, 299))
	}
	if accentIn(img, 0, 800) < 300 {
		t.Error("the line is not drawn")
	}
}

// A stretch without readings has no line and no band in it: not a line
// across, and nothing at the bottom where a zero would be.
func TestImageLeavesAGapEmpty(t *testing.T) {
	whole := chartImage(t, ramp(1000, 1000), 800, 300)
	gapped := chartImage(t, ramp(40, 60), 800, 300)
	// The plot spans most of the picture; 45 to 55 s of 100 is well inside
	// the gap whatever its margins.
	x0, x1 := 800*47/100, 800*53/100
	if accentIn(whole, x0, x1) == 0 {
		t.Fatal("the unbroken ramp draws nothing where the gap would be; the test is measuring the wrong place")
	}
	if n := accentIn(gapped, x0, x1); n != 0 {
		t.Errorf("%d pixels of line drawn where there were no readings", n)
	}
}

// A picture too small to hold a plot is none.
func TestImageTooSmall(t *testing.T) {
	if img := chartImage(t, ramp(1000, 1000), 40, 30); img != nil {
		t.Errorf("a 40 by 30 picture was drawn")
	}
}

// Two strips are stacked, the upper above the middle of the picture and
// the lower below it, each drawing its own line; a gap in the lower is
// empty there only.
func TestImageStacksStrips(t *testing.T) {
	img := chartImage(t, stack(40, 60), 800, 600)
	x0, x1 := 800*47/100, 800*53/100
	if accentWithin(img, x0, x1, 0, 300) == 0 {
		t.Error("the upper strip draws nothing over the lower strip's gap")
	}
	if accentWithin(chartImage(t, stack(1000, 1000), 800, 600), x0, x1, 300, 600) == 0 {
		t.Fatal("the lower strip draws nothing there even without its gap; the test is measuring the wrong place")
	}
	if n := accentWithin(img, x0, x1, 300, 600); n != 0 {
		t.Errorf("%d pixels of line in the lower strip's gap", n)
	}
}
