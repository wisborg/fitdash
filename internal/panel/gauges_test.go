package panel

import (
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// gaugesEpoch is an arbitrary, synthetic reference instant -- not a real
// activity's start time (see CLAUDE.md: no real dates in a committed
// fixture) -- the same discipline balanceEpoch (balance_test.go) and
// gaugeEpoch (gauge_test.go) already follow.
var gaugesEpoch = time.Date(2022, 3, 4, 0, 0, 0, 0, time.UTC)

// gaugesFixture is what gaugesTrack (below) builds a track from: which of
// the ordinary metrics readings are recorded on every sample, and, per
// balance metric, either nil (never recorded at all) or a slice of one
// genuine, distinct, nonzero value per sample -- the same "real recording,
// not a single repeated number" discipline balanceContactTrackConstant
// (balance_test.go) uses, since a real value per sample is what lets
// buildGaugeSeries' own smoothing and each BalancePanel's own Accepts walk
// the track exactly as a real render would.
type gaugesFixture struct {
	n                                                                  int
	speed, heartRate, power, cadence                                   bool
	stanceBalance, impactBalance, stiffnessBalance, oscillationBalance bool
}

// gaugesTrack builds one sample per second from gaugesEpoch, carrying
// whichever fields f names -- a value of 55 (comfortably nonzero, comfortably
// on-scale for a balance reading) wherever a balance metric is requested,
// which is all any of BalancePanel's own value accessors or Accepts needs to
// see it as genuine.
func gaugesTrack(f gaugesFixture) *fitactivity.Track {
	n := f.n
	if n == 0 {
		n = 20
	}
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		s := fitactivity.Sample{Time: gaugesEpoch.Add(time.Duration(i) * time.Second)}
		if f.speed {
			s.HasSpeed, s.Speed = true, 3.0
		}
		if f.heartRate {
			s.HasHeartRate, s.HeartRate = true, uint8(140+i%10)
		}
		if f.power {
			s.HasPower, s.Power = true, uint16(200+i%20)
		}
		if f.cadence {
			s.HasCadence, s.Cadence = true, uint8(80+i%6)
		}
		if f.stanceBalance {
			s.HasStanceTimeBalance, s.StanceTimeBalance = true, 55
		}
		dev := map[string]float64{}
		if f.impactBalance {
			dev[fitactivity.StrydImpactLoadingRateBalanceField] = 55
		}
		if f.stiffnessBalance {
			dev[fitactivity.StrydLegSpringStiffnessBalanceField] = 55
		}
		if f.oscillationBalance {
			dev[fitactivity.StrydVerticalOscillationBalanceField] = 55
		}
		if len(dev) > 0 {
			s.DevFields = dev
		}
		samples[i] = s
	}
	return &fitactivity.Track{Samples: samples}
}

// gaugesContext builds the Context a real render would over gaugesTrack(f):
// Track and Report, derived from the SAME track, so the two can never
// disagree about what it carries -- the identical discipline ctxWith
// (readout_test.go) and balancePainterFor (balance_test.go) both follow.
func gaugesContext(f gaugesFixture) *Context {
	track := gaugesTrack(f)
	return &Context{Track: track, Report: inspect.Build(track)}
}

// --- IsBalancePanel ---------------------------------------------------------

// TestIsBalancePanel_IdentifiesTheBarsAndTheirOwnPaceVariant pins the
// membership test internal/render's keep filter relies on: true for each of
// the four bars and for balancePace()'s own wrapper, false for every other
// panel in this package -- including the ORDINARY Pace(), which reports the
// identical Name() ("pace") as balancePace() and must still be told apart by
// TYPE, never by name.
func TestIsBalancePanel_IdentifiesTheBarsAndTheirOwnPaceVariant(t *testing.T) {
	for _, p := range []Panel{
		ContactBalance(), ImpactBalance(), StiffnessBalance(), OscillationBalance(), balancePace(),
	} {
		if !IsBalancePanel(p) {
			t.Errorf("IsBalancePanel(%s) = false, want true", p.Name())
		}
	}
	for _, p := range []Panel{HeartRate(), Pace(), Power(), Cadence(), Distance(), ElapsedPanel{}, RoutePanel{}} {
		if IsBalancePanel(p) {
			t.Errorf("IsBalancePanel(%s) = true, want false -- only the four bars and their own paired "+
				"Pace variant belong to the balance display", p.Name())
		}
	}
}

