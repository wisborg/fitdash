// Package render drives a layout's panels over an activity's timeline and
// hands the resulting frames to a sink.
//
// It owns the loop, the static/dynamic composition, and the construction of
// each frame's state. It does not own what a panel is -- that contract, and
// the types a panel is handed, live in internal/panel, because the types that
// define a contract belong with it.
package render

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/encode"
	"github.com/wisborg/fitdash/internal/panel"
)

// maxGap is how far Frame's sample lookup will interpolate across missing
// data before reporting the instant as absent.
//
// It is fitactivity's own default rather than a number chosen here, because it
// is a property of how densely a device records (1 Hz for the files this
// targets) rather than of how a dashboard looks. Beyond it, a straight line
// between two real samples is a fabrication, and the whole absent-data design
// depends on the difference being reported instead of smoothed over.
const maxGap = fitactivity.DefaultMaxGap

// Renderer draws one activity through one layout.
type Renderer struct {
	ctx      *panel.Context
	theme    panel.Theme
	faces    *panel.FaceCache
	basePx   float64
	placed   []panel.Placed
	painters []panel.Painter
	declined []string

	// marginPx is the layout's own margin, in pixels for this render's frame
	// size: Layout.MarginPx resolved once here rather than recomputed per
	// frame. It is what the highlight border is drawn inside of -- see
	// drawHighlightBorder -- and the reason that border can never collide
	// with a panel: Layout.Resolve insets its own working frame by exactly
	// this many pixels (via that same MarginPx) before it divides anything
	// among the tree, so the band this many pixels deep around the edge is
	// the one region of the frame no panel is ever given a Box in. Reading
	// it through the accessor rather than re-deriving `Margin * min(w, h)`
	// here is what keeps that a fact about one formula instead of two that
	// could silently drift apart.
	marginPx float64
}

// New prepares every panel the layout places for this activity.
//
// Whether a panel is placed is decided by its own Accepts, and the decision is
// made BEFORE any box is divided, so a panel that declines contributes nothing
// to its siblings' share and they grow into its space. That is the whole of
// "the layout closes up"; see panel.Layout.Resolve.
func New(ctx *panel.Context, layout panel.Layout, theme panel.Theme) (*Renderer, error) {
	if ctx == nil {
		return nil, fmt.Errorf("render: no context")
	}
	if ctx.Timeline.Frames() <= 0 {
		return nil, fmt.Errorf("render: timeline has no frames")
	}
	// Track and Timer are dereferenced on every frame, so a nil one panics
	// deep inside the loop rather than failing here beside the two checks that
	// were already made. panel.Context is an exported struct with exported
	// fields; a caller can omit either.
	if ctx.Track == nil {
		return nil, fmt.Errorf("render: context has no track")
	}
	if ctx.Timer == nil {
		return nil, fmt.Errorf("render: context has no timer model")
	}
	if ctx.Fonts == nil {
		return nil, fmt.Errorf("render: context has no font cache")
	}

	var declined []string
	keep := func(p panel.Panel) bool {
		if p.Accepts(ctx) {
			return true
		}
		// Recorded rather than merely skipped: a panel that is absent because
		// the activity carries nothing for it must be ANNOUNCED, or "no
		// unexplained holes" holds in the pixels and not in the user's
		// understanding of them.
		declined = append(declined, p.Name())
		return false
	}

	placed, err := layout.Resolve(ctx.Width, ctx.Height, keep)
	if err != nil {
		return nil, err
	}

	r := &Renderer{
		ctx: ctx, theme: theme, faces: ctx.Fonts,
		basePx: ctx.BasePx(), placed: placed, declined: declined,
		// The same formula Layout.Resolve itself uses to inset its working
		// frame before dividing anything among the tree -- read through
		// Layout.MarginPx rather than re-derived here, so the guarantee
		// drawHighlightBorder relies on (the margin band is never given to a
		// panel) rests on one formula, not two that could drift apart.
		marginPx: layout.MarginPx(ctx.Width, ctx.Height),
	}
	for _, p := range placed {
		painter := p.Panel.Prepare(ctx, p.Box)
		if painter == nil {
			return nil, fmt.Errorf("render: panel %q prepared a nil painter", p.Panel.Name())
		}
		r.painters = append(r.painters, painter)
	}
	return r, nil
}

