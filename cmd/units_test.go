package cmd

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/panel"
)

// panelsLine is the summary's "panels:" line.
func panelsLine(stderr string) string {
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "panels: ") {
			return l
		}
	}
	return ""
}

// A run renders pace by default and says nothing of units; --units imperial
// and --speed-readout speed put speed in pace's place and say both, and pace
// is not reported as declined, since nothing is missing.
func TestRunRender_UnitsAndSpeedReadout(t *testing.T) {
	run := mergePiece(t, t.TempDir(), "run.fit", mergeBase)

	_, stderr, err := runCLI(t, runRender, true, "--dry-run", "--output-dir", t.TempDir(), "--size", "640x360", run)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, stderr)
	}
	if p := panelsLine(stderr); !strings.Contains(p, "pace") || strings.Contains(p, "speed") {
		t.Errorf("a run by default: %q, want pace and no speed", p)
	}
	if strings.Contains(stderr, "units:") || strings.Contains(stderr, "speed shown") {
		t.Errorf("a default render reported units:\n%s", stderr)
	}

	_, stderr, err = runCLI(t, runRender, true, "--dry-run", "--output-dir", t.TempDir(), "--size", "640x360",
		"--units", "imperial", "--speed-readout", "speed", run)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, stderr)
	}
	if p := panelsLine(stderr); !strings.Contains(p, "speed") || strings.Contains(p, "pace") {
		t.Errorf("--speed-readout speed: %q, want speed and no pace", p)
	}
	for _, want := range []string{"units: mi, ft, mph, min/mi", "speed shown as speed, not pace (--speed-readout speed)"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "declined (this activity carries no such data): pace") {
		t.Errorf("pace reported as missing data:\n%s", stderr)
	}
}

// Units, a unit, or a speed readout there are not are refused, naming the
// flag, before the activity is read.
func TestRunRender_RefusesBadUnits(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--units", "nautical"}, "--units: \"nautical\" is not a system of units"},
		{[]string{"--unit", "speed=kph"}, "--unit: \"kph\" is not a unit of speed"},
		{[]string{"--speed-readout", "velocity"}, "--speed-readout \"velocity\" is invalid"},
	} {
		args := append(append([]string{"--dry-run"}, c.args...), "missing.fit")
		if _, _, err := runCLI(t, runRender, true, args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}

// --elevation-gain and --elevation-loss are read in the elevation unit:
// 1000 ft is 304.8 m.
func TestResolveElevationTuning_InFeet(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{elevationGain: 1000, elevationLoss: 500}
	imperial, _ := units.Of(units.Imperial)
	opts, source := resolveElevationTuning(nil, imperial)
	if math.Abs(opts.TargetGain-304.8) > 1e-9 || math.Abs(opts.TargetLoss-152.4) > 1e-9 || source != elevationTuningSourceTargets {
		t.Errorf("%+v from %q; want 304.8 and 152.4 m from the targets", opts, source)
	}
}

// The summary says why speed was chosen: the flag, or auto and the sport --
// or that the file named none.
func TestWriteUnitsSummary_SaysWhy(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	metric, _ := units.Of(units.Metric)
	for _, c := range []struct{ mode, sport, want string }{
		{panel.SpeedReadoutAuto, "cycling", `speed shown as speed, not pace (auto, for sport "cycling")`},
		{panel.SpeedReadoutAuto, "", "speed shown as speed, not pace (auto, for a file that names no sport)"},
	} {
		renderOpts.speedReadout = c.mode
		var b bytes.Buffer
		writeUnitsSummary(&b, metric, panel.SpeedReadoutSpeed, c.sport)
		if strings.TrimSpace(b.String()) != c.want {
			t.Errorf("%q", b.String())
		}
	}
	var b bytes.Buffer
	writeUnitsSummary(&b, metric, panel.SpeedReadoutPace, "running")
	if b.Len() != 0 {
		t.Errorf("a metric render of pace reported %q", b.String())
	}
}