// --- hasAnyBalanceMetric ----------------------------------------------------

// TestHasAnyBalanceMetric_TrueOnlyWithAGenuineReading pins the OR condition
// balancePaceReadout's own Accepts is gated on: false with none of the four
// present, false when every recorded value is the refused zero
// BalancePanel's own doc comment names, true the moment even one genuine
// reading exists, and false for a nil Context or Track -- mirroring
// BalancePanel.Accepts' own defensive cases (balance_test.go) since this
// function is built entirely from those four Accepts calls.
func TestHasAnyBalanceMetric_TrueOnlyWithAGenuineReading(t *testing.T) {
	if hasAnyBalanceMetric(nil) {
		t.Error("hasAnyBalanceMetric(nil) = true, want false")
	}
	if hasAnyBalanceMetric(&Context{Track: nil}) {
		t.Error("hasAnyBalanceMetric with a nil Track = true, want false")
	}

	none := gaugesContext(gaugesFixture{speed: true})
	if hasAnyBalanceMetric(none) {
		t.Error("hasAnyBalanceMetric = true for a track carrying none of the four, want false")
	}

	allZero := gaugesTrack(gaugesFixture{})
	for i := range allZero.Samples {
		allZero.Samples[i].HasStanceTimeBalance, allZero.Samples[i].StanceTimeBalance = true, 0
	}
	zeroCtx := &Context{Track: allZero, Report: inspect.Build(allZero)}
	if hasAnyBalanceMetric(zeroCtx) {
		t.Error("hasAnyBalanceMetric = true for a track whose only balance field is every-sample-refused-zero, want false")
	}

	oneGenuine := gaugesContext(gaugesFixture{stanceBalance: true})
	if !hasAnyBalanceMetric(oneGenuine) {
		t.Error("hasAnyBalanceMetric = false for a track carrying a genuine StanceTimeBalance reading, want true")
	}

	devOnly := gaugesContext(gaugesFixture{oscillationBalance: true})
	if !hasAnyBalanceMetric(devOnly) {
		t.Error("hasAnyBalanceMetric = false for a track carrying a genuine Stryd developer balance field, want true")
	}
}

// --- balancePaceReadout.Accepts ---------------------------------------------

// TestBalancePaceReadout_AcceptsRequiresBalanceDataAlongsideItsOwnCoverage is
// the flagship test for the AND this whole feature's fallback depends on:
// balancePace() must decline whenever hasAnyBalanceMetric is false, no matter
// how good its own Pace coverage is -- otherwise the balance branch would
// survive on Pace alone (see gauges.go's own doc comment for why that is
// worse than falling back) -- and it must still honour Pace's OWN coverage
// test on top, so an activity with balance data but no usable pace (no
// speed) still declines.
func TestBalancePaceReadout_AcceptsRequiresBalanceDataAlongsideItsOwnCoverage(t *testing.T) {
	cases := []struct {
		name string
		f    gaugesFixture
		want bool
	}{
		{"neither speed nor balance", gaugesFixture{}, false},
		{"speed only, no balance data at all", gaugesFixture{speed: true}, false},
		{"balance data only, no speed", gaugesFixture{stanceBalance: true}, false},
		{"speed and balance data both present", gaugesFixture{speed: true, stanceBalance: true}, true},
		{"speed and a developer balance field both present", gaugesFixture{speed: true, oscillationBalance: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := gaugesContext(c.f)
			if got := balancePace().Accepts(ctx); got != c.want {
				t.Errorf("balancePace().Accepts() = %v, want %v", got, c.want)
			}
		})
	}
}

// --- the gauge Alt slot over the REAL layouts -------------------------------

// gaugeOrdinaryKeep is what internal/render's keep filter reduces to under
// --gauges balance (or under no --gauges special-casing at all): every
// panel's own Accepts, and nothing else. Used to exercise the gauge Alt
// slot's own composition -- gaugeBalanceColumn versus the plain metrics
// column -- independently of the --gauges=metrics tripwire, which is a
// SEPARATE thing this file checks with gaugeMetricsKeep, below.
func gaugeOrdinaryKeep(ctx *Context) func(Panel) bool {
	return func(p Panel) bool { return p.Accepts(ctx) }
}

