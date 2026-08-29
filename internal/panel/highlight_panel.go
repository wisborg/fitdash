package panel

import (
	"image/color"
	"math"
	"time"
)

// HighlightPanel draws a highlight strip: a full-width ribbon of the whole
// render's VIDEO timeline, with each configured --highlight drawn as a block
// at its own position on it, a playhead marking where the render currently
// is, and the active highlight's name.
//
// One responsibility, deliberately narrow: WHERE on the render the
// highlights sit and WHICH one is playing right now. It says nothing about
// what a highlight is about -- that is the rest of the dashboard's job,
// running at the highlight's own pace underneath it. Folding the name into,
// say, ElapsedPanel would make that panel's Accepts answer two unrelated
// questions (see docs/architecture.md and the plan this implements); giving
// the strip its own box is also what lets a render with no --highlight be
// pixel-identical to one from before this feature existed, because Accepts
// below just declines and Layout.Resolve closes up around it exactly as it
// does for any other declining panel.
//
// A GPS or heart-rate dropout during a highlight is IRRELEVANT to this
// panel, and deliberately so: a highlight is a property of the render's
// timeline (an offset pair the user typed), not of what a sensor recorded at
// that offset. This is the one panel in the project that legitimately never
// consults a Sample presence flag -- Dynamic below never reads f.Sample at
// all -- and that is a decision, not an oversight; it is written down here
// because every other panel's absence of such a check would be a bug.
type HighlightPanel struct{}

// Name identifies the panel.
func (HighlightPanel) Name() string { return "highlight" }

// Accepts declines outright when no --highlight was given at all.
//
// Unlike every other panel's Accepts, this is a fact about the FLAGS, not
// about the activity: no FIT file ever carries "highlight" data for this to
// find, so a decline here must not be reported next to "this activity
// carries no such data" the way a missing-power decline is (see
// cmd/render.go's two decline headings, and resolveHighlights). It is also
// the whole absent-data policy this panel has for the "no highlights
// configured at all" case from the plan's table: decline, and let the
// layout close up -- the strongest form of that promise being that the
// render then comes out identical to one with no highlight feature in it at
// all.
func (HighlightPanel) Accepts(ctx *Context) bool {
	return len(ctx.Highlights) > 0
}

// highlightRestAlpha is how visible an inactive highlight's block is against
// the ribbon -- present enough to say "something is marked here" before the
// render ever reaches it, dim enough that the active block visibly brightens
// against it. Not a colour: an opacity fraction Static and Dynamic share, so
// a block's "lit" state is the same colour at full alpha rather than a
// second colour standing in for "active".
const highlightRestAlpha = 0.45

// minBlockFraction is the smallest a highlight's block is allowed to shrink
// to, as a fraction of the ribbon's own height. A highlight that rounds to a
// single frame (see Timeline's own zero-frame trap) would otherwise be a
// sliver less than a pixel wide on a long render -- present in the data,
// invisible in the strip, which is the same class of silent failure as a
// panel that declines without saying so.
const minBlockFraction = 0.5

// blockGapFraction is the hairline of background left between two blocks
// that would otherwise touch or overlap, as a fraction of the ribbon's own
// height rather than a pixel count -- consistent with every other measure
// this panel takes off unit or ribbon.H, so the seam scales with the box
// instead of vanishing at 4K or swallowing a whole block at a small size.
//
// Back-to-back reps -- the plan's own example for why touching endpoints
// (a.To == b.From) are allowed rather than refused as an overlap -- would
// otherwise abut with nothing but a one-pixel antialiasing seam between
// them, which reads as one uniform bar rather than two highlights a viewer
// can count.
const blockGapFraction = 0.2

