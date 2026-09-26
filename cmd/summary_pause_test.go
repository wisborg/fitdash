package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
	"github.com/wisborg/fitdash/internal/panel"
)

// A highlight starting inside a pause is marked on the elevation profile, so
// the summary does not say it is not. It said so for both highlights of a
// merged five-file run whose bounds fell between two files -- the natural
// place to put one -- because the check and the panel both refused any
// bound deep in a recording gap, pause or not. The summary reads the same
// TimeToDistance the panel does, with the same timer model, so the two
// cannot disagree; without the timer the old line comes back, which is
// checked so this cannot pass for another reason.
func TestWriteHighlightSummary_AHighlightStartingInAPauseIsMarked(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	opts := fittest.DefaultOptions()
	opts.Count = 900
	opts.Pauses = []fittest.Pause{{Start: 300 * time.Second, End: 480 * time.Second}}
	path := filepath.Join(t.TempDir(), "paused.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	timer := fitactivity.BuildTimerModel(track)
	start := track.Samples[0].Time

	highlights := []panel.Highlight{{Name: "Between files", From: 400 * time.Second, To: 700 * time.Second}}
	tl, err := panel.NewSegmentedTimeline(start, 900*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatal(err)
	}
	summary := func(timer *fitactivity.TimerModel) string {
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)
		writeHighlightSummary(c, tl, track, timer, highlights, panel.Smoothing{}, panel.DefaultTheme(), true)
		return buf.String()
	}

	const unmarked = `highlight "Between files" has no distance at its bounds`
	if !strings.Contains(summary(nil), unmarked) {
		t.Fatal("precondition: without the timer the highlight was not reported unmarked, so this proves nothing")
	}
	if out := summary(timer); strings.Contains(out, unmarked) {
		t.Errorf("a highlight starting inside a pause was reported unmarked:\n%s", out)
	}
}
