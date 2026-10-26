package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
	fs "github.com/wisborg/fitactivity/strava"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
)

// cachedStrava is a login and a cached answer for an invented activity, in
// a configuration and cache directory of the test's own, so a strava:
// source is read without asking Strava anything. Every value here is
// invented: a few points near 10°N 20°E.
func cachedStrava(t *testing.T) {
	t.Helper()
	config, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv(fs.EnvConfig, "")
	t.Setenv(fs.EnvClientID, "4242")
	t.Setenv(fs.EnvClientSecret, "invented")
	if err := fs.SaveToken(filepath.Join(config, "strava-token.json"), fs.Token{
		ClientID: "4242", AthleteID: 77, AccessToken: "a", RefreshToken: "r",
		ExpiresAt: time.Now().Add(6 * time.Hour), Scope: "read,activity:read_all",
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, "fitactivity", "strava", "77")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	answer := `{"id":1001,"name":"Invented Run","sport_type":"Run","start_date":"2026-04-02T06:00:00Z",
		"streams":[{"type":"time","data":[0,1,2,3]},{"type":"latlng","data":[[10,20],[10.0001,20],[10.0002,20],[10.0003,20]]},
		{"type":"distance","data":[0,11,22,33]},{"type":"heartrate","data":[120,121,122,123]}]}`
	if err := os.WriteFile(filepath.Join(dir, "1001.streams.json"), []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stravaCommand() *cobra.Command {
	r := &cobra.Command{Use: "fitdash"}
	cmd := &cobra.Command{Use: "inspect"}
	r.AddCommand(cmd)
	addStrava(r)
	return cmd
}

// An activity on Strava is read as a file is, its title kept, and named
// for the files written after it without the colon its source has.
func TestReadActivity_FromStrava(t *testing.T) {
	cachedStrava(t)
	track, sources, err := decodeActivitiesWith(readActivity(stravaCommand()), []string{"strava:1001"})
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Invented Run" || len(track.Samples) != 4 || len(sources) != 1 || sources[0].Path != "strava:1001" {
		t.Errorf("track %q, %d samples, sources %+v", track.Title, len(track.Samples), sources)
	}
	// Strava records no speed; it is worked out from the distance, and
	// said so, as for any file without one.
	if !sources[0].DerivedSpeed {
		t.Error("speed not derived from the distance")
	}
	if got := plannedOutputPath("strava:1001", "", "out", ".mp4"); got != filepath.Join("out", "strava-1001.mp4") {
		t.Errorf("output %q", got)
	}

	var b bytes.Buffer
	writeSummary(&b, inspect.Build(track), sources, track)
	for _, want := range []string{"title    Invented Run", "strava   https://www.strava.com/activities/1001",
		"active   (Strava keeps no timer events)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("inspect's header lacks %q:\n%s", want, b.String())
		}
	}
}

// A file alone never asks Strava, so needs no application set up; asked
// to, without one, it says how to set one up.
func TestReadActivity_AFileNeverAsksStrava(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(fs.EnvConfig, "")
	t.Setenv(fs.EnvClientID, "")
	t.Setenv(fs.EnvClientSecret, "")
	path := filepath.Join(t.TempDir(), "run.fit")
	if err := fittest.WriteFile(path, fittest.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	// Registering --strava sets it to its default, so the command is
	// made before the flag is set, as parsing does.
	cmd := stravaCommand()
	defer func() { stravaAttach = false }()
	if _, _, err := decodeActivitiesWith(readActivity(cmd), []string{path}); err != nil {
		t.Fatalf("a file alone: %v", err)
	}
	stravaAttach = true
	_, _, err := decodeActivitiesWith(readActivity(cmd), []string{path})
	if err == nil || !strings.Contains(err.Error(), "no Strava API application") {
		t.Errorf("--strava without an application: %v", err)
	}
}

// A card is titled with the activity's name on Strava over what the
// activity is.
func TestSummaryCardTitledFromStrava(t *testing.T) {
	cachedStrava(t)
	track, _, err := decodeActivitiesWith(readActivity(stravaCommand()), []string{"strava:1001"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := units.Of(units.Metric)
	card := summaryCard(track, fitactivity.BuildTimerModel(track), panel.BuildElevation(track, fitactivity.ElevationOptions{}), u, panel.SpeedReadoutPace, false, time.Time{})
	if card.Title != "Invented Run" {
		t.Errorf("card titled %q", card.Title)
	}
}