// Frames is the number of frames in the render.
func (r *Renderer) Frames() int { return r.ctx.Timeline.Frames() }

// Placed returns the panels that will draw, with their boxes.
func (r *Renderer) Placed() []panel.Placed { return r.placed }

// Declined returns the names of panels that had nothing to show, for the
// render summary.
func (r *Renderer) Declined() []string { return r.declined }

// Frame builds the per-frame state for frame i.
//
// It is the single place the invariant on panel.Frame.Sample is established:
// when the lookup fails, the sample is the ZERO sample, never a stale one.
// Holding the last good reading through a dropout would keep the dashboard
// from flickering and would make a frozen heart rate indistinguishable from a
// live one.
func (r *Renderer) Frame(i int) panel.Frame {
	at := r.ctx.Timeline.At(i)
	interval, weight := r.ctx.Timeline.IntervalAt(i, r.ctx.HighlightTransition)
	f := panel.Frame{
		Index:   i,
		At:      at,
		Elapsed: r.ctx.Timer.Elapsed(at),
		Active:  r.ctx.Timer.Active(at),
		Paused:  r.ctx.Timer.Paused(at),

		HasTimerEvents: r.ctx.Timer.HasTimerEvents(),

		Interval:       interval,
		IntervalWeight: weight,
	}
	if s, ok := r.ctx.Track.AtWithGap(at, maxGap); ok {
		// Smoothing is applied only where there IS a reading. Deep in a
		// dropout the sample stays zero, so the invariant every panel's single
		// presence check relies on survives -- an average drawn from either
		// side of a gap would fill it in with a number nobody recorded, which
		// is the stale-reading failure wearing arithmetic.
		//
		// The window is resolved per FRAME rather than once for the whole
		// render: see Context.Smoothing and Timeline.AutoSmoothingAt for why
		// a render that carries a slowed highlight cannot have one auto
		// window.
		window := r.ctx.Smoothing.WindowAt(r.ctx.Timeline, i)
		f.Sample, f.HasSample = smoothSample(r.ctx.Track, at, window, s), true
	}
	return f
}

// canvas wraps img for drawing with this render's theme and font cache.
func (r *Renderer) canvas(img *image.RGBA) (*panel.Canvas, error) {
	return r.canvasThemed(img, r.theme)
}

// canvasThemed is canvas with an explicit theme, which only renderStaticWash
// (see below) ever needs: every other caller draws against this render's own
// r.theme, unchanged for its whole life.
func (r *Renderer) canvasThemed(img *image.RGBA, theme panel.Theme) (*panel.Canvas, error) {
	return panel.NewCanvas(img, r.basePx, theme, r.faces)
}

// RenderStatic clears img to the background and draws every panel's invariant
// content once.
//
// The result is the base every frame is composited onto. Nothing per-frame is
// in scope here, because Painter.Static takes no Frame.
func (r *Renderer) RenderStatic(img *image.RGBA) error {
	return r.renderStaticThemed(img, r.theme)
}

// renderStaticThemed is RenderStatic against an explicit theme rather than
// this render's own -- the one seam --highlight-style wash needs and nothing
// else does, which is why it stays unexported rather than becoming a second
// public entry point.
func (r *Renderer) renderStaticThemed(img *image.RGBA, theme panel.Theme) error {
	c, err := r.canvasThemed(img, theme)
	if err != nil {
		return err
	}
	c.Fill(theme.Background)
	for _, p := range r.painters {
		p.Static(c)
	}
	return nil
}

