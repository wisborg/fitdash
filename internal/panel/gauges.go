package panel

// --gauges' legal values -- which of the gauge block's two Alt candidates
// (gaugeBalanceColumn, below, placed at the gauge slot in both LandscapeLayout
// and PortraitLayout) the user gets: today's four fluctuating readouts, or
// the left/right balance bars kept alongside pace. Exported for the same
// reason BottomBandProfile/BottomBandDistance are (layouts.go): the CLI's own
// validation and Context.Gauges compare against one pair of strings rather
// than each restating them as literals a rename here could silently stop
// matching.
const (
	// GaugesMetrics is the default: heart rate, pace, power and cadence,
	// exactly as before this flag existed. An activity that carries none of
	// the four balance metrics falls back to this under GaugesBalance too --
	// see hasAnyBalanceMetric and balanceReadout's own Accepts -- so this
	// flag adds a second way to reach that same fallback, never a different
	// one.
	GaugesMetrics = "metrics"

	// GaugesBalance shows the four balance bars (BalancePanel, balance.go)
	// instead of the plain metrics, with pace and step length kept beside
	// them (balancePace, balanceStepLength, below) as the effort context the
	// bars are read against.
	GaugesBalance = "balance"
)

// hasAnyBalanceMetric reports whether ctx's activity carries at least one of
// the four balance readings -- ContactBalance, ImpactBalance,
// StiffnessBalance or OscillationBalance -- through each panel's own Accepts,
// never a name-based guess. This is the OR that decides whether the balance
// branch of the gauge Alt slot has anything of its own to show at all.
//
// It exists so balanceReadout (below) can require it on top of Pace's or
// StepLength's own ordinary coverage test. An activity can carry either with
// NONE of the four -- an ordinary run with no footpod and no Running
// Dynamics is the common case, not an edge one -- and either one surviving
// ALONE in the balance branch, every bar beside it declined, is not balance:
// it is a smaller, worse copy of the metrics branch wearing the balance
// branch's box, and a user who typed --gauges balance would see one or two
// numbers where they expected four bars. Gating on this is what makes "the
// branch prunes to nothing and the Alt falls through to the ordinary gauges"
// (see --gauges' own help text, cmd/render.go's bindRenderFlags) a fact the
// generic layout engine reaches on its own, with no name-based special case
// in internal/render: once every one of gaugeBalanceColumn's leaves
// declines, pruneSlot's own ordinary rule -- a split with zero surviving
// children is itself pruned -- removes the whole branch, and the Alt tries
// its next candidate for free.
func hasAnyBalanceMetric(ctx *Context) bool {
	for _, b := range []BalancePanel{ContactBalance(), ImpactBalance(), StiffnessBalance(), OscillationBalance()} {
		if b.Accepts(ctx) {
			return true
		}
	}
	return false
}

// balanceReadout marks an ordinary Readout as belonging to the balance
// branch of the gauge Alt slot (gaugeBalanceColumn, below) rather than the
// ordinary metrics branch -- purely so IsBalancePanel (below) can tell them
// apart at the point internal/render's keep filter needs to. Two Readouts
// share this wrapper, Pace and StepLength, because both need the identical
// extra condition on top of their own ordinary coverage test: neither may
// survive ALONE in the balance branch once every one of the four bars has
// declined -- see hasAnyBalanceMetric's own doc comment for why a lone
// survivor there is worse than falling back to the ordinary metrics column
// entirely, a finding that applies just as much to a step-length reading
// stranded with no bars and no pace beside it as it does to pace alone. An
// ordinary Pace() or StepLength() value is otherwise indistinguishable, by
// type or by name, from the one placed here: each reports the identical
// Name() its unwrapped constructor would.
type balanceReadout struct{ Readout }

// Accepts requires hasAnyBalanceMetric on top of the embedded Readout's own
// ordinary coverage test -- see balanceReadout's own doc comment for why the
// AND is necessary and where hasAnyBalanceMetric lives. Name and Prepare are
// the embedded Readout's own, promoted unchanged: wrapping touches only
// WHETHER a panel is placed in the balance branch, never what it draws once
// it is.
func (p balanceReadout) Accepts(ctx *Context) bool {
	return hasAnyBalanceMetric(ctx) && p.Readout.Accepts(ctx)
}

// balancePace is Pace(), wrapped so it survives in the balance branch of the
// gauge Alt slot only when the activity carries at least one of the four
// balance metrics beside it -- see balanceReadout.
func balancePace() balanceReadout {
	return balanceReadout{Pace()}
}

// balanceStepLength is StepLength(), wrapped identically to balancePace --
// see balanceReadout's own doc comment for why both readouts need the same
// extra condition. Seated beside pace (gaugeBalanceColumn, below) as a
// second piece of effort context the bars are read against: a step length is
// a magnitude, not a balance, so it belongs in this column as an ordinary
// Readout rather than as a fifth bar (see StepLength's own doc comment).
func balanceStepLength() balanceReadout {
	return balanceReadout{StepLength()}
}

