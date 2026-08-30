package panel

import (
	"image/color"
	"math"
	"time"
)

// MarkerPanel draws a marker strip: a full-width ribbon of the whole
// render's VIDEO timeline, with each configured --highlight drawn as a
// block at its own position on it, each configured --label drawn as a tick,
// a playhead marking where the render currently is, and the name of
// whichever highlight and/or label is active right now.
//
// One responsibility, deliberately narrow: WHERE on the render the
// highlights and labels sit and WHICH ones are playing right now. It says
// nothing about what a highlight or a label is about -- that is the rest of
// the dashboard's job, running at its own pace underneath it. Folding the
// name into, say, ElapsedPanel would make that panel's Accepts answer
// unrelated questions (see docs/architecture.md and the plan this
// implements); giving the strip its own box is also what lets a render with
// no --highlight and no --label be pixel-identical to one from before this
// feature existed, because Accepts below just declines and Layout.Resolve
// closes up around it exactly as it does for any other declining panel.
//
// This was HighlightPanel until --label landed. The rename is required, not
// tidiness: a labels-only render would otherwise print a panel named
// "highlight" in the summary when no highlight exists at all, which is a
// small lie this project does not ship. It ripples to Name() below (now
// "markers"), to both Layout's placements, to the decline heading in
// cmd/render.go, and to this file's own tests.
//
// A GPS or heart-rate dropout during a highlight, or at a label's instant,
// is IRRELEVANT to this panel, and deliberately so: both are properties of
// the render's timeline (an offset the user typed), not of what a sensor
// recorded at that offset. This is the one panel in the project that
// legitimately never consults a Sample presence flag -- Dynamic below never
// reads f.Sample at all -- and that is a decision, not an oversight; it is
// written down here because every other panel's absence of such a check
// would be a bug.
type MarkerPanel struct{}

// Name identifies the panel.
func (MarkerPanel) Name() string { return "markers" }

// Accepts answers exactly one question: is there anything on this render's
// timeline to show at all, meaning at least one --highlight or at least one
// --label. It stays one question about the flags, not two -- an activity
// never carries "highlight" or "label" data for this to find, so a decline
// here is a fact about the FLAGS and must not be reported next to "this
// activity carries no such data" the way a missing-power decline is (see
// cmd/render.go's two decline headings, resolveHighlights and
// resolveLabels). Declining with neither configured, and letting the layout
// close up, is the whole absent-data policy this panel has for that case --
// the strongest form of that promise being that the render then comes out
// identical to one with no highlight or label feature in it at all.
func (MarkerPanel) Accepts(ctx *Context) bool {
	return len(ctx.Highlights) > 0 || len(ctx.Labels) > 0
}

// highlightRestAlpha is how visible an inactive highlight's block is against
// the ribbon -- present enough to say "something is marked here" before the
// render ever reaches it, dim enough that the active block visibly brightens
// against it. Not a colour: an opacity fraction Static and Dynamic share, so
// a block's "lit" state is the same colour at full alpha rather than a
// second colour standing in for "active".
const highlightRestAlpha = 0.45

// restAlpha scales highlightRestAlpha up toward full strength by weight,
// the ramp both this panel's blocks (on Frame.IntervalWeight) and
// RoutePanel's own route marks (on the identical weight) use to brighten an
// active highlight. One function rather than the same two-term expression
// written out in both files: the two callers must agree on how
// "brightening" itself is computed, not merely share the constant it starts
// from, or a future change to the ramp's shape could land in one file and
// not the other with nothing to say so.
func restAlpha(weight float64) float64 {
	return highlightRestAlpha + (1-highlightRestAlpha)*clampWeight(weight)
}

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

