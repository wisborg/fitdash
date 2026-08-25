package panel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
)

// Theme is the palette every panel draws from.
//
// A panel never names a colour literal, for the same reason it never names a
// pixel constant: eight panels each picking their own grey is eight programs
// in one frame. Anything a panel wants to say -- this is a reading, this is
// chrome, this is missing -- it says by choosing the role, and the theme
// decides what the role looks like.
type Theme struct {
	// Background fills the frame. There is no source video beneath it: in
	// fitdash the background IS the product, which is also what lets a
	// declining panel leave clean background rather than a hole.
	Background color.Color

	// Foreground is a live reading -- the numbers the dashboard exists to
	// show.
	Foreground color.Color

	// Dim is chrome: axes, rules, labels, the un-covered part of a route.
	// Present, but not what the eye should land on.
	Dim color.Color

	// Accent marks the current position -- the route dot, a playhead.

	Accent color.Color

	// Absent is the colour of a placeholder: the "--" a panel draws where a
	// reading would be if the sensor had one.
	//
	// It is a distinct role rather than a reuse of Dim because the two say
	// different things. Dim means "this is chrome"; Absent means "there is no
	// data here", which is a fact about the activity rather than about the
	// drawing. A viewer has to be able to tell a missing heart rate from an
	// axis label without knowing which is which in advance, so this must stay
	// visibly different from BOTH Foreground and Background.
	Absent color.Color
}

// DefaultTheme is the shipped palette: a dark dashboard with a warm accent.
func DefaultTheme() Theme {
	return Theme{
		Background: color.RGBA{0x0E, 0x0E, 0x10, 0xFF},
		Foreground: color.RGBA{0xF2, 0xF2, 0xF4, 0xFF},
		Dim:        color.RGBA{0x6E, 0x6E, 0x78, 0xFF},
		Accent:     color.RGBA{0xFF, 0x5A, 0x36, 0xFF},
		Absent:     color.RGBA{0x4A, 0x4A, 0x54, 0xFF},
	}
}

// FaceCache holds rasterized font faces, keyed by pixel size.
//
// Building a face is expensive and a render asks for the same handful of sizes
// on every one of its frames -- 45,000 times for a 25-minute activity at 30
// fps -- so the cache is not an optimisation so much as the difference between
// a render finishing and not.
//
// It is NOT goroutine-safe by contract, and the mutex below is belt and braces
// rather than a licence to share one across goroutines. Nothing in a render
// writes to it concurrently: a Painter's Dynamic method must not mutate, and
// the two Canvases of one render are used one at a time.
type FaceCache struct {
	mu    sync.Mutex
	font  *opentype.Font
	faces map[int]font.Face
}

// NewFaceCache returns a cache over the embedded monospace face.
//
// Monospace on purpose: a dashboard's numbers change every frame, and in a
// proportional face the readout jitters horizontally as digits swap width.
// That movement reads as instability in the data rather than in the typography.
func NewFaceCache() (*FaceCache, error) {
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, fmt.Errorf("panel: parsing the embedded font: %w", err)
	}
	return &FaceCache{font: f, faces: map[int]font.Face{}}, nil
}