// renderStaticWash draws the SAME static content as RenderStatic, against
// washTheme(r.theme) instead of r.theme -- a second full static base whose
// only difference from the first is its Background. See washTheme for why
// that restriction (identical everywhere else) is what keeps blending the two
// bases from ever blurring a label: every pixel a panel actually inked is
// identical between the two images, so a blend can only ever move a pixel
// that was showing bare background in both.
//
// This is the "two static bases" design --highlight-style wash ships with
// rather than a per-frame Theme. A Canvas carrying a per-frame Theme would
// make the static layer disagree with the dynamic one about what background
// it was drawn against -- the axis-origin trap docs/architecture.md's
// "Per-render and per-frame state" section names -- and Theme is meant to be
// fixed for the whole render (see canvas.go's own Canvas doc comment). Two
// full bases, blended by Frame.IntervalWeight where the loop already knows
// which frames need it, keeps Theme itself untouched and pays for the
// avoidance with a second full-frame buffer instead.
func (r *Renderer) renderStaticWash(img *image.RGBA) error {
	return r.renderStaticThemed(img, washTheme(r.theme))
}

// highlightWashStrength is how far the wash style's alternate background
// moves from the ordinary one toward Theme.Highlight -- a blend fraction, not
// a colour, so this stays compliant with "no colour literals outside Theme":
// the colour itself is always one of the theme's own roles.
//
// A judgement call rather than a derivation, the same way highlightRestAlpha
// is in the highlight panel: strong enough to read as a tint from across the
// room where a highlight is active, restrained enough that Foreground text
// drawn straight over it stays legible. Verified by eye against both shipped
// themes rather than by TestThemesAreLegible, which iterates panel.Themes()
// and has no notion of a background this type derives at render time.
const highlightWashStrength = 0.22

// washTheme is theme with its Background blended toward its own Highlight by
// highlightWashStrength, and every other role -- Foreground, Dim, Accent,
// Absent, Highlight itself -- left untouched. See renderStaticWash for why
// leaving every other role alone is load-bearing rather than incidental.
func washTheme(theme panel.Theme) panel.Theme {
	theme.Background = blendColor(theme.Background, theme.Highlight, highlightWashStrength)
	return theme
}

// blendColor linearly interpolates from a toward b by weight in [0,1],
// channel by channel, in the non-premultiplied space every Theme colour is
// declared in.
func blendColor(a, b color.Color, weight float64) color.Color {
	an := color.NRGBAModel.Convert(a).(color.NRGBA)
	bn := color.NRGBAModel.Convert(b).(color.NRGBA)
	return color.NRGBA{
		R: blendChannel(an.R, bn.R, weight),
		G: blendChannel(an.G, bn.G, weight),
		B: blendChannel(an.B, bn.B, weight),
		A: blendChannel(an.A, bn.A, weight),
	}
}

func blendChannel(a, b uint8, weight float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*weight))
}

// blendBases writes a per-pixel lerp of base toward wash into dst, by weight.
//
// Lerping the raw bytes rather than compositing through image/draw is exact
// here specifically because both source images are the output of
// renderStaticThemed, which Fill covers edge to edge before any panel draws:
// every pixel of both is fully opaque, so there is no alpha to reason about
// and a linear blend of the channels IS the colour blend, not an
// approximation of one.
func blendBases(dst, base, wash *image.RGBA, weight float64) {
	if weight <= 0 {
		copy(dst.Pix, base.Pix)
		return
	}
	if weight >= 1 {
		copy(dst.Pix, wash.Pix)
		return
	}
	for i := range dst.Pix {
		dst.Pix[i] = blendChannel(base.Pix[i], wash.Pix[i], weight)
	}
}

