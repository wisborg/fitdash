package panel

import (
	"fmt"
	"strings"
)

// LayoutAuto names the arrangement chosen from the frame's own shape.
const LayoutAuto = "auto"

// --bottom-band's legal values -- which of the bottom strip's two Alt
// candidates (below) the user gets, rather than which the activity happens
// to carry. Exported so the CLI's own validation and Context.BottomBand
// compare against one pair of strings rather than each defining its own, the
// same reason --highlight-style's values live beside Highlight rather than in
// cmd.
const (
	// BottomBandProfile is the default: the elevation profile, filled to the
	// playhead, exactly as before this flag existed. An activity that carries
	// no elevation still falls back to the distance readout under this value
	// -- ElevationPanel's own Accepts declines and the Alt slot falls through
	// -- so this flag adds a second way to reach that fallback rather than
	// replacing it.
	BottomBandProfile = "profile"

	// BottomBandDistance omits the profile outright, so the readout takes the
	// band even on an activity that DOES carry elevation. See New's keep
	// filter in internal/render, which is where this is actually enforced:
	// rejecting ElevationPanel by name before its own Accepts is ever asked,
	// exactly the one-line keep-filter rejection this Alt slot's own doc
	// comment (and docs/architecture.md's "one band, two candidates") was
	// built to make possible.
	BottomBandDistance = "distance"
)

// Layouts are the arrangements --layout can choose, besides auto.
func Layouts() []Layout { return []Layout{LandscapeLayout(), PortraitLayout()} }

// SelectLayout picks an arrangement by name, or from the frame's shape when
// the name is LayoutAuto.
//
// Auto remains the default and is right almost always: a landscape tree
// squeezed into a portrait frame gives every panel an absurd aspect ratio.
// Naming one explicitly is for the cases automation cannot know about -- a
// square frame, or a wide render meant to sit beside something else -- and
// forcing a mismatch is the user's to make, not this function's to prevent.
//
// An unknown name is refused rather than falling back, for the same reason
// --theme refuses one: a typo should not cost a whole render.
func SelectLayout(name string, w, h int) (Layout, error) {
	if name == LayoutAuto || name == "" {
		if h > w {
			return PortraitLayout(), nil
		}
		return LandscapeLayout(), nil
	}
	for _, l := range Layouts() {
		if l.Name == name {
			return l, nil
		}
	}
	names := []string{LayoutAuto}
	for _, l := range Layouts() {
		names = append(names, l.Name)
	}
	return Layout{}, fmt.Errorf("panel: unknown layout %q; use %s", name, strings.Join(names, ", "))
}

