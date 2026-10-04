package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"

	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/panel"
)

// bindUnitFlags attaches --units, --unit and --speed-readout. --units and
// --unit are spelt as videofx and course spell them, and read by
// fitactivity's units package in all three, so one activity reads the same
// in each.
func bindUnitFlags(f *pflag.FlagSet) {
	f.StringVar(&renderOpts.units, "units", string(units.Metric),
		"the units the dashboard's numbers are written in -- \"metric\" (default: km, m, km/h, min/km) or \"imperial\" "+
			"(mi, ft, mph, min/mi). The distance readout, the profile's axis and heights, the climb panel, pace and speed all "+
			"follow it, and so do --elevation-gain and --elevation-loss, which are read in its elevation unit. "+
			"`fitdash inspect` reports what the file recorded and stays in its units")
	f.StringArrayVar(&renderOpts.unitEach, "unit", nil,
		"one quantity's unit, over --units (repeatable) -- distance=km|mi|nmi, elevation=m|ft, speed=km/h|mph|kn|m/s, "+
			"pace=min/km|min/mi. For a flight's mixture: --units imperial --unit distance=nmi --unit speed=kn")
	f.StringVar(&renderOpts.speedReadout, "speed-readout", panel.SpeedReadoutAuto,
		"how the gauge column reads speed -- \""+panel.SpeedReadoutAuto+"\" (default: pace for running, walking and hiking, "+
			"speed for every other sport, and for a file that names none, such as a GPX or KML track), \""+
			panel.SpeedReadoutPace+"\" (time per distance, as a runner reads it) or \""+panel.SpeedReadoutSpeed+
			"\" (distance an hour, as a cyclist or a pilot reads it)")
}

// parseUnits is the units --units and every --unit over it ask for, refused
// where the user typed them rather than rendering in a unit nobody chose.
func parseUnits(system string, each []string) (units.Set, error) {
	set, err := units.Of(units.System(system))
	if err != nil {
		return set, fmt.Errorf("render: --units: %w", err)
	}
	for _, spec := range each {
		if err := set.UseSpec(spec); err != nil {
			return set, fmt.Errorf("render: --unit: %w", err)
		}
	}
	return set, nil
}

// writeUnitsSummary says what the render was written in, when that is not
// the default: the units, unless they are metric, and the gauge column's
// reading of speed, unless it is pace -- the one a render without these
// flags would have shown, so a ride's switch to speed is announced rather
// than discovered.
func writeUnitsSummary(out io.Writer, u units.Set, readout, sport string) {
	if metric, _ := units.Of(units.Metric); u != metric {
		names := []string{u.Distance.Name, u.Elevation.Name, u.Speed.Name, u.Pace.Name}
		fmt.Fprintf(out, "units: %s\n", strings.Join(names, ", "))
	}
	if readout == panel.SpeedReadoutSpeed {
		why := "--speed-readout " + renderOpts.speedReadout
		if renderOpts.speedReadout == panel.SpeedReadoutAuto {
			why = fmt.Sprintf("auto, for sport %q", sport)
			if sport == "" {
				why = "auto, for a file that names no sport"
			}
		}
		fmt.Fprintf(out, "speed shown as speed, not pace (%s)\n", why)
	}
}