// Prepare lays out the ribbon, every highlight's block, every label's tick,
// and where the highlight name and the label name will each sit, entirely
// from ctx.Highlights, ctx.Labels and ctx.Timeline -- all fixed for the
// whole render, and exactly the inputs the plan this implements names as
// this panel's static content. Nothing here reads a Frame; the facts that
// DO vary per frame (Frame.Interval, Frame.IntervalWeight, Frame.Label,
// Frame.LabelWeight, Frame.Index) arrive later, in Dynamic, which is what
// keeps them out of this method by construction rather than by discipline.
func (MarkerPanel) Prepare(ctx *Context, box Box) Painter {
	p := &markerPainter{box: box, frames: ctx.Timeline.Frames()}
	if p.frames <= 0 || (len(ctx.Highlights) == 0 && len(ctx.Labels) == 0) || box.W <= 0 || box.H <= 0 {
		// Accepts should have prevented the neither-configured case, and
		// Resolve already refuses a box too small to place at all --
		// reaching here means a caller placed this panel without asking.
		// Drawing nothing is the least-wrong option left, as
		// ElevationPanel's own Prepare takes the same position for its
		// analogous defence.
		return p
	}

	unit := math.Min(box.W, box.H)
	inset := unit * 0.02

	p.labelPx = unit * 0.13
	p.namePx = unit * 0.22
	p.labelNamePx = unit * 0.16
	ribbonH := unit * 0.14

	centerY := box.Y + box.H/2
	p.centerX = box.X + box.W/2
	p.labelY = centerY - unit*0.38
	p.nameY = centerY + unit*0.34
	// The label name sits BETWEEN the ribbon and the highlight name's own
	// row, not on top of either -- "a second anchor within the same box,
	// alongside the highlight name" from the plan, deliberately never
	// taking over the highlight name's row even while both are on screen
	// at once, so the highlight name never pops out and back mid-highlight
	// as a label passes over it (see this method's own reasoning below).
	// 0.15 leaves clear daylight on both sides at every box shape this
	// panel is tested against (see highlight_panel_test.go's FitsEveryBox
	// and label tests), and the clearance below the ribbon is measured
	// against the PLAYHEAD rather than the ribbon: the playhead extends
	// headExtendBelow past the ribbon's own bottom edge, so the ribbon's
	// edge at 0.01 is not what this row has to clear. This row's own top
	// sits at 0.15 less half its labelNamePx, which is 0.07, and
	// headExtendBelow is sized against that -- see its own comment. Above,
	// the highlight name row's top edge is around 0.23 (nameY 0.34 less
	// roughly half its own namePx).
	//
	// A label name long enough for FitSize to shrink labelNamePx only
	// moves this row's top edge DOWN, away from the playhead, so the
	// clearance below is a floor rather than a figure that can erode.
	p.labelNameY = centerY + unit*0.15
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
	p.ok = true

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

	// The playhead is ASYMMETRIC, and that is the label name row's doing
	// rather than an aesthetic choice. Above the ribbon there is nothing
	// between it and the chrome label, so it takes the full headExtend.
	// Below, the label name row is in the way: a symmetric 0.08 would put
	// the playhead's lower tip well inside the label's own glyphs, and
	// because the playhead sweeps the whole ribbon that is not a near miss
	// at one position -- every label would eventually have an accent line
	// drawn through its name.
	//
	// The room available is measured against the label row's REAL glyph
	// extent, not against labelNamePx. A face's ascender-to-descender box
	// runs about 1.16 to 1.25 times the nominal size (it varies with the
	// size, through hinting), so the row's true top edge sits appreciably
	// higher than labelNameY less half labelNamePx would suggest. Measured
	// across the box shapes highlight_panel_test.go covers, the gap between
	// the ribbon's bottom edge and that true top edge is 0.040 to 0.047 of
	// unit -- so 0.05 was inside the glyphs at every one of them, and the
	// arithmetic that chose it was wrong precisely because it reasoned
	// about the nominal size instead of the drawn one.
	//
	// 0.03 leaves at least 0.01 of clear background at the tightest shape.
	// Both are fractions of the same unit, so the clearance holds at every
	// resolution rather than only the ones a test happened to try -- and
	// TestMarkerPanel_PlayheadClearsTheLabelNameRowAcrossEveryBoxShape
	// asserts it against the measured extent, so raising this number back
	// toward headExtend fails rather than quietly reintroducing the bug.
	p.headExtendBelow = unit * 0.03

	// A label's tick is deliberately thinner than the playhead and extends
	// ABOVE the ribbon only, never through or below it -- differing from
	// the playhead in shape as well as colour (Theme.Foreground, not
	// Theme.Accent) so the two are never mistaken for each other even in a
	// frame with no colour to go by.
	p.tickW = math.Max(1, unit*0.012)
	p.tickExtend = unit * 0.05

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

	// Same template rule, applied to the label row: sized once against the
	// longest label name across every --label, never against whichever one
	// is active.
	if ctx.Fonts != nil {
		var longest string
		var longestW float64
		for _, l := range ctx.Labels {
			if w, _, err := ctx.Fonts.Measure(l.Name, p.labelNamePx); err == nil && w > longestW {
				longest, longestW = l.Name, w
			}
		}
		if longest != "" {
			if px, err := ctx.Fonts.FitSize(longest, box.W*0.92, p.labelNamePx); err == nil {
				p.labelNamePx = px
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

	// Labels have no span of their own to occupy on the ribbon -- an
	// instant, not a range -- so each gets a tick at its own resolved
	// FirstFrame rather than a block. FirstFrame is read back rather than
	// re-derived from Label.At through frameFraction: a Label's frame bounds
	// are already resolved against this same Timeline by cmd/label.go's
	// resolveLabels, and re-deriving them here would be a second computation
	// free to land a frame away from the one Frame.Label/LabelWeight are
	// built from in the render loop -- exactly the two-layers-disagreed
	// failure this architecture exists to prevent.
	p.labelNames = make([]string, len(ctx.Labels))
	p.tickX = make([]float64, len(ctx.Labels))
	p.labelAnchorX = make([]float64, len(ctx.Labels))
	for i, l := range ctx.Labels {
		p.labelNames[i] = l.Name
		p.tickX[i] = p.ribbon.X + clampFraction(float64(l.FirstFrame)/float64(p.frames))*p.ribbon.W

		// Anchored over the label's OWN tick, exactly the way a highlight's
		// name is anchored over the midpoint of its own block above -- not
		// centred in the panel, which is invisible with one label but with
		// several leaves every name with no visible relationship to any
		// tick at all. Clamped the same way, so a label near either end of
		// the ribbon stays inside the box instead of printing half off it.
		anchor := p.tickX[i]
		if ctx.Fonts != nil {
			if w, _, err := ctx.Fonts.Measure(l.Name, p.labelNamePx); err == nil {
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
		p.labelAnchorX[i] = anchor
	}

	return p
}

// clampFraction bounds a raw fraction to [0,1]. Shared by frameFraction (a
// highlight's activity-time offset converted to a frame index) and the tick
// position derived directly from a Label's own resolved FirstFrame, so a
// highlight's block and a label's tick sit on the same axis by construction
// rather than by two separately-clamped computations that merely happen to
// agree.
func clampFraction(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
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
	return clampFraction(float64(tl.IndexAt(offset)) / float64(frames))
}

type markerPainter struct {
	box    Box
	frames int

	// ok is false when Prepare bailed out early (no highlight or label
	// configured despite Accepts, or a box too degenerate to draw into) --
	// see Prepare's own early returns. Both Static and Dynamic check it
	// first, rather than inferring "nothing to draw" from len(p.blocks)
	// alone, because a labels-only render has no blocks at all yet still
	// has real content (the ribbon and every tick) that must draw.
	ok bool

	ribbon      Box
	centerX     float64
	labelY      float64
	nameY       float64
	labelNameY  float64
	labelPx     float64
	namePx      float64
	labelNamePx float64
	headW       float64
	headExtend  float64
	// headExtendBelow is the playhead's DOWNWARD reach, deliberately
	// shorter than headExtend so it stops clear of the label name row.
	// See Prepare for why the two differ.
	headExtendBelow float64
	tickW           float64
	tickExtend      float64

	// names, blocks and anchorX are parallel to Context.Highlights and
	// indexed by Frame.Interval -- the same index the render loop hands
	// Dynamic, so a lookup here can never disagree with which highlight the
	// loop says is active.
	names   []string
	blocks  []Box
	anchorX []float64

	// labelNames, tickX and labelAnchorX are parallel to Context.Labels and
	// indexed by Frame.Label, for the identical reason.
	labelNames   []string
	tickX        []float64
	labelAnchorX []float64
}

// Static draws the ribbon, every highlight's block and every label's tick,
// all invariant across the render: the "MARKERS" chrome label, the track the
// blocks and ticks sit on, the blocks themselves dimmed to
// highlightRestAlpha until Dynamic brightens whichever one is playing, and
// the ticks at full strength since a label has no "rest" state of its own to
// dim from -- it is either on screen (Dynamic draws its name) or it is not,
// but the tick marking WHERE it falls belongs on the ribbon permanently, the
// same way a highlight's block does.
//
// This is also what makes a labels-only render (no --highlight at all)
// honest rather than a hole: MarkerPanel's own Accepts above already treats
// "at least one --label" as reason enough to be placed, and the ticks drawn
// here are the invariant content that makes the box real ink on every frame
// of such a render, not bare background waiting for a highlight that never
// comes.
func (p *markerPainter) Static(c *Canvas) {
	if !p.ok {
		return
	}
	_ = c.Text("MARKERS", p.centerX, p.labelY, 0.5, 0.5, p.labelPx, c.Theme.Dim)
	c.Rect(p.ribbon, c.Theme.Dim)
	for _, b := range p.blocks {
		c.Rect(b, Fade(c.Theme.Highlight, highlightRestAlpha))
	}
	for _, x := range p.tickX {
		c.Rect(Box{
			X: x - p.tickW/2, Y: p.ribbon.Y - p.tickExtend,
			W: p.tickW, H: p.tickExtend,
		}, c.Theme.Foreground)
	}
}

// Dynamic draws the playhead and, when a highlight and/or a label is active,
// brightens the highlight's block and fades in each one's name in its own
// area.
//
// "Configured, none active this frame" is answered by drawing only the
// playhead here: the ribbon, every block and every tick already drew in
// Static, so the box is never empty even on a frame with nothing playing --
// per the plan's absent-data table, that is NOT a hole, it is a timeline
// with nothing lit right now. A highlight with no name is answered by
// skipping the text call entirely: the block lighting up already says a
// highlight is active, and a "--" here would claim the program failed to
// find a name the user simply chose not to give. A Label, unlike a
// Highlight, can never reach here with an empty name -- resolveLabels
// refuses one outright, per Label's own doc comment -- so no equivalent
// skip is needed for it.
//
// The highlight name and the label name draw in their OWN areas
// (p.nameY and p.labelNameY) rather than sharing one: a label overlapping
// an active highlight shows both names at once, neither one moving or
// popping out to make room for the other. The rejected alternative --
// letting a label take over the highlight's area while it is on screen --
// would make the highlight's name visibly vanish and reappear as a label
// passes over it, which reads as a glitch rather than a feature; two lines
// cost a little vertical room and no rule a viewer has to infer.
func (p *markerPainter) Dynamic(c *Canvas, f Frame) {
	if !p.ok {
		return
	}

	if f.Interval != NoHighlight && f.Interval >= 0 && f.Interval < len(p.blocks) {
		weight := clampWeight(f.IntervalWeight)
		alpha := restAlpha(weight)
		c.Rect(p.blocks[f.Interval], Fade(c.Theme.Highlight, alpha))

		if name := p.names[f.Interval]; name != "" {
			_ = c.Text(name, p.anchorX[f.Interval], p.nameY, 0.5, 0.5, p.namePx, Fade(c.Theme.Foreground, weight))
		}
	}

	if f.Label != NoLabel && f.Label >= 0 && f.Label < len(p.labelNames) {
		weight := clampWeight(f.LabelWeight)
		_ = c.Text(p.labelNames[f.Label], p.labelAnchorX[f.Label], p.labelNameY, 0.5, 0.5, p.labelNamePx, Fade(c.Theme.Foreground, weight))
	}

	frac := clampFraction(float64(f.Index) / float64(p.frames))
	x := p.ribbon.X + frac*p.ribbon.W
	c.Rect(Box{
		X: x - p.headW/2, Y: p.ribbon.Y - p.headExtend,
		W: p.headW, H: p.ribbon.H + p.headExtend + p.headExtendBelow,
	}, c.Theme.Accent)
}

// clampWeight bounds Frame.IntervalWeight or Frame.LabelWeight to [0,1]
// before it is used as an alpha factor. The render loop's own
// Timeline.IntervalAt and LabelAt already promise this range; the clamp
// here is this panel refusing to let an out-of-range weight -- were the
// promise ever violated upstream -- invert an alpha channel into something a
// Canvas is not asked to make sense of.
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