// Prepare lays out the ribbon, every highlight's block and where its name
// will sit, entirely from ctx.Highlights and ctx.Timeline -- both fixed for
// the whole render, and exactly the two inputs the plan this implements
// names as this panel's static content. Nothing here reads a Frame; the
// facts that DO vary per frame (Frame.Interval, Frame.IntervalWeight,
// Frame.Index) arrive later, in Dynamic, which is what keeps them out of
// this method by construction rather than by discipline.
func (HighlightPanel) Prepare(ctx *Context, box Box) Painter {
	p := &highlightPainter{box: box, frames: ctx.Timeline.Frames()}
	if p.frames <= 0 || len(ctx.Highlights) == 0 || box.W <= 0 || box.H <= 0 {
		// Accepts should have prevented the highlights case, and Resolve
		// already refuses a box too small to place at all -- reaching here
		// means a caller placed this panel without asking. Drawing nothing
		// is the least-wrong option left, as ElevationPanel's own Prepare
		// takes the same position for its analogous defence.
		return p
	}

	unit := math.Min(box.W, box.H)
	inset := unit * 0.02

	p.labelPx = unit * 0.13
	p.namePx = unit * 0.22
	ribbonH := unit * 0.14

	centerY := box.Y + box.H/2
	p.centerX = box.X + box.W/2
	p.labelY = centerY - unit*0.38
	p.nameY = centerY + unit*0.34
	ribbonCenterY := centerY - unit*0.06

	p.ribbon = Box{
		X: box.X + inset,
		Y: ribbonCenterY - ribbonH/2,
		W: box.W - 2*inset,
		H: ribbonH,
	}
	if p.ribbon.W <= 0 {
		return p
	}

	// Wide enough, and extended far enough past the ribbon, that the
	// playhead is legible on more than hue: against a lit block (Highlight
	// at close to full alpha) the accent colour's LUMINANCE is close enough
	// to the highlight colour's own that a thin line reads as barely more
	// than a colour shift -- exactly the frame the viewer most wants the
	// playhead to stand out in. Extending it well past the ribbon on both
	// sides puts most of its length over the panel's plain background
	// instead, where it reads against Theme.Background rather than
	// Theme.Highlight.
	p.headW = math.Max(1, unit*0.02)
	p.headExtend = unit * 0.08

	// The name's size is fit against the LONGEST name across every
	// highlight, never against whichever one is active -- the same template
	// rule clockTemplate follows in elapsed.go. Sizing per-highlight would
	// make the text grow and shrink as the render moved from one highlight
	// to the next, which is exactly the jitter the rule exists to prevent.
	if ctx.Fonts != nil {
		var longest string
		var longestW float64
		for _, h := range ctx.Highlights {
			if h.Name == "" {
				continue
			}
			if w, _, err := ctx.Fonts.Measure(h.Name, p.namePx); err == nil && w > longestW {
				longest, longestW = h.Name, w
			}
		}
		if longest != "" {
			if px, err := ctx.Fonts.FitSize(longest, box.W*0.92, p.namePx); err == nil {
				p.namePx = px
			}
		}
	}

	minBlockW := math.Max(2, p.ribbon.H*minBlockFraction)
	p.names = make([]string, len(ctx.Highlights))
	p.blocks = make([]Box, len(ctx.Highlights))
	p.anchorX = make([]float64, len(ctx.Highlights))
	for i, h := range ctx.Highlights {
		p.names[i] = h.Name

		x0 := p.ribbon.X + frameFraction(ctx.Timeline, h.From)*p.ribbon.W
		x1 := p.ribbon.X + frameFraction(ctx.Timeline, h.To)*p.ribbon.W
		if x1-x0 < minBlockW {
			mid := (x0 + x1) / 2
			x0, x1 = mid-minBlockW/2, mid+minBlockW/2
		}
		p.blocks[i] = Box{X: x0, Y: p.ribbon.Y, W: x1 - x0, H: p.ribbon.H}

		// The name sits centred over its block, clamped so it cannot run
		// past the box it must fit -- a highlight near either end of the
		// ribbon would otherwise print its name half off the panel. This is
		// resolved HERE, once, from a measurement Prepare can already make,
		// rather than re-measured every frame the highlight is active.
		anchor := (x0 + x1) / 2
		if ctx.Fonts != nil && h.Name != "" {
			if w, _, err := ctx.Fonts.Measure(h.Name, p.namePx); err == nil {
				half := w / 2
				lo, hi := box.X+inset+half, box.X+box.W-inset-half
				switch {
				case lo > hi:
					anchor = p.centerX
				case anchor < lo:
					anchor = lo
				case anchor > hi:
					anchor = hi
				}
			}
		}
		p.anchorX[i] = anchor
	}

	// A second pass, after every block's own position and width are settled:
	// pull touching or overlapping neighbours apart by a hairline so two
	// abutting highlights read as two blocks rather than one bar. Highlights
	// are already sorted by From (resolveHighlights' own contract), and so
	// are the blocks derived from them, so only ADJACENT pairs can possibly
	// touch -- a single left-to-right sweep is enough.
	gap := p.ribbon.H * blockGapFraction
	for i := 1; i < len(p.blocks); i++ {
		short := gap - (p.blocks[i].X - (p.blocks[i-1].X + p.blocks[i-1].W))
		if short <= 0 {
			continue
		}
		p.blocks[i-1].W -= short / 2
		p.blocks[i].X += short / 2
		p.blocks[i].W -= short / 2
	}
	return p
}

// frameFraction locates offset -- an activity-time offset from the
// timeline's own start, exactly what Highlight.From and .To are measured in
// -- as a fraction of tl's own frame count.
//
// This is deliberately the SAME axis Frame.Index moves along in Dynamic
// (frame i sits at fraction i/Frames()), which is what lets a highlight's
// static block and the per-frame playhead share one ruler without computing
// it twice. IndexAt is Timeline's own activity-time-to-frame lookup, so nothing
// here re-derives the segment arithmetic Timeline already owns.
func frameFraction(tl Timeline, offset time.Duration) float64 {
	frames := tl.Frames()
	if frames <= 0 {
		return 0
	}
	frac := float64(tl.IndexAt(offset)) / float64(frames)
	if frac < 0 {
		return 0
	}
	if frac > 1 {
		return 1
	}
	return frac
}

