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

// elevationPanelName is ElevationPanel's own Name(), read from the panel
// itself rather than restated as a string literal here -- the same reason
// cmd's markerPanelName is read from MarkerPanel{}.Name() -- so a rename in
// internal/panel cannot silently stop --bottom-band=distance from omitting
// the right panel. See New's keep filter, the one place this is compared
// against.
var elevationPanelName = panel.ElevationPanel{}.Name()

// markerPanelName is MarkerPanel's own Name(), read the same way and for the
// same reason: New's keep filter compares against it to decide whether the
// marker strip is being absorbed into the elevation profile (see
// profileTakesTheBand below), and a rename in internal/panel must not
// silently stop that comparison from matching.
var markerPanelName = panel.MarkerPanel{}.Name()

// Renderer draws one activity through one layout.
type Renderer struct {
	ctx      *panel.Context
	theme    panel.Theme
	faces    *panel.FaceCache
	basePx   float64
	placed   []panel.Placed
	painters []panel.Painter
	declined []string
	omitted  []string
	absorbed []string

	// overlappingLabels holds, ascending, the index into ctx.Labels of every
	// label a placed Painter reports (via panel.LabelOverlapReporter) as
	// still too close to a neighbour after its own bounded nudge -- today
	// only ElevationPanel implements the interface, for its D.4 policy. See
	// OverlappingLabels.
	overlappingLabels []int

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

	// washColors is each ctx.Highlights entry's own wash colour, resolved
	// ONCE here rather than re-derived per frame: a highlight's own
	// background= when it has one, or the theme's derived tint otherwise --
	// see washColorFor. The uncoloured case is deliberately not a branch a
	// caller has to know about: every highlight, coloured or not, gets an
	// entry.
	washColors []color.NRGBA

	// washBases is the colour-keyed table of static wash bases Run builds
	// eagerly, one full frame per DISTINCT colour among washColors, before
	// its own loop starts -- see Run and washBaseFor. Nil under any style
	// other than HighlightStyleWash, and nil until Run has built it: Render,
	// the simple comparison path, never reads this field at all, and
	// recomputes its own base per call instead (see renderBase) -- see that
	// method's own doc comment for why that is deliberate rather than an
	// oversight.
	washBases map[color.NRGBA]*image.RGBA
}

