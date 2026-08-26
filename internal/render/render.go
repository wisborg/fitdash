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
	"image/draw"

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
	f := panel.Frame{
		Index:   i,
		At:      at,
		Elapsed: r.ctx.Timer.Elapsed(at),
		Active:  r.ctx.Timer.Active(at),
		Paused:  r.ctx.Timer.Paused(at),

		HasTimerEvents: r.ctx.Timer.HasTimerEvents(),
	}
	if s, ok := r.ctx.Track.AtWithGap(at, maxGap); ok {
		f.Sample, f.HasSample = s, true
	}
	return f
}

// canvas wraps img for drawing with this render's theme and font cache.
func (r *Renderer) canvas(img *image.RGBA) (*panel.Canvas, error) {
	return panel.NewCanvas(img, r.basePx, r.theme, r.faces)
}

// RenderStatic clears img to the background and draws every panel's invariant
// content once.
//
// The result is the base every frame is composited onto. Nothing per-frame is
// in scope here, because Painter.Static takes no Frame.
func (r *Renderer) RenderStatic(img *image.RGBA) error {
	c, err := r.canvas(img)
	if err != nil {
		return err
	}
	c.Fill(r.theme.Background)
	for _, p := range r.painters {
		p.Static(c)
	}
	return nil
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
	return nil
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
func (r *Renderer) Render(img *image.RGBA, f panel.Frame) error {
	if err := r.RenderStatic(img); err != nil {
		return err
	}
	return r.RenderDynamic(img, f)
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
		// Restore the static base rather than redrawing it: copying pixels is
		// the entire reason the static layer exists, and at 45,000 frames the
		// difference between a copy and re-rasterizing every axis and label is
		// the difference between a render and an afternoon.
		draw.Draw(buf, buf.Bounds(), base, image.Point{}, draw.Src)

		f := r.Frame(i)
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
