package cmd

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/wisborg/fitactivity/fittest"
)

// writePlotActivity is twenty synthetic minutes with power on the first
// half only, and a footpod's developer field named as Stryd names its
// power.
func writePlotActivity(t *testing.T) string {
	t.Helper()
	opts := fittest.DefaultOptions()
	opts.Count = 1200
	opts.PowerWatts = 250
	opts.PoweredRecords = 600
	opts.DeveloperField = "Power"
	path := filepath.Join(t.TempDir(), "plot.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	return path
}

// runPlotCmd runs plot with args, every flag at its default first.
func runPlotCmd(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	cmd := &cobra.Command{Use: "plot", Args: cobra.MinimumNArgs(1), RunE: runPlot, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().AddFlagSet(plotCmd.Flags())
	// The flags are shared with plotCmd and write into plotOpts, so one
	// test's --y would otherwise be the next one's too.
	// A list flag's default reads "[]", which Set would take for an item,
	// so the lists are emptied directly.
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, list := f.Value.(pflag.SliceValue); !list {
			if err := f.Value.Set(f.DefValue); err != nil {
				t.Fatalf("restoring the default for --%s: %v", f.Name, err)
			}
		}
		f.Changed = false
	})
	plotOpts.y, plotOpts.unitEach, plotOpts.fonts = nil, nil, nil
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return o.String(), e.String(), err
}

// Without --y the metrics the activity has are listed with their coverage,
// and the ones it has not are left out.
func TestPlotListsTheMetrics(t *testing.T) {
	out, _, err := runPlotCmd(t, writePlotActivity(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"heart-rate", "power", "pace", "50.0%", "Power"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "temperature") {
		t.Errorf("the listing offers temperature, which the activity does not have:\n%s", out)
	}
}

// A chart is drawn the size asked for, titled with the metric and its unit;
// a metric the activity carries part of the time is drawn, and said to be
// partial.
func TestPlotDrawsAChart(t *testing.T) {
	out, errOut, err := runPlotCmd(t, writePlotActivity(t), "--y", "power", "--width", "70", "--height", "15")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 15 {
		t.Errorf("%d lines, want 15:\n%s", len(lines), out)
	}
	if lines[0] != "power (W) against time" {
		t.Errorf("title %q", lines[0])
	}
	if !strings.Contains(errOut, "power present 50% of the time") {
		t.Errorf("no word that power is partial: %q", errOut)
	}
	if _, errOut, _ := runPlotCmd(t, writePlotActivity(t), "--y", "heart-rate", "--width", "70", "--height", "15"); errOut != "" {
		t.Errorf("a metric present throughout is said to be partial: %q", errOut)
	}
}

// The watch's power and a footpod's "Power" are told apart by their case.
func TestPlotTellsTheFootpodsPowerApart(t *testing.T) {
	watch, _, err := runPlotCmd(t, writePlotActivity(t), "--y", "power", "--width", "70", "--height", "15")
	if err != nil {
		t.Fatal(err)
	}
	pod, _, err := runPlotCmd(t, writePlotActivity(t), "--y", "Power", "--width", "70", "--height", "15")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(pod, "power (W)") || watch == pod {
		t.Errorf("--y Power drew the watch's power, not the footpod's:\n%s", pod)
	}
}

// A metric the activity does not carry, or one plot does not know, is
// refused with the reason, not drawn as a flat line at zero.
func TestPlotRefusesWhatIsNotThere(t *testing.T) {
	path := writePlotActivity(t)
	if _, _, err := runPlotCmd(t, path, "--y", "temperature"); err == nil || !strings.Contains(err.Error(), "no temperature") {
		t.Errorf("temperature, which the activity lacks: %v", err)
	}
	if _, _, err := runPlotCmd(t, path, "--y", "watts"); err == nil || !strings.Contains(err.Error(), "heart-rate") {
		t.Errorf("an unknown metric is refused without the list of known ones: %v", err)
	}
	if _, _, err := runPlotCmd(t, path, "--y", "power", "--x", "altitude"); err == nil {
		t.Error("--x altitude was accepted")
	}
}

// Against distance, the x axis is in the chart's distance unit.
func TestPlotAgainstDistance(t *testing.T) {
	out, _, err := runPlotCmd(t, writePlotActivity(t), "--y", "pace", "--x", "distance", "--units", "imperial", "--width", "70", "--height", "15")
	if err != nil {
		t.Fatal(err)
	}
	if first := strings.SplitN(out, "\n", 2)[0]; first != "pace (min/mi) against distance (mi)" {
		t.Errorf("title %q", first)
	}
}

// --out writes a PNG of the size asked for, says where, and refuses a name
// that is not a PNG's rather than writing PNG bytes under it.
func TestPlotWritesAPNG(t *testing.T) {
	path := writePlotActivity(t)
	out := filepath.Join(t.TempDir(), "hr.png")
	_, errOut, err := runPlotCmd(t, path, "--y", "heart-rate", "--out", out, "--width", "640", "--height", "320")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("%s is not a PNG: %v", out, err)
	}
	if cfg.Width != 640 || cfg.Height != 320 {
		t.Errorf("a PNG %d by %d, want 640 by 320", cfg.Width, cfg.Height)
	}
	if !strings.Contains(errOut, out) {
		t.Errorf("not said where the chart went: %q", errOut)
	}
	if _, _, err := runPlotCmd(t, path, "--y", "heart-rate", "--out", filepath.Join(t.TempDir(), "hr.jpg")); err == nil {
		t.Error("--out hr.jpg was accepted")
	}
}