// New prepares every panel the layout places for this activity.
//
// Whether a panel is placed is decided by its own Accepts, and the decision is
// made BEFORE any box is divided, so a panel that declines contributes nothing
// to its siblings' share and they grow into its space. That is the whole of
// "the layout closes up"; see panel.Layout.Resolve.
//
// One panel can also be removed for a second, unrelated reason:
// --bottom-band distance (ctx.BottomBand == panel.BottomBandDistance) rejects
// ElevationPanel by name, and does so WITHOUT ever calling its Accepts. That
// is deliberate, not an oversight -- Accepts answers "does this activity
// carry the data", and this rejection is answering "did the user ask not to
// show it regardless", which must not be conflated with the first question
// (see Context.BottomBand's own doc comment). Recorded separately too: see
// omitted below and Renderer.Omitted, which the render summary reports under
// its own heading rather than folding into Declined.
//
// A THIRD, independent reason removes MarkerPanel by name: once the elevation
// profile is going to draw the configured highlights and labels as marks on
// its own distance axis (see internal/panel/elevation.go's "the name rows"
// and buildMarks), the standalone strip would be showing the identical
// highlights and labels a second time, in the same frame, on a different
// axis -- not a bug exactly, but a waste of the box MarkerPanel would
// otherwise take and the user's own stated reason for wanting the two
// merged. profileTakesTheBand below is the ONE expression both branches of
// keep consult, so the profile's placement and the strip's suppression can
// never disagree: it cannot be computed twice, once per panel, because a
// second computation is a second chance for it to drift.
//
// This must NOT be expressed as MarkerPanel.Accepts calling
// ElevationPanel{}.Accepts -- see docs/architecture.md's "the cheaper
// alternative, and why it is a trap", which this fails identically. Accepts
// cannot see --bottom-band distance's own removal of ElevationPanel above
// (that happens right here, in keep, never in Accepts), so a
// bottom-band-distance render would have the strip decline on the strength
// of a profile that was never going to be drawn -- the highlights would
// vanish from the frame entirely, exactly the outcome the Alt slot between
// the profile and the distance readout was invented to prevent, one layer
// up. Nor can it be expressed in the layout tree: see docs/architecture.md
// for why a nested Alt whose losing branch is a Col of [Distance, Markers]
// is arithmetically impossible -- the band's own weight would have to be two
// different numbers depending on which case is being rendered, and a slot's
// weight is a static property of the tree.
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

	// profileTakesTheBand is true exactly when ElevationPanel is both going
	// to be drawn (the same Accepts check keep runs for real below, resolved
	// once here rather than twice) AND not removed by --bottom-band distance
	// -- the one expression both branches of keep consult, documented on New
	// itself for why it must be exactly one computation. Costs one extra
	// BuildElevationModel pass over the samples, which ElevationPanel.Accepts'
	// own doc comment already argues is nothing beside rendering a frame.
	profileTakesTheBand := ctx.BottomBand != panel.BottomBandDistance && (panel.ElevationPanel{}).Accepts(ctx)

	var declined, omitted, absorbed []string
	keep := func(p panel.Panel) bool {
		// Checked BEFORE Accepts, and unconditionally -- never mind whether
		// this activity actually carries elevation. The two questions are
		// answered by different things (see Context.BottomBand): asking
		// Accepts first and only overriding a "true" would still be reporting
		// on the activity, and would report an omission as a "decline" for an
		// activity that happens to carry the data. Checking first means every
		// --bottom-band=distance omission is reported the same honest way
		// regardless of what the activity carries: "the flag removed this",
		// not "the activity lacks this" -- the latter is never actually
		// established, so it must never be claimed.
		if ctx.BottomBand == panel.BottomBandDistance && p.Name() == elevationPanelName {
			omitted = append(omitted, p.Name())
			return false
		}
		// Removed for the third reason, MarkerPanel only: the elevation
		// profile is about to draw the same highlights and labels as marks
		// on its own axis (see New's own doc comment on profileTakesTheBand).
		// Guarded on at least one highlight or label actually being
		// configured -- without that guard, an ordinary render with neither
		// would report the strip as "folded into the profile" when nothing
		// was folded anywhere: MarkerPanel.Accepts below would have declined
		// it regardless, and that decline (a fact about the flags) must not
		// be relabelled as an absorption (a fact about where the marks went).
		if p.Name() == markerPanelName && profileTakesTheBand && (len(ctx.Highlights) > 0 || len(ctx.Labels) > 0) {
			absorbed = append(absorbed, p.Name())
			return false
		}
		// --gauges metrics (the zero value's own behaviour too) omits the
		// whole balance display outright, before its own Accepts is ever
		// asked -- panel.IsBalancePanel is the type assertion that finds it,
		// never a name list, so a fifth balance metric costs this line
		// nothing. See panel.Context.Gauges' own doc comment for why this
		// must not be folded into any balance panel's own Accepts instead.
		if ctx.Gauges != panel.GaugesBalance && panel.IsBalancePanel(p) {
			return false
		}
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
		basePx: ctx.BasePx(), placed: placed, declined: declined, omitted: omitted, absorbed: absorbed,
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
		// Optional capability, checked once here rather than in the frame
		// loop: most Painters place no labels of their own and do not
		// implement it. See panel.LabelOverlapReporter's own doc comment.
		if lor, ok := painter.(panel.LabelOverlapReporter); ok {
			r.overlappingLabels = append(r.overlappingLabels, lor.OverlappingLabels()...)
		}
	}

	// Resolved once here, regardless of --highlight-style: cheap (a handful
	// of colour conversions, no image drawn), and resolving it unconditionally
	// is what keeps "a highlight with no background= just uses the theme's own
	// tint" from being a branch anywhere else -- see washColorFor.
	if n := len(ctx.Highlights); n > 0 {
		r.washColors = make([]color.NRGBA, n)
		for i, h := range ctx.Highlights {
			r.washColors[i] = resolveWashColor(h, theme)
		}
	}
	return r, nil
}

// resolveWashColor is a highlight's own background= when it has one, or
// washTheme(theme)'s derived tint otherwise -- the uncoloured case folded
// into the same colour resolution rather than a special value some caller
// has to check for.
func resolveWashColor(h panel.Highlight, theme panel.Theme) color.NRGBA {
	if h.HasBackground {
		return h.Background
	}
	return color.NRGBAModel.Convert(washTheme(theme).Background).(color.NRGBA)
}

// Frames is the number of frames in the render.
func (r *Renderer) Frames() int { return r.ctx.Timeline.Frames() }