// LandscapeLayout is the wide arrangement: the clock on the left at twice the
// width, the readouts stacked beside it.
//
// Layouts are built by functions so they always carry a Name. A Layout
// assembled as a struct literal elsewhere would have an empty one, and a
// diagnostic naming a layout while the pixels show another is a lie that is
// hard to catch.
func LandscapeLayout() Layout {
	return Layout{
		Name:      "landscape",
		Margin:    0.03,
		FontScale: 0.05,
		// This band shows the elevation profile, or the distance readout in
		// its place if there is no profile -- an Alt slot (layout.go), not a
		// Row: since the fill under the profile became this project's own
		// distance indicator (see elevation.go), the profile and the readout
		// never draw in the same frame, so there is nothing left for a Row's
		// weights to divide between them. See layout.go's Alt doc comment for
		// the mechanism and the one gap it leaves in the decline summary, and
		// docs/architecture.md's "one band, two candidates" section for the
		// full design.
		//
		// This is still a layout-level grouping, not a composite panel: an Alt
		// slot holding the two existing panels unmodified. A panel that drew
		// both would be the first in the project to sub-divide its own box,
		// and it would report one Name() in the summary where another panel's
		// data is actually shown -- hiding a declined profile behind a panel
		// that claims it drew.
		//
		// Alt over a shared predicate -- a strip-variant of Distance whose own
		// Accepts also calls ElevationPanel{}.Accepts -- is deliberate, not
		// merely the cheaper option skipped: Accepts is a pure function of
		// Context and cannot see Resolve's own keep filter, so a
		// --bottom-band=distance flag built that way would remove the profile
		// by keep AND leave that variant's own Accepts declining, pruning the
		// whole band and deleting distance from the render -- exactly the
		// outcome this change exists to prevent. Under Alt, keep(elevation)
		// returning false simply falls through to the next candidate, which
		// does the right thing for free -- and is exactly what --bottom-band
		// (see New's keep filter in internal/render) actually does.
		//
		// Each candidate keeps its own ordinary leaf Pad. There is no more
		// "daylight between two halves" to defend here -- the two never share
		// a frame for a pad to separate them within.
		Root: Slot{Dir: Col, Children: []Slot{
			{Dir: Row, Weight: 4, Children: []Slot{
				{Dir: Col, Weight: 3, Children: []Slot{
					{Panel: RoutePanel{}, Weight: 3, Pad: 0.01},
					// Elapsed time and distance are the pair on this
					// dashboard that only ever increase across the
					// activity -- heart rate, pace, power and cadence all
					// fluctuate -- which is why distance sits here beside
					// the clock rather than in the gauge column to the
					// right, splitting the box the way this row used to
					// before the readout moved into the bottom band (see
					// bb11c73).
					//
					// The 2:1 weight is not the 3:2 split that row once
					// used, and the change is not cosmetic: measured with
					// the two Prepare methods actually called (synthetic
					// boxes, no real activity involved), at 1920x1080 and
					// at 3840x2160 the clock's own fitted digit size is
					// IDENTICAL under both ratios -- it is bounded by the
					// box's HEIGHT there, not its width, so giving it more
					// width buys it nothing -- while distance's fitted
					// value shrinks about 15% under 2:1 versus 3:2 (from
					// matching the clock's own size at 3:2 down to roughly
					// seven-eighths of it). Landscape and 4K therefore
					// favour 3:2 on this measure alone. Portrait inverts
					// it: there the ROW's total width is scarce enough
					// that the clock template itself is width-bound under
					// both ratios, and 3:2's wider distance share starves
					// it -- the clock's own fitted size drops by roughly a
					// tenth going from 2:1 to 3:2, while distance's own
					// value, already the smaller number on a portrait
					// frame, loses proportionally more of its share than
					// it gains height. 2:1 is the ratio that keeps the
					// CLOCK -- the panel this pairing exists to keep
					// beside, and unconditionally placed where distance is
					// not -- from shrinking in the tree where space is
					// tightest, at a real but smaller cost to distance's
					// own digit size in the tree where space is not.
					// Restating the old ratio from memory would have been
					// a guess dressed as a derivation; retuning either
					// number again means re-running this same measurement,
					// not eyeballing a render.
					{Dir: Row, Weight: 2, Children: []Slot{
						{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
						{Panel: Distance(), Weight: 1, Pad: 0.01},
					}},
				}},
				{Dir: Col, Weight: 1, Children: []Slot{
					{Panel: HeartRate(), Pad: 0.01},
					{Panel: Pace(), Pad: 0.01},
					{Panel: Power(), Pad: 0.01},
					{Panel: Cadence(), Pad: 0.01},
				}},
			}},
			// climb and gradient share a strip directly above the elevation
			// band, its own full-width row rather than folded into the Alt
			// slot below -- see docs/architecture.md's "the placement
			// rejected" for the two reasons a Row{Profile, Climb, Gradient}
			// inside the band was rejected instead (--bottom-band would
			// have to reject three more names by hand, and the Alt slot's
			// first-survivor rule would delete distance from the render
			// outright if the profile ever declined while these accepted).
			//
			// Weight 1 of this tree's total of 8 (Row 4, Strip 1, Alt 2,
			// Marker 1) -- an EIGHTH, chosen as a fraction of the tree
			// rather than measured against the four gauges specifically,
			// which is the mistake this comment's own neighbour below
			// warns about: the moment a user drops a gauge, "measured
			// against four gauges" is stale where "an eighth of the
			// tree" is not. See PortraitLayout's own Strip weight for why
			// it is 2, not 1, there: the SAME fraction, not the same
			// number, because portrait's tree sums to a larger total.
			//
			// Inside the strip, climb on the left and gradient on the
			// right -- the frame's existing grammar (see the elapsed/distance
			// pairing above, and its own comment): metrics that only ever
			// increase sit left (clock, distance, and now gain/loss), metrics
			// that fluctuate sit right (the gauge column, and now the current
			// grade).
			//
			// An even split, and it is even because ClimbPanel's content now
			// FILLS its box rather than occupying a fixed fraction of it.
			//
			// That was not always true, and the history is worth keeping because
			// two plausible weightings were tried and both failed. While the
			// track was a fixed fraction of the box, this panel always left the
			// remainder empty at its own right edge -- so a BIGGER box grew that
			// emptiness into a gulf between the two groups, and a SMALLER box
			// shortened the gain/loss bars instead. The bar's length is the whole
			// reading the panel exists for (see its "shared scale" section), so
			// neither direction was acceptable, and no weight could have been:
			// the slack was inside the panel, not in the split.
			//
			// With the track taking whatever width remains (see ClimbPanel's own
			// trackW comment), the panel has no structural slack left, so the two
			// groups sit adjacent at ANY weights and this number goes back to
			// meaning only what it should: how much width the bars get relative
			// to the gradient's line and reading. Even reads well at 1080p and
			// 4K -- a gate call, not a derivation.
			//
			// GradientPanel's line and reading are sized off unit
			// (min(box.W, box.H), see gradient.go's "Scaling" section), so they
			// are bounded by the STRIP'S OWN HEIGHT rather than by however much
			// width the row hands them. Width beyond what that template needs is
			// daylight inside gradient's own box, and because gradient anchors
			// its content at its own box's left edge that daylight lands at the
			// row's outer right edge rather than between the groups.
			//
			// The alternative -- pushing either panel's content against its own
			// box edge to sit nearer its neighbour -- was rejected outright: it
			// would depend on the NEIGHBOUR's box being where it happens to sit
			// today, which is a layout fact no panel can see, so it would break
			// silently the next time these weights moved.
			{Dir: Row, Weight: 1, Children: []Slot{
				{Panel: ClimbPanel{}, Weight: 1, Pad: 0.01},
				{Panel: GradientPanel{}, Weight: 1, Pad: 0.01},
			}},
			// Weight 2, not the 1 this slot carried when the profile and the
			// standalone marker strip below were still two separate rows.
			// Once ElevationPanel started drawing the configured highlights
			// and labels itself (see elevation.go's "the name rows" and
			// buildMarks), MarkerPanel's row is pruned in EVERY case the
			// profile is placed at all -- see the comment on MarkerPanel's
			// own row below -- so this band is the only row left doing that
			// row's job, and it takes that row's weight rather than leaving
			// it to fall upward to the panels above. Measured at 1920x1080:
			// giving the row's weight to the band alone was not enough on
			// its own to keep the profile readable once it was also
			// carrying a highlight's and a label's own name (see
			// elevNamePx/elevLabelNamePx in elevation.go for the other half
			// of that fix); this weight is what recovers the "no marks"
			// case back to roughly its pre-feature height, and the name
			// rows' own shrink is what recovers the marked case.
			{Dir: Alt, Weight: 2, Children: []Slot{
				{Panel: ElevationPanel{}, Pad: 0.01},
				{Panel: Distance(), Pad: 0.01},
			}},
			// MarkerPanel declines outright with neither --highlight nor
			// --label configured (see its own Accepts), and Resolve prunes a
			// declining leaf BEFORE dividing space among its siblings -- so
			// this row costs an ordinary render nothing: the rows above it
			// get exactly the boxes they would have gotten had this line
			// never been added. See highlight_panel_test.go's pixel-identical
			// test.
			//
			// It is ALSO pruned -- by internal/render's own keep filter,
			// never by this Accepts -- whenever the band above is showing
			// the profile and at least one highlight or label IS configured:
			// the marks are drawn on the profile's own axis instead (see
			// elevation.go), and this row would otherwise show the identical
			// marks a second time. That is the ordinary case this row's
			// weight was folded into the band's own weight above for. It
			// still has to exist, at its own weight, for the one case it is
			// NOT pruned: --bottom-band distance, which omits the profile
			// and restores this row as the standalone strip -- see
			// docs/architecture.md's "one band, two candidates" and
			// BottomBandDistance's own doc comment.
			{Panel: MarkerPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}

// PortraitLayout is the tall arrangement: everything in one column, the clock
// given the most room.
//
// A different TREE rather than the landscape one rescaled, because the
// available width collapses in portrait and a row of three panels there would
// give each a sliver too narrow to read.
func PortraitLayout() Layout {
	return Layout{
		Name:      "portrait",
		Margin:    0.03,
		FontScale: 0.05,
		Root: Slot{Dir: Col, Children: []Slot{
			{Panel: RoutePanel{}, Weight: 4, Pad: 0.01},
			// See LandscapeLayout's own comment beside this same pairing:
			// elapsed time and distance are the two metrics that only ever
			// increase, which is why distance sits beside the clock here
			// too rather than in one of the gauge rows below, at the same
			// 2:1 weight, measured the same way -- and it is THIS tree
			// where 2:1 earns its keep: with the row's total width scarce,
			// 2:1 is what keeps the clock's own template from shrinking
			// below what 3:2 gave it here, per that same comment's numbers.
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
				{Panel: Distance(), Weight: 1, Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: HeartRate(), Pad: 0.01},
				{Panel: Pace(), Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: Power(), Pad: 0.01},
				{Panel: Cadence(), Pad: 0.01},
			}},
			// This band shows the elevation profile, or the distance readout
			// in its place if there is no profile, exactly as
			// LandscapeLayout's own Alt slot does -- see that function's
			// comment for the mechanism (an Alt slot, not a Row, because the
			// two never draw in the same frame once the fill under the
			// profile became this project's own distance indicator) and for
			// why --bottom-band needs it to be Alt rather than a shared
			// predicate on Distance's own Accepts.
			//
			// climb and gradient share a strip directly above the elevation
			// band here too, for the identical reason LandscapeLayout places
			// it there -- see that function's own comment on the row for the
			// two-part case against folding it into the Alt slot below
			// instead, and for why climb sits left of gradient.
			//
			// Weight 2 of this tree's total of 16 (Route 4, Elapsed+Distance 2,
			// HR+Pace 2, Power+Cadence 2, Strip 2, Alt 3, Marker 1) -- the SAME
			// eighth LandscapeLayout's own Strip weight is of ITS total of 8,
			// not the same NUMBER: this tree's rows sum to twice landscape's
			// total, so the single unit of weight that is an eighth there is a
			// sixteenth here, and 2 is what an eighth actually costs in this
			// tree. Copying landscape's raw weight (1) into this tree would be
			// exactly the mistake the Alt band's own weight comment below
			// already names: the same unit of weight is worth less once the
			// tree's own total is larger.
			//
			// The two children split evenly, the same as LandscapeLayout's own
			// strip, and for the same reason: see that function's row comment for
			// why no weight could close the gap while ClimbPanel's track was a
			// fixed fraction of its box, and why an even split became the right
			// answer once the track took whatever width remained instead.
			//
			// Worth stating that this tree agreeing with landscape is a RESULT
			// here, not an assumption. Earlier versions of this row needed a
			// milder ratio than landscape's, because portrait hands the strip a
			// narrower absolute width for the identical fraction of a taller
			// frame, so a split tuned against landscape's much wider strip
			// crowded gradient's line and reading against its own box edges
			// here. That pressure is gone with the slack removed -- neither
			// panel now depends on the surplus -- and the two trees converged on
			// their own, each judged on its own rendered frame. If a future
			// change reintroduces a fixed-width element in either panel, expect
			// them to diverge again, and judge portrait on a 1080x1920 frame
			// rather than copying landscape's number across.
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: ClimbPanel{}, Weight: 1, Pad: 0.01},
				{Panel: GradientPanel{}, Weight: 1, Pad: 0.01},
			}},
			// Weight 3, not the 2 this slot carried before MarkerPanel's row
			// below was folded into it -- one more than LandscapeLayout's own
			// Alt weight, because this tree's rows sum to a larger total
			// (14, against landscape's 7, before climb joined either -- see
			// that row's own comment above for the totals now that it has) and
			// the SAME single unit of weight the marker row vacated is worth
			// less here proportionally; +1 is what measured out to the marker
			// row's own vacated share in THIS tree specifically, not
			// landscape's weight copied over.
			// Judged on its own gate: at 1080x1920 this keeps the "no marks"
			// case's plot comfortably tall, and a highlight-and-label render
			// legible without the plot collapsing the way it did before this
			// weight moved (see LandscapeLayout's own comment on the
			// identical trade for the reasoning that applies here too).
			{Dir: Alt, Weight: 3, Children: []Slot{
				{Panel: ElevationPanel{}, Pad: 0.01},
				{Panel: Distance(), Pad: 0.01},
			}},
			// See LandscapeLayout's own comment beside MarkerPanel: it
			// declines and Resolve prunes it before the sibling rows'
			// space is divided, so this row costs an ordinary render
			// nothing.
			{Panel: MarkerPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}