// RenderDynamic draws every panel's per-frame content onto img, which must
// ALREADY hold the static base -- it does not clear.
//
// This is the fast path the loop uses: copy the base, draw what moves.
func (r *Renderer) RenderDynamic(img *image.RGBA, f panel.Frame) error {
	c, err := r.canvas(img)
	if err != nil {
		return err
	}
	for _, p := range r.painters {
		p.Dynamic(c, f)
	}
	// The highlight border is a render-wide overlay, not a panel, and this
	// is the one place this feature is allowed to touch: it belongs to no
	// box in the layout tree, only to the margin Layout.Resolve leaves empty
	// around all of them. Drawn last, though the margin is never under a
	// panel's own box so the order cannot matter today.
	r.drawHighlightBorder(c, f)
	return nil
}

// highlightBorderInsetFraction and highlightBorderThicknessFraction position
// the accent border inside the layout's own margin band. Both are fractions
// of marginPx -- the margin's OWN pixel width, not the frame's -- so the
// border scales with whatever margin the active layout declares rather than
// assuming one. Chosen so inset+thickness stays under 1: the border never
// reaches the frame's outer edge, and never reaches the inner edge where
// Layout.Resolve begins handing out boxes, leaving a visible gap on both
// sides that is what makes it read as a border rather than a filled margin.
const (
	highlightBorderInsetFraction     = 0.25
	highlightBorderThicknessFraction = 0.5
)

// drawHighlightBorder draws the accent-coloured frame the default
// --highlight-style border marks a highlight with, alpha ramping with
// Frame.IntervalWeight -- the same number the highlight strip's own block
// brightens by, so the two animate in lockstep rather than as two
// independent recomputations that could land a frame apart.
//
// Nothing is drawn outside a highlight (Frame.Interval is NoHighlight, or
// the ramp has not started -- see Timeline.IntervalAt), under
// --highlight-style none, or when this layout carries no margin to draw
// inside of at all.
func (r *Renderer) drawHighlightBorder(c *panel.Canvas, f panel.Frame) {
	if r.marginPx <= 0 {
		return
	}
	if r.ctx.HighlightStyle == panel.HighlightStyleNone {
		return
	}
	if f.Interval == panel.NoHighlight || f.IntervalWeight <= 0 {
		return
	}

	inset := r.marginPx * highlightBorderInsetFraction
	thick := r.marginPx * highlightBorderThicknessFraction
	w, h := float64(r.ctx.Width), float64(r.ctx.Height)
	outerW, outerH := w-2*inset, h-2*inset
	if outerW <= 0 || outerH <= 0 || thick <= 0 {
		return
	}

	col := panel.Fade(r.theme.Highlight, f.IntervalWeight)
	x, y := inset, inset
	c.Rect(panel.Box{X: x, Y: y, W: outerW, H: thick}, col)                  // top
	c.Rect(panel.Box{X: x, Y: y + outerH - thick, W: outerW, H: thick}, col) // bottom
	c.Rect(panel.Box{X: x, Y: y, W: thick, H: outerH}, col)                  // left
	c.Rect(panel.Box{X: x + outerW - thick, Y: y, W: thick, H: outerH}, col) // right
}

// Render draws a complete frame from scratch: background, static content, then
// dynamic content.
//
// This is the simple path, and it exists mainly so that it can be COMPARED
// with the fast one. The two must produce identical pixels, and a test asserts
// exactly that -- which is the automated catch for the trap this whole design
// is shaped around. If any panel's static content ever comes to depend on
// something that differs between the two paths, the images differ and the test
// fails with a pixel count, rather than the disagreement shipping in a video.
//
// Recomputing the wash base on every call (see renderBase) rather than
// caching it is the cost of Render staying the "simple path" its own name
// promises: it exists to be compared against the fast static+dynamic split
// Run uses, not to be fast itself.
func (r *Renderer) Render(img *image.RGBA, f panel.Frame) error {
	if err := r.renderBase(img, f); err != nil {
		return err
	}
	return r.RenderDynamic(img, f)
}