// IsBalancePanel reports whether p is part of the balance gauge display --
// one of the four bars, or one of their own paired Readout variants -- rather
// than any other panel in the tree.
//
// A type assertion, not a list of panel names: internal/render's keep filter
// uses this to omit the whole balance display under --gauges metrics (see
// render.New's keep closure), and a type switch on BalancePanel means a
// fifth balance metric, whenever one lands, is covered automatically the day
// its own constructor exists -- it costs internal/render nothing, which is
// the whole point of delegating membership here instead of hand-listing
// names in the one package that must never need to know how many there are.
func IsBalancePanel(p Panel) bool {
	switch p.(type) {
	case BalancePanel, balanceReadout:
		return true
	default:
		return false
	}
}

// gaugeBalanceReadoutRowWeight and gaugeBalanceBarWeight are
// gaugeBalanceColumn's own weights: the {pace, step length} row at twice each
// bar's own share -- unchanged from pace's own original solo weight, since
// the row now splits that same share between two peers rather than growing
// it -- a first guess to be judged on a rendered frame rather than a
// derivation, see gaugeBalanceColumn's own doc comment and, in the layout
// chapter's own history, the two rounds it took to retune the
// elapsed/distance split the identical way.
const (
	gaugeBalanceReadoutRowWeight = 2
	gaugeBalanceBarWeight        = 1
)

// gaugeBalanceColumn is the "balance" candidate of the gauge Alt slot in
// BOTH LandscapeLayout and PortraitLayout (layouts.go) -- factored into one
// function so the two trees cannot drift apart on the one thing they must
// agree on: the same panels, at the same relative weights, full-width bars,
// regardless of which tree is asking.
//
// Pace and step length share the top row, side by side at EQUAL weight: both
// are three-row readouts (label, value, unit) rather than a bar, and both are
// effort context the bars are read against, so neither outranks the other
// the way pace alone once implicitly did. The row as a whole keeps pace's own
// original weight, twice each bar's own share, which puts this one
// number-first row ahead of a group of near-identical bars rather than
// orphaned behind them -- a balance bar is a single horizontal element
// needing far less height than a three-row readout does.
func gaugeBalanceColumn() Slot {
	return Slot{Dir: Col, Children: []Slot{
		// The Row itself carries no Pad of its own -- only its two children
		// do, the identical pattern layouts.go's own ElapsedPanel/Distance
		// Row uses. A Pad on the Row IN ADDITION to a Pad on each child would
		// nest two independent insets, which breaks the single-level
		// "every leaf carries the SAME Pad" property
		// TestResolve_DeclineCombinationsOverTheRealLayoutsLeaveNoUnclaimedRectangle
		// (layout_test.go) checks by reconstructing each leaf's pre-pad box:
		// a leaf under a padded Row would need its OWN pad added back twice
		// to recover what the tree actually handed it.
		{Dir: Row, Weight: gaugeBalanceReadoutRowWeight, Children: sizedAsPeers(
			balancePace(), balanceStepLength(),
		)},
		{Panel: ContactBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: ImpactBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: StiffnessBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: OscillationBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
	}}
}

// sizedAsPeers seats readouts side by side in one Row at the same text size.
//
// A Readout sizes its reading against its OWN widest template, which is
// right when readouts are stacked in a column: each fills the width it was
// given, and nobody compares them. Seated in a Row they are compared, by
// anyone who looks. Pace's template is "88:88" and a step length's is three
// digits, so the narrower template won more font size and the step length
// printed visibly larger than the pace beside it -- which reads as a claim
// that one of the two matters more, made by nothing but the number of
// characters each happens to need.
//
// So every member is given the widest template of the group. They then fit
// against the same string in boxes the Row already made the same width, and
// come out the same size. Nothing else about them changes: each still reads
// its own metric, prints its own unit, and declines on its own terms.
//
// Widest is measured in RUNES, which orders width faithfully only because
// this project draws in gomono (canvas.go) and every glyph there is one
// advance wide. Under a proportional face this would have to measure the
// string through the face instead -- and it could not do it here, because
// there is no Fonts to measure with until Prepare.
func sizedAsPeers(rs ...balanceReadout) []Slot {
	widest := ""
	for _, r := range rs {
		if t := r.valueTemplate(); len([]rune(t)) > len([]rune(widest)) {
			widest = t
		}
	}
	out := make([]Slot, 0, len(rs))
	for _, r := range rs {
		r.Readout.template = widest
		out = append(out, Slot{Panel: r, Pad: 0.01})
	}
	return out
}