// gaugeMetricsKeep is internal/render's own one-line keep-filter addition,
// restated here so this package's own tests can exercise it without
// depending on internal/render (which imports internal/panel, not the other
// way around): reject anything IsBalancePanel names outright, before its own
// Accepts is ever asked, then fall back to ordinary Accepts for everything
// else -- see render.New's keep closure for the real thing this mirrors.
func gaugeMetricsKeep(ctx *Context) func(Panel) bool {
	return func(p Panel) bool {
		if IsBalancePanel(p) {
			return false
		}
		return p.Accepts(ctx)
	}
}

// gaugePanelNames collects the Name() of every placed panel that is one of
// the ordinary four gauges or one of the balance branch's own five, for the
// membership assertions below -- deliberately narrow rather than the whole
// placed set, since this test's own subject is the gauge Alt slot alone and
// every other panel's placement is exercised elsewhere.
func gaugePanelNames(placed []Placed) map[string]int {
	watch := map[string]bool{
		"heart-rate": true, "pace": true, "power": true, "cadence": true,
		"contact-balance": true, "impact-balance": true, "stiffness-balance": true, "oscillation-balance": true,
	}
	counts := map[string]int{}
	for _, p := range placed {
		if watch[p.Panel.Name()] {
			counts[p.Panel.Name()]++
		}
	}
	return counts
}

// TestResolve_GaugeAltOverTheRealLayoutsPlacesTheRightPanels is the
// membership test the plan asks for: over BOTH real trees, at three frame
// sizes, which of the gauge Alt slot's two candidates actually won, checked
// by NAME rather than only by "the boxes tile" -- a regression that placed
// the wrong candidate, or left pace stranded alone, would satisfy every
// geometric assertion and still be wrong.
func TestResolve_GaugeAltOverTheRealLayoutsPlacesTheRightPanels(t *testing.T) {
	full := gaugesFixture{speed: true, heartRate: true, power: true, cadence: true,
		stanceBalance: true, impactBalance: true, stiffnessBalance: true, oscillationBalance: true}
	partial := gaugesFixture{speed: true, heartRate: true, power: true, cadence: true, stanceBalance: true}
	none := gaugesFixture{speed: true, heartRate: true, power: true, cadence: true}

	cases := []struct {
		name string
		keep func(Panel) bool
		want map[string]int
	}{
		{
			"--gauges metrics, activity carries full balance data too -- metrics wins regardless",
			gaugeMetricsKeep(gaugesContext(full)),
			map[string]int{"heart-rate": 1, "pace": 1, "power": 1, "cadence": 1},
		},
		{
			"--gauges balance (or no tripwire), full balance data -- balance wins, metrics never tried",
			gaugeOrdinaryKeep(gaugesContext(full)),
			map[string]int{"pace": 1, "contact-balance": 1, "impact-balance": 1, "stiffness-balance": 1, "oscillation-balance": 1},
		},
		{
			"--gauges balance, only contact-balance present -- pace plus the surviving bar",
			gaugeOrdinaryKeep(gaugesContext(partial)),
			map[string]int{"pace": 1, "contact-balance": 1},
		},
		{
			"--gauges balance, activity carries none of the four -- prunes to nothing, falls through to metrics",
			gaugeOrdinaryKeep(gaugesContext(none)),
			map[string]int{"heart-rate": 1, "pace": 1, "power": 1, "cadence": 1},
		},
	}

	sizes := []struct {
		name string
		w, h int
	}{
		{"1080p", 1920, 1080},
		{"4K", 3840, 2160},
		{"portrait", 1080, 1920},
	}

	for _, l := range []Layout{LandscapeLayout(), PortraitLayout()} {
		for _, s := range sizes {
			for _, c := range cases {
				t.Run(l.Name+"/"+s.name+"/"+c.name, func(t *testing.T) {
					placed, err := l.Resolve(s.w, s.h, c.keep)
					if err != nil {
						t.Fatalf("Resolve: %v", err)
					}
					got := gaugePanelNames(placed)
					if len(got) != len(c.want) {
						t.Fatalf("gauge-related panels placed = %v, want %v", got, c.want)
					}
					for name, wantCount := range c.want {
						if got[name] != wantCount {
							t.Errorf("%q placed %d time(s), want %d (placed: %v)", name, got[name], wantCount, got)
						}
					}
					assertNoOverlap(t, placed)
				})
			}
		}
	}
}