// renderBase fills img with the static base frame f is drawn onto: the plain
// static layer, or -- under --highlight-style wash, for a frame inside a
// highlight -- that layer blended toward the washed one by
// Frame.IntervalWeight. This is what Run's own loop does with its two
// precomputed bases (see Run), spelled out per call instead of per render so
// this path and that one apply the exact same rule and can be compared frame
// for frame.
func (r *Renderer) renderBase(img *image.RGBA, f panel.Frame) error {
	if err := r.RenderStatic(img); err != nil {
		return err
	}
	if r.ctx.HighlightStyle != panel.HighlightStyleWash || f.Interval == panel.NoHighlight || f.IntervalWeight <= 0 {
		return nil
	}
	wash := image.NewRGBA(img.Bounds())
	if err := r.renderStaticWash(wash); err != nil {
		return err
	}
	blendBases(img, img, wash, f.IntervalWeight)
	return nil
}

// Run renders every frame into sink.
//
// progress, when non-nil, is called with the frame index and the total after
// each frame. A 45,000-frame render that prints nothing for ten minutes is a
// bug report waiting to happen, but throttling belongs to whoever is
// displaying it rather than here.
//
// The sink is NOT closed here. Its Close is the encode's own verdict -- ffmpeg
// reports a failure at exit, not at write -- so the caller must close it and
// check, and doing it here would hide that error inside a loop's return value.
func Run(ctx context.Context, r *Renderer, sink encode.Sink, progress func(i, n int)) error {
	n := r.Frames()

	base := image.NewRGBA(image.Rect(0, 0, r.ctx.Width, r.ctx.Height))
	if err := r.RenderStatic(base); err != nil {
		return err
	}

	// The wash style's second static base -- see renderStaticWash -- built
	// once here, never per frame, and only when the style asks for it: an
	// ordinary border or none render allocates no second buffer and blends
	// nothing, which is the "opt-in" half of shipping wash at all.
	var washBase *image.RGBA
	if r.ctx.HighlightStyle == panel.HighlightStyleWash {
		washBase = image.NewRGBA(image.Rect(0, 0, r.ctx.Width, r.ctx.Height))
		if err := r.renderStaticWash(washBase); err != nil {
			return err
		}
	}

	// One frame buffer, reused. A fresh RGBA per frame is 33 MB at 4K, which
	// over 45,000 frames is not a leak but is an enormous amount of garbage.
	buf := image.NewRGBA(image.Rect(0, 0, r.ctx.Width, r.ctx.Height))

	// A sink that only needs some frames says so, and the ones it does not
	// need are never drawn. That is what makes --frames a preview rather than
	// a full render with almost all of its output discarded: it took 36
	// seconds to produce five PNGs before this.
	selector, selective := sink.(encode.Selector)

	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("render: cancelled after %d of %d frames: %w", i, n, err)
		}
		if selective && !selector.Wants(i) {
			// Offered to the sink anyway, so it can tell how long the render
			// was -- and reported to progress, so the bar tracks position in
			// the activity rather than how many frames happened to be wanted.
			if progress != nil {
				progress(i+1, n)
			}
			continue
		}
		f := r.Frame(i)

		// Restore the static base rather than redrawing it: copying pixels is
		// the entire reason the static layer exists, and at 45,000 frames the
		// difference between a copy and re-rasterizing every axis and label is
		// the difference between a render and an afternoon. Under wash, inside
		// a highlight, the base is blended toward washBase by IntervalWeight
		// first -- see blendBases and renderBase, which apply exactly the same
		// rule so this loop and Render agree frame for frame.
		if washBase != nil && f.Interval != panel.NoHighlight {
			blendBases(buf, base, washBase, f.IntervalWeight)
		} else {
			draw.Draw(buf, buf.Bounds(), base, image.Point{}, draw.Src)
		}

		if err := r.RenderDynamic(buf, f); err != nil {
			return fmt.Errorf("render: drawing frame %d of %d: %w", i, n, err)
		}
		if err := sink.WriteFrame(i, buf); err != nil {
			return fmt.Errorf("render: writing frame %d of %d: %w", i, n, err)
		}
		if progress != nil {
			progress(i+1, n)
		}
	}
	return nil
}