// face returns a face at px pixels, rounded to a whole pixel.
//
// Rounding is what makes the cache effective: a size derived from a fractional
// box dimension is a different float64 on almost every call, so keying on the
// raw value would cache nothing while growing without bound. A sub-pixel
// difference in text size is not visible; the unbounded map is.
func (c *FaceCache) face(px float64) (font.Face, error) {
	if px < 1 {
		px = 1
	}
	key := int(math.Round(px))

	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.faces[key]; ok {
		return f, nil
	}
	// DPI 72 makes one point exactly one pixel, so a caller asking for a size
	// in pixels gets one -- every dimension in this package is in pixels, and
	// a points-versus-pixels conversion hiding here would silently rescale
	// every layout.
	f, err := opentype.NewFace(c.font, &opentype.FaceOptions{
		Size: float64(key), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("panel: building a %dpx font face: %w", key, err)
	}
	c.faces[key] = f
	return f, nil
}

// Canvas is the drawing surface a panel is handed.
//
// It carries the frame's dimensions, the base text size and the theme, and
// NOTHING that varies from frame to frame. That is deliberate and is one of
// the four things keeping the static layer honest: if a Canvas carried the
// per-frame state, a panel's Static method could reach frame data through the
// drawing surface, which is the same leak the phase split exists to close,
// wearing a different hat.
type Canvas struct {
	// W, H are the frame's pixel dimensions.
	W, H int

	// BasePx is the layout's base text size in pixels, already resolved from
	// its FontScale. A panel sizes its own text relative to this rather than
	// choosing pixels, so one layout reads the same at 1080p and 4K.
	BasePx float64

	// Theme is the palette. See Theme.
	Theme Theme

	dc    *gg.Context
	faces *FaceCache
}

// NewCanvas wraps img for drawing.
//
// gg draws directly into the image's own pixels rather than into a buffer it
// copies out afterwards, so the frame a panel draws on is the frame the
// encoder pipes -- no copy per frame, which at 45,000 frames of 4K is the
// difference between a render and a memory profile.
func NewCanvas(img *image.RGBA, basePx float64, theme Theme, faces *FaceCache) (*Canvas, error) {
	if img == nil {
		return nil, fmt.Errorf("panel: no image to draw on")
	}
	if faces == nil {
		return nil, fmt.Errorf("panel: no font cache")
	}
	if basePx <= 0 {
		return nil, fmt.Errorf("panel: base text size must be positive, got %v", basePx)
	}
	b := img.Bounds()
	return &Canvas{
		W: b.Dx(), H: b.Dy(),
		BasePx: basePx,
		Theme:  theme,
		dc:     gg.NewContextForRGBA(img),
		faces:  faces,
	}, nil
}

// Fill paints the whole frame.
//
// The renderer calls this once per frame before any panel draws, which is what
// makes a declining panel's absence read as clean background rather than as
// whatever the previous frame left there.
func (c *Canvas) Fill(col color.Color) {
	c.dc.SetColor(col)
	c.dc.Clear()
}

// Rect fills b.
func (c *Canvas) Rect(b Box, col color.Color) {
	c.dc.SetColor(col)
	c.dc.DrawRectangle(b.X, b.Y, b.W, b.H)
	c.dc.Fill()
}

// Polyline strokes the path through the given points, which must be the same
// length. Fewer than two points draws nothing -- a single point is not a line,
// and a route with one GPS fix is a real input.
//
// Of the three conditions, only two are load-bearing and that was measured
// rather than assumed. The length-mismatch check prevents an index panic and
// its removal fails the tests. The `< 2` bound does NOT change behaviour:
// relaxing it to `< 1` still guards the empty case, and a single point becomes
// a MoveTo with no LineTo, which the underlying context strokes as nothing.
// It is kept because it states the precondition a caller should reason about
// -- a line needs two points -- rather than relying on a library's tolerance
// for a degenerate path, but no test can distinguish it and none pretends to.
func (c *Canvas) Polyline(xs, ys []float64, width float64, col color.Color) {
	if len(xs) != len(ys) || len(xs) < 2 || width <= 0 {
		return
	}
	c.dc.SetColor(col)
	c.dc.SetLineWidth(width)
	c.dc.MoveTo(xs[0], ys[0])
	for i := 1; i < len(xs); i++ {
		c.dc.LineTo(xs[i], ys[i])
	}
	c.dc.Stroke()
}

// Text draws s at px pixels, anchored at (x, y).
//
// ax and ay place the anchor within the text's own box: 0 is left/top, 0.5 is
// centre, 1 is right/bottom. A panel positioning a readout against the right
// edge of its box passes ax=1 and the box's right edge, and does not have to
// measure the string first -- which matters because the string's width changes
// every frame as the digits do.
func (c *Canvas) Text(s string, x, y, ax, ay, px float64, col color.Color) error {
	if s == "" {
		return nil
	}
	face, err := c.faces.face(px)
	if err != nil {
		return err
	}
	c.dc.SetFontFace(face)
	c.dc.SetColor(col)
	c.dc.DrawStringAnchored(s, x, y, ax, ay)
	return nil
}

// MeasureText returns the pixel extent s would occupy at px.
func (c *Canvas) MeasureText(s string, px float64) (w, h float64, err error) {
	face, err := c.faces.face(px)
	if err != nil {
		return 0, 0, err
	}
	c.dc.SetFontFace(face)
	w, h = c.dc.MeasureString(s)
	return w, h, nil
}

// FitTextSize returns the largest size no greater than px at which s fits
// within maxW.
//
// Panels need this because a Box is a rectangle they must fit rather than an
// anchor they can grow away from: a readout that is comfortable at 1080p can
// overflow the same box in a portrait frame, where the width collapses. The
// result is never below 1 pixel -- a caller with a box too small for even that
// has a layout problem, not a typography one, and Resolve already refuses a
// frame too small for its arrangement.
//
// Scaling by the measured ratio rather than searching: text width is very
// nearly linear in size for a monospace face, so one measurement and one
// correction land within a pixel, where a binary search would cost a dozen
// measurements per frame for no visible difference.
func (c *Canvas) FitTextSize(s string, maxW, px float64) (float64, error) {
	if s == "" || maxW <= 0 || px <= 0 {
		return px, nil
	}
	w, _, err := c.MeasureText(s, px)
	if err != nil {
		return 0, err
	}
	if w <= maxW {
		return px, nil
	}
	scaled := px * maxW / w
	if scaled < 1 {
		scaled = 1
	}
	// One correction pass: rounding to a whole-pixel face size can push the
	// result back over the limit, and a readout one pixel wider than its box
	// is a readout with a clipped digit.
	for scaled > 1 {
		w, _, err := c.MeasureText(s, scaled)
		if err != nil {
			return 0, err
		}
		if w <= maxW {
			break
		}
		scaled--
	}
	return scaled, nil
}
