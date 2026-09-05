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
	// see hasAnyBalanceMetric and balancePaceReadout's own Accepts -- so this
	// flag adds a second way to reach that same fallback, never a different
	// one.
	GaugesMetrics = "metrics"

	// GaugesBalance shows the four balance bars (BalancePanel, balance.go)
	// instead of the plain metrics, with Pace kept beside them (balancePace,
	// below) as the effort context the bars are read against.
	GaugesBalance = "balance"
)

// hasAnyBalanceMetric reports whether ctx's activity carries at least one of
// the four balance readings -- ContactBalance, ImpactBalance,
// StiffnessBalance or OscillationBalance -- through each panel's own Accepts,
// never a name-based guess. This is the OR that decides whether the balance
// branch of the gauge Alt slot has anything of its own to show at all.
//
// It exists so balancePaceReadout (below) can require it on top of Pace's own
// ordinary coverage test. An activity can carry pace with NONE of the four --
// an ordinary run with no footpod and no Running Dynamics is the common case,
// not an edge one -- and Pace surviving ALONE in the balance branch, every bar
// beside it declined, is not balance: it is a smaller, worse copy of the
// metrics branch wearing the balance branch's box, and a user who typed
// --gauges balance would see one number where they expected four. Gating on
// this is what makes "the branch prunes to nothing and the Alt falls through
// to the ordinary gauges" (see --gauges' own help text, cmd/render.go's
// bindRenderFlags) a fact the generic layout engine reaches on its own, with
// no name-based special case in internal/render: once every one of the five
// leaves in gaugeBalanceColumn declines, pruneSlot's own ordinary rule -- a
// split with zero surviving children is itself pruned -- removes the whole
// branch, and the Alt tries its next candidate for free.
func hasAnyBalanceMetric(ctx *Context) bool {
	for _, b := range []BalancePanel{ContactBalance(), ImpactBalance(), StiffnessBalance(), OscillationBalance()} {
		if b.Accepts(ctx) {
			return true
		}
	}
	return false
}

// balancePaceReadout marks Pace() as belonging to the balance branch of the
// gauge Alt slot (gaugeBalanceColumn, below) rather than the ordinary metrics
// branch -- purely so IsBalancePanel (below) can tell the two apart at the
// point internal/render's keep filter needs to. An ordinary Pace() value is
// otherwise indistinguishable, by type or by name, from the one placed here:
// both report the identical Name(), "pace".
type balancePaceReadout struct{ Readout }

// Accepts requires hasAnyBalanceMetric on top of Pace's own ordinary
// coverage test -- see that function's own doc comment for why the AND is
// necessary and where it lives. Name and Prepare are Pace's own, promoted
// from the embedded Readout unchanged: wrapping touches only WHETHER this
// panel is placed in the balance branch, never what it draws once it is.
func (p balancePaceReadout) Accepts(ctx *Context) bool {
	return hasAnyBalanceMetric(ctx) && p.Readout.Accepts(ctx)
}

// balancePace is Pace(), wrapped so it survives in the balance branch of the
// gauge Alt slot only when the activity carries at least one of the four
// balance metrics beside it -- see balancePaceReadout.
func balancePace() Panel {
	return balancePaceReadout{Pace()}
}

// IsBalancePanel reports whether p is part of the balance gauge display --
// one of the four bars, or their own paired Pace variant -- rather than any
// other panel in the tree.
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
	case BalancePanel, balancePaceReadout:
		return true
	default:
		return false
	}
}

// gaugeBalancePaceWeight and gaugeBalanceBarWeight are gaugeBalanceColumn's
// own weights: pace at twice each bar's own share, a first guess to be judged
// on a rendered frame rather than a derivation -- see gaugeBalanceColumn's
// own doc comment and, in the layout chapter's own history, the two rounds it
// took to retune the elapsed/distance split the identical way.
const (
	gaugeBalancePaceWeight = 2
	gaugeBalanceBarWeight  = 1
)

// gaugeBalanceColumn is the "balance" candidate of the gauge Alt slot in
// BOTH LandscapeLayout and PortraitLayout (layouts.go) -- factored into one
// function so the two trees cannot drift apart on the one thing they must
// agree on: the same five panels, at the same relative weights, full-width
// bars, regardless of which tree is asking.
//
// Pace sits first, at twice each bar's own weight: it is the effort context
// the bars are read against, which puts the one number-first element ahead
// of a group of near-identical bars rather than orphaned behind them, and a
// balance bar is a single horizontal element needing far less height than
// pace's own three-row readout.
func gaugeBalanceColumn() Slot {
	return Slot{Dir: Col, Children: []Slot{
		{Panel: balancePace(), Weight: gaugeBalancePaceWeight, Pad: 0.01},
		{Panel: ContactBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: ImpactBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: StiffnessBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
		{Panel: OscillationBalance(), Weight: gaugeBalanceBarWeight, Pad: 0.01},
	}}
}