// Into anything but a terminal the chart is braille; --terminal kitty is
// a picture whatever the output.
func TestPlotTerminalChoice(t *testing.T) {
	path := writePlotActivity(t)
	out, _, err := runPlotCmd(t, path, "--y", "heart-rate", "--width", "60", "--height", "12")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b") || !strings.HasPrefix(out, "heart-rate (bpm)") {
		t.Errorf("not braille into a buffer: %q", out[:min(len(out), 60)])
	}
	out, _, err = runPlotCmd(t, path, "--y", "heart-rate", "--width", "60", "--height", "12", "--terminal", "kitty")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "\x1b_Ga=T,f=100,q=2,c=60,") {
		t.Errorf("--terminal kitty drew %q", out[:min(len(out), 40)])
	}
}

// Several metrics, comma-separated or repeated, are charted one strip each
// under one title; a metric named twice, or one the activity lacks among
// several, is refused; and each partial metric is said to be.
func TestPlotSeveralMetrics(t *testing.T) {
	path := writePlotActivity(t)
	for _, args := range [][]string{{"--y", "heart-rate,power"}, {"--y", "heart-rate", "--y", "power"}} {
		out, errOut, err := runPlotCmd(t, append([]string{path, "--width", "70", "--height", "30"}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 30 || lines[0] != "heart-rate and power against time" {
			t.Errorf("%v: %d lines titled %q", args, len(lines), lines[0])
		}
		for _, head := range []string{"│heart-rate (bpm)", "│power (W)"} {
			if !strings.Contains(out, head) {
				t.Errorf("%v: no strip headed %q", args, head)
			}
		}
		if !strings.Contains(errOut, "power present 50%") {
			t.Errorf("%v: power's partial coverage not said: %q", args, errOut)
		}
	}
	if _, _, err := runPlotCmd(t, path, "--y", "power,power"); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("power twice: %v", err)
	}
	if _, _, err := runPlotCmd(t, path, "--y", "power,temperature"); err == nil || !strings.Contains(err.Error(), "no temperature") {
		t.Errorf("temperature among several: %v", err)
	}
}

// A PNG of several metrics is taller by default, so each strip has room.
func TestPlotSeveralMetricsPNGHeight(t *testing.T) {
	out := filepath.Join(t.TempDir(), "two.png")
	if _, _, err := runPlotCmd(t, writePlotActivity(t), "--y", "heart-rate,power", "--out", out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != plotImageWidth || cfg.Height != plotImageHeightFor(2) || plotImageHeightFor(2) <= plotImageHeight {
		t.Errorf("two metrics' PNG is %d by %d, want %d by %d, taller than one's", cfg.Width, cfg.Height, plotImageWidth, plotImageHeightFor(2))
	}
}