// LastFrameWithSample is the last frame index whose state carries a real
// sample, or the final frame when the activity has none anywhere.
//
// It exists because a render's timeline spans the SESSION's declared window
// (see panel.NewTimelineForActivity), which can end after the last record:
// a watch that stopped recording a moment before the session closed leaves
// real elapsed time with nothing recorded in it. Those trailing frames are
// honest -- every gauge shows its placeholder, because Track.AtWithGap
// interpolates between readings and refuses to extrapolate past the last
// one -- but they are useless as the "here is the end state" frame the
// --frames landmarks are for, which is what this answers instead.
//
// Deliberately asks this Renderer for the frames rather than re-deriving
// "does this instant have a sample" from the track: Frame is the single
// place that question is answered for the render, and a second copy here
// could disagree with the pixels the loop actually produced -- which is
// exactly the failure the frame-state invariant exists to prevent.
//
// Walks back rather than computing from the last sample's timestamp,
// because an activity can also end inside an ordinary dropout, and the
// last frame that DRAWS something is the honest answer in both cases.
func (r *Renderer) LastFrameWithSample() int {
	for i := r.Frames() - 1; i >= 0; i-- {
		if r.Frame(i).HasSample {
			return i
		}
	}
	return r.Frames() - 1
}

// Placed returns the panels that will draw, with their boxes.
func (r *Renderer) Placed() []panel.Placed { return r.placed }

// Declined returns the names of panels that had nothing to show, for the
// render summary.
func (r *Renderer) Declined() []string { return r.declined }

// Omitted returns the names of panels a user flag removed before Accepts was
// ever consulted -- distinct from Declined, whose panels genuinely had
// nothing to show. Today this is --bottom-band distance's own ElevationPanel
// rejection, and nothing else; see New's keep filter and
// Context.BottomBand's doc comment for why the two lists must stay separate
// rather than merging into one "left out" report.
func (r *Renderer) Omitted() []string { return r.omitted }

// Absorbed returns the names of panels whose own content is being drawn by a
// DIFFERENT panel instead -- distinct from both Declined (nothing to show)
// and Omitted (a flag removed it before Accepts was asked): an absorbed panel
// had something to show, and it is genuinely on screen, just not in its own
// box. Today this is MarkerPanel alone, once ElevationPanel is drawing the
// same highlights and labels as marks on its own distance axis (see New's own
// doc comment on profileTakesTheBand); nothing else in this project is
// absorbed. The render summary reports this under its own heading, naming
// WHERE the content moved to rather than correcting a false claim of
// decline -- see cmd/render.go's writePanelSummary.
func (r *Renderer) Absorbed() []string { return r.absorbed }

