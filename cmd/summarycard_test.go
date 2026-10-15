package cmd

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/panel"
)

// A flight's card is titled with the flight, names the aircraft under it,
// and gives its day where it
// began, its distance and time from pushback, and its top speed and
// cruising level, in aviation's units.
func TestSummaryCardForAFlight(t *testing.T) {
	track := syntheticFlight()
	track.Flights = []fitactivity.Flight{{Number: "XY123", From: "AAA", To: "BBB", Aircraft: "Example 320", Registration: "XX-ABC"}}
	track.FillDistanceFromGPS()
	for i := range track.Samples {
		s := &track.Samples[i]
		s.HasSpeed, s.Speed = true, 230
		s.HasElevation, s.Elevation = true, 36000*0.3048
	}
	timer := fitactivity.BuildTimerModel(track)
	u, err := aviationUnits(nil)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := timer.Window()
	pushback := start.Add(20 * time.Minute)
	card := summaryCard(track, timer, nil, u, panel.SpeedReadoutSpeed, true, pushback)
	if card.Title != "XY123 AAA → BBB" {
		t.Errorf("title %q", card.Title)
	}
	if len(card.Lines) == 0 || card.Lines[0] != "Example 320 · XX-ABC" {
		t.Errorf("lines %q, want the aircraft first, under the title", card.Lines)
	}
	// Sydney's longitude, ten hours ahead: 08:00 UTC is 18:00 there, the
	// same day.
	all := strings.Join(card.Lines, " | ")
	for _, want := range []string{"1 Jan 2026", " nm · ", "max 447 kn", "FL360"} {
		if !strings.Contains(all, want) {
			t.Errorf("card %q lacks %q", all, want)
		}
	}
	if strings.Contains(all, "2:18:00") {
		t.Errorf("card %q times the flight from the recording's start, not its pushback", all)
	}
}

// Anything else's card is titled with its sport and gives its climb and its
// average pace, and leaves out a figure the file does not have.
func TestSummaryCardForARun(t *testing.T) {
	opts := fittest.DefaultOptions()
	path := filepath.Join(t.TempDir(), "run.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, err := fitactivity.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	timer := fitactivity.BuildTimerModel(track)
	u, _ := units.Of(units.Metric)
	card := summaryCard(track, timer, panel.BuildElevation(track, fitactivity.ElevationOptions{}), u, panel.SpeedReadoutPace, false, time.Time{})
	if card.Title == "" {
		t.Error("no title")
	}
	all := strings.Join(card.Lines, " | ")
	if !strings.Contains(all, " km · ") || !strings.Contains(all, "/km") {
		t.Errorf("card %q lacks the distance, time and average pace", all)
	}
	if strings.Contains(all, "FL") || strings.Contains(all, "max ") {
		t.Errorf("a run's card %q has a flight's figures", all)
	}
}

// --layout flyover is the flyover view; asking for another view by name
// with it is refused before anything is read.
func TestFullFrameRefusesAnotherView(t *testing.T) {
	renderOpts = renderOptions{}
	cmd := &cobra.Command{Use: "test", Args: cobra.ExactArgs(1), RunE: runRender, SilenceUsage: true, SilenceErrors: true}
	bindRenderFlags(cmd)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--layout", "flyover", "--route-view", "3d", filepath.Join(t.TempDir(), "absent.fit")})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--layout flyover") {
		t.Errorf("--layout flyover --route-view 3d: %v", err)
	}
}

// A title or a subtitle is the full-frame flyover's card's; asked for with
// any other layout it is refused, not dropped.
func TestTitleNeedsTheFullFrame(t *testing.T) {
	for _, flag := range []string{"--title", "--subtitle"} {
		renderOpts = renderOptions{}
		cmd := &cobra.Command{Use: "test", Args: cobra.ExactArgs(1), RunE: runRender, SilenceUsage: true, SilenceErrors: true}
		bindRenderFlags(cmd)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{flag, "Home", filepath.Join(t.TempDir(), "absent.fit")})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "--layout flyover") {
			t.Errorf("%s without --layout flyover: %v", flag, err)
		}
	}
}