type highlightPainter struct {
	box    Box
	frames int

	ribbon     Box
	centerX    float64
	labelY     float64
	nameY      float64
	labelPx    float64
	namePx     float64
	headW      float64
	headExtend float64

	// names, blocks and anchorX are parallel to Context.Highlights and
	// indexed by Frame.Interval -- the same index the render loop hands
	// Dynamic, so a lookup here can never disagree with which highlight the
	// loop says is active.
	names   []string
	blocks  []Box
	anchorX []float64
}

// Static draws the ribbon and every highlight's block, all invariant across
// the render: the "HIGHLIGHTS" chrome label, the track the blocks sit on,
// and the blocks themselves, dimmed to highlightRestAlpha until Dynamic
// brightens whichever one is playing.
func (p *highlightPainter) Static(c *Canvas) {
	if len(p.blocks) == 0 {
		return
	}
	_ = c.Text("HIGHLIGHTS", p.centerX, p.labelY, 0.5, 0.5, p.labelPx, c.Theme.Dim)
	c.Rect(p.ribbon, c.Theme.Dim)
	for _, b := range p.blocks {
		c.Rect(b, Fade(c.Theme.Highlight, highlightRestAlpha))
	}
}

// Dynamic draws the playhead and, when a highlight is active, brightens its
// block and fades in its name.
//
// "Highlights configured, none active this frame" is answered by drawing
// only the playhead here: the ribbon and every block already drew in
// Static, so the box is never empty even on a frame with nothing playing --
// per the plan's absent-data table, that is NOT a hole, it is a timeline
// with nothing lit right now. A highlight with no name is answered by
// skipping the text call entirely: the block lighting up already says a
// highlight is active, and a "--" here would claim the program failed to
// find a name the user simply chose not to give.
func (p *highlightPainter) Dynamic(c *Canvas, f Frame) {
	if len(p.blocks) == 0 {
		return
	}

	if f.Interval != NoHighlight && f.Interval >= 0 && f.Interval < len(p.blocks) {
		weight := clampWeight(f.IntervalWeight)
		alpha := highlightRestAlpha + (1-highlightRestAlpha)*weight
		c.Rect(p.blocks[f.Interval], Fade(c.Theme.Highlight, alpha))

		if name := p.names[f.Interval]; name != "" {
			_ = c.Text(name, p.anchorX[f.Interval], p.nameY, 0.5, 0.5, p.namePx, Fade(c.Theme.Foreground, weight))
		}
	}

	frac := float64(f.Index) / float64(p.frames)
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	x := p.ribbon.X + frac*p.ribbon.W
	c.Rect(Box{
		X: x - p.headW/2, Y: p.ribbon.Y - p.headExtend,
		W: p.headW, H: p.ribbon.H + 2*p.headExtend,
	}, c.Theme.Accent)
}

// clampWeight bounds Frame.IntervalWeight to [0,1] before it is used as an
// alpha factor. The render loop's own Timeline.IntervalAt already promises
// this range; the clamp here is this panel refusing to let an out-of-range
// weight -- were the promise ever violated upstream -- invert an alpha
// channel into something a Canvas is not asked to make sense of.
func clampWeight(w float64) float64 {
	if w < 0 {
		return 0
	}
	if w > 1 {
		return 1
	}
	return w
}

// Fade scales col's alpha by weight, leaving its RGB unchanged. It is how
// this panel animates the entrance/exit ramp without ever touching the
// background, and internal/render's own highlight-border overlay uses it
// for the identical reason -- one generic colour primitive with no
// feature-specific content, exported here rather than kept as two copies
// that would need to agree by inspection instead of by construction.
//
// Scaling alpha rather than blending RGB toward some other colour is what
// keeps this panel out of the trap docs/architecture.md names for the whole
// project: a Painter that painted a background-coloured rectangle in
// Dynamic would paint over whatever Static drew beneath it, and a per-frame
// Theme would make the static layer disagree with the dynamic one. Scaling
// alpha instead lets the Canvas's own compositing do the blending, so what
// shows through a faded block or name is whatever THIS panel already drew
// in Static underneath it -- never a colour chosen here standing in for the
// frame's real background.
func Fade(col color.Color, weight float64) color.Color {
	weight = clampWeight(weight)
	n := color.NRGBAModel.Convert(col).(color.NRGBA)
	n.A = uint8(math.Round(float64(n.A) * weight))
	return n
}