// OverlappingLabels returns the index into ctx.Labels of every label a
// placed panel reports as still too close to a neighbour after its own
// bounded separation nudge -- see panel.LabelOverlapReporter and
// ElevationPanel's D.4 policy in internal/panel/elevation.go. Empty when no
// placed panel implements the interface, or when none of its labels
// collided badly enough to matter.
func (r *Renderer) OverlappingLabels() []int { return r.overlappingLabels }

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
	label, labelWeight := panel.LabelAt(r.ctx.Labels, i, r.ctx.Timeline.FPS(), r.ctx.HighlightTransition)
	cut, cutWeight := r.ctx.Timeline.CutAt(i, r.ctx.HighlightTransition)
	f := panel.Frame{
		Index:   i,
		At:      at,
		Elapsed: r.ctx.Timer.Elapsed(at),
		Active:  r.ctx.Timer.Active(at),
		Paused:  r.ctx.Timer.Paused(at),

		HasTimerEvents: r.ctx.Timer.HasTimerEvents(),

		Interval:       interval,
		IntervalWeight: weight,

		Label:       label,
		LabelWeight: labelWeight,

		Cut:       cut,
		CutWeight: cutWeight,
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

// renderStaticForColor draws the same static content as RenderStatic, with
// Background overridden to col and every other role left at r.theme's own --
// the general form renderStaticWash's own restriction is a special case of.
// Used to build each distinct entry in Run's colour-keyed wash-base table
// (see washBaseFor), and by renderBase to recompute a coloured highlight's
// own base per call rather than reading that table.
func (r *Renderer) renderStaticForColor(img *image.RGBA, col color.NRGBA) error {
	theme := r.theme
	theme.Background = col
	return r.renderStaticThemed(img, theme)
}

// washColorFor is the colour interval's own highlight washes to under
// --highlight-style wash: its background= when it set one, or the theme's
// derived tint otherwise. Resolved once in New (see washColors); this is
// just the accessor.
func (r *Renderer) washColorFor(interval int) color.NRGBA {
	return r.washColors[interval]
}

// washBaseFor is Run's own colour-keyed table lookup: the full static base
// for washColorFor(interval), built eagerly by Run before its loop starts
// (see Run) so the per-frame cost is a copy, never a re-render.
//
// It returns nil when Run has not built the table -- under any style other
// than wash, or before Run runs at all -- which is deliberate: Render, the
// simple comparison path, must never call this. It recomputes its own base
// per call instead (see renderBase), which is what keeps
// TestRenderer_StaticPlusDynamicEqualsRenderExactly_HighlightWash a real
// test of the table rather than a comparison of the table with itself.
func (r *Renderer) washBaseFor(interval int) *image.RGBA {
	return r.washBases[r.washColorFor(interval)]
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
	// Drawn after the border, and after every panel, because unlike the
	// border this one genuinely overlaps panel content -- it has to be on
	// top of what it is announcing.
	r.drawCutNotice(c, f)
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

// The cut notice's own proportions, all fractions of something already
// resolved for this render rather than pixel constants: the text is a
// fraction of the layout's own base size (Renderer.basePx, which is
// Context.BasePx resolved once), and every other measure is a fraction of
// that text size, so the whole notice scales with the frame exactly as a
// panel's contents do.
// cutNoticeWord is what the card calls the stretch it names. The duration
// follows it, so the whole card reads "PAUSED 0:04:32".
//
// It names what HAPPENED in the activity, not what this render did about it.
// "SKIPPED" was the first wording and describes the renderer's own action,
// which is a fact about the flags rather than about the recording -- a
// viewer who never saw the command line has no way to know what was skipped
// or by whom, where "paused" is a thing they did and a duration they can
// recognise. The card only ever appears in a render that cut the pause out,
// so there is no frozen-dashboard case for it to be confused with.
//
// The trailing space is part of the constant because the word and the
// duration are drawn as two runs in two colours and laid out from this one's
// measured width; a separator added at the call site would be a second place
// the spacing lived.
const cutNoticeWord = "PAUSED "

const (
	cutNoticeTextFraction   = 0.55
	cutNoticePadXFraction   = 0.70
	cutNoticePadYFraction   = 0.45
	cutNoticeBorderFraction = 0.06
	cutNoticeGapFraction    = 0.40
)

// drawCutNotice draws the card that names how much activity time was spliced
// out at this seam -- "PAUSED 0:04:32" -- alpha ramping with Frame.CutWeight,
// the same 0->1->0 shape the highlight border and a label's name both use.
//
// This is the honesty half of --pauses skip, and the reason the flag was
// shippable at all. Cutting a pause is the one thing docs/architecture.md
// refused outright, on the grounds that "a video where four minutes silently
// vanish does not say so anywhere". The cut is still a splice and the route
// dot still jumps; what changed is that the frame it jumps on says what
// happened, in the activity's own clock format, for long enough to read.
//
// A render-wide overlay rather than a panel, for the reason the highlight
// border is one: it belongs to no box in the layout tree. Unlike the border
// it does NOT confine itself to the margin -- a duration is text, and the
// margin is a few pixels of a frame's smaller dimension -- so it is drawn
// over whatever panel occupies the top of the frame, opaque, for about a
// second and a half. That is a deliberate cost: a notice a viewer can miss
// is not a notice, and every alternative that avoided the overlap (a
// reserved box, sitting empty for the whole render; a mark on the marker
// strip, which is absorbed into the elevation profile in the common case)
// pays more for less.
//
// Nothing is drawn outside a notice's own frames (Frame.Cut is NoCut, or the
// ramp has not started), which under the default --pauses freeze is every
// frame of the render: a freeze render carries no cuts at all, so this
// returns on its first comparison and costs nothing.
//
// Colours are roles, not decoration (see panel.Theme): the card sits on
// Background so it reads as something laid OVER the dashboard, the word and
// the border are Dim because they are chrome, and the duration itself is
// Foreground because it is a real measurement of real time. Deliberately not
// Absent -- that role means the activity carries no such data, and this
// activity carries it perfectly well; the RENDER is what left it out.
func (r *Renderer) drawCutNotice(c *panel.Canvas, f panel.Frame) {
	cuts := r.ctx.Timeline.Cuts()
	if f.Cut == panel.NoCut || f.Cut >= len(cuts) || f.CutWeight <= 0 {
		return
	}
	px := r.basePx * cutNoticeTextFraction
	if px <= 0 {
		return
	}

	clock := panel.FormatClock(cuts[f.Cut].Removed())
	wordW, textH, err := c.MeasureText(cutNoticeWord, px)
	if err != nil {
		return
	}
	clockW, _, err := c.MeasureText(clock, px)
	if err != nil {
		return
	}

	padX, padY := px*cutNoticePadXFraction, px*cutNoticePadYFraction
	cardW, cardH := wordW+clockW+2*padX, textH+2*padY
	cardX := (float64(r.ctx.Width) - cardW) / 2
	// Just inside the layout's own margin, which is where the frame stops
	// being guaranteed empty -- so the card starts exactly where panel
	// content starts rather than at an offset invented here.
	cardY := r.marginPx + px*cutNoticeGapFraction
	if cardX < 0 || cardY < 0 || cardY+cardH > float64(r.ctx.Height) {
		// Too small a frame to place the notice without running off it.
		// Drawing a clipped card over the dashboard would be worse than
		// drawing none: the render summary reports every cut regardless,
		// so the fact is not lost with the pixels.
		return
	}

	w := f.CutWeight
	c.Rect(panel.Box{X: cardX, Y: cardY, W: cardW, H: cardH}, panel.Fade(c.Theme.Background, w))

	border := px * cutNoticeBorderFraction
	if border < 1 {
		border = 1
	}
	col := panel.Fade(c.Theme.Dim, w)
	c.Rect(panel.Box{X: cardX, Y: cardY, W: cardW, H: border}, col)
	c.Rect(panel.Box{X: cardX, Y: cardY + cardH - border, W: cardW, H: border}, col)
	c.Rect(panel.Box{X: cardX, Y: cardY, W: border, H: cardH}, col)
	c.Rect(panel.Box{X: cardX + cardW - border, Y: cardY, W: border, H: cardH}, col)

	// Left-anchored and laid out from one measured width, so the two runs
	// sit against each other as one line rather than as two independently
	// centred strings that would separate as the duration's own width
	// changed.
	textY := cardY + cardH/2
	_ = c.Text(cutNoticeWord, cardX+padX, textY, 0, 0.5, px, col)
	_ = c.Text(clock, cardX+padX+wordW, textY, 0, 0.5, px, panel.Fade(c.Theme.Foreground, w))
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
// highlight -- that layer blended toward THAT HIGHLIGHT'S OWN colour by
// Frame.IntervalWeight (see washColorFor). This is what Run's own loop does
// with its precomputed, colour-keyed table of bases (see Run and
// washBaseFor), spelled out per call instead of per render so this path and
// that one apply the exact same rule and can be compared frame for frame.
//
// It calls washColorFor -- a cheap lookup, resolved once in New -- but
// deliberately never washBaseFor: recomputing the coloured base on every call
// rather than reading Run's table is the cost of Render staying the "simple
// path" its own name promises, and it is what keeps
// TestRenderer_StaticPlusDynamicEqualsRenderExactly_HighlightWash a real test
// of the table rather than a comparison of the table with itself.
func (r *Renderer) renderBase(img *image.RGBA, f panel.Frame) error {
	if err := r.RenderStatic(img); err != nil {
		return err
	}
	if r.ctx.HighlightStyle != panel.HighlightStyleWash || f.Interval == panel.NoHighlight || f.IntervalWeight <= 0 {
		return nil
	}
	wash := image.NewRGBA(img.Bounds())
	if err := r.renderStaticForColor(wash, r.washColorFor(f.Interval)); err != nil {
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

	// The wash style's colour-keyed table of static bases -- see
	// r.washBases and washBaseFor -- built eagerly here, ONE PER DISTINCT
	// COLOUR among r.washColors, never per frame, and only when the style
	// asks for it: an ordinary border or none render allocates no second
	// buffer and blends nothing, which is the "opt-in" half of shipping
	// wash at all.
	//
	// One base per distinct colour rather than one per highlight: two
	// highlights naming the same colour (including two uncoloured ones, which
	// share washTheme's own derived tint) share one buffer, and the loop
	// below never has to know which highlight is active, only which colour.
	if r.ctx.HighlightStyle == panel.HighlightStyleWash {
		r.washBases = make(map[color.NRGBA]*image.RGBA, len(r.washColors))
		for _, col := range r.washColors {
			if _, ok := r.washBases[col]; ok {
				continue
			}
			wb := image.NewRGBA(image.Rect(0, 0, r.ctx.Width, r.ctx.Height))
			if err := r.renderStaticForColor(wb, col); err != nil {
				return err
			}
			r.washBases[col] = wb
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
		// a highlight, the base is blended toward that highlight's OWN wash
		// base -- r.washBaseFor(f.Interval), the table entry for its own
		// resolved colour -- by IntervalWeight first. See blendBases and
		// renderBase, which apply exactly the same rule so this loop and
		// Render agree frame for frame.
		if r.washBases != nil && f.Interval != panel.NoHighlight {
			blendBases(buf, base, r.washBaseFor(f.Interval), f.IntervalWeight)
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
