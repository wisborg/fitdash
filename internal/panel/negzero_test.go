package panel

import (
	"testing"

	"github.com/wisborg/fitactivity/units"
)

// A figure that rounds to nothing is written as nothing, without a sign:
// "-0 m" at the foot of a profile that dips a few decimetres below sea level
// is a label saying the ground is below zero by nothing, and "-0.0%" a
// gradient that says it falls by nothing. Signs belong to figures that
// survive the rounding.
func TestFormatting_ZeroCarriesNoSign(t *testing.T) {
	for _, c := range []struct {
		m    float64
		want string
	}{
		{-0.4, "0 m"},
		{-0.0, "0 m"},
		{0.4, "0 m"},
		{-0.6, "-1 m"},
		{-0.5, "-1 m"}, // fmt alone rounds half to even, to "-0"
		{-12, "-12 m"},
	} {
		if got := formatElevation(c.m, units.Metre); got != c.want {
			t.Errorf("formatElevation(%g) = %q, want %q", c.m, got, c.want)
		}
	}
	for _, c := range []struct {
		grade float64
		want  string
	}{
		{-0.00004, "+0.0%"},
		{0.00004, "+0.0%"},
		{-0.0006, "-0.1%"},
		{0.073, "+7.3%"},
		{-0.073, "-7.3%"},
	} {
		if got := formatGrade(c.grade); got != c.want {
			t.Errorf("formatGrade(%g) = %q, want %q", c.grade, got, c.want)
		}
	}
}
