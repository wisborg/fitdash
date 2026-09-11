package cmd

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/render"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// TestParseSize_ReadsWxHAndRefusesTheRest keeps a malformed --size from
// reaching the encoder, which never sees the string and so cannot diagnose it.
func TestParseSize_ReadsWxHAndRefusesTheRest(t *testing.T) {
	cases := []struct {
		in      string
		w, h    int
		wantErr bool
	}{
		{"1920x1080", 1920, 1080, false},
		{"640x360", 640, 360, false},
		{"1080X1920", 1080, 1920, false}, // an upper-case X is the same request
		{"  1280x720 ", 1280, 720, false},
		// Odd dimensions parse here and are refused by the encoder, which owns
		// the rule and knows why it exists. Repeating the check would be a
		// second copy of a rule with one owner.
		{"1919x1080", 1919, 1080, false},
		{"1920", 0, 0, true},
		{"1920x", 0, 0, true},
		{"x1080", 0, 0, true},
		{"widexhigh", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			w, h, err := parseSize(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("parseSize(%q) = %d,%d; want an error", c.in, w, h)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSize(%q): %v", c.in, err)
			}
			if w != c.w || h != c.h {
				t.Errorf("parseSize(%q) = %d,%d, want %d,%d", c.in, w, h, c.w, c.h)
			}
		})
	}
}

// TestOutputPath_DerivesFromTheActivityAndRefusesToClobber pins both halves.
//
// Refusing an existing file rather than overwriting matters because a render
// takes minutes: silently replacing one is a bad trade for a flag the user
// could have passed in a second, and ffmpeg's own -y would clobber without
// asking. The message has to name the flag that resolves it, or the user is
// left guessing.
//
// The sample filename's date is the 2020-01-02 03:04:05 placeholder every
// other fixture in this repo counts off, and it is deliberately not a date
// any real recording carries. A watch names its files after the moment they
// were recorded, so a realistic-looking date in a test is a real activity's
// date -- and CLAUDE.md puts dates alongside coordinates in what must never
// enter a commit. What this test needs from the name is a space in it and a
// .fit extension to strip; when it happened is not part of the assertion.
func TestOutputPath_DerivesFromTheActivityAndRefusesToClobber(t *testing.T) {
	dir := t.TempDir()

	got, err := outputPath("/some/where/2020-01-02 Morning Run.fit", "", dir, ".mp4")
	if err != nil {
		t.Fatalf("outputPath: %v", err)
	}
	if want := filepath.Join(dir, "2020-01-02 Morning Run.mp4"); got != want {
		t.Errorf("outputPath = %q, want %q", got, want)
	}

	// An explicit -o wins outright.
	explicit := filepath.Join(dir, "elsewhere.mp4")
	if got, err := outputPath("a.fit", explicit, dir, ".mp4"); err != nil || got != explicit {
		t.Errorf("outputPath with -o = %q, %v; want %q", got, err, explicit)
	}

	// And an existing file is refused, with the flag named.
	if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = outputPath("/some/where/2020-01-02 Morning Run.fit", "", dir, ".mp4")
	if err == nil {
		t.Fatal("outputPath overwrote an existing render")
	}
	if !strings.Contains(err.Error(), "-o") {
		t.Errorf("the error should name the flag that resolves it; got: %v", err)
	}
}

// TestFrameIndices_CoversTheBoundariesAndHonoursFrameAt pins which frames
// --frames writes.
//
// Frame 0 and the last frame are there deliberately: panels that accumulate --
// a progress bar, a covered route, a splits list -- are wrong at the
// boundaries far more often than in the middle, and frame 0 hits every
// "nothing has happened yet" branch at once.
func TestFrameIndices_CoversTheBoundariesAndHonoursFrameAt(t *testing.T) {
	tl, err := panel.NewTimeline(time.Now(), 100*time.Second, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	n := tl.Frames() // 3000

	got, err := frameIndices(tl, tl.Frames()-1, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	want := []int{0, n / 4, n / 2, 3 * n / 4, n - 1}
	if len(got) != len(want) {
		t.Fatalf("frameIndices = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frameIndices[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	if got[0] != 0 {
		t.Error("frame 0 must be included; it hits every 'nothing has happened yet' branch")
	}
	if got[len(got)-1] != n-1 {
		t.Error("the last frame must be included")
	}

	// --frame-at resolves through the timeline, so it is the same arithmetic
	// the render itself uses. 50 seconds at 30 fps is frame 1500.
	got, err = frameIndices(tl, tl.Frames()-1, []time.Duration{50 * time.Second}, nil, nil, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	if last := got[len(got)-1]; last != 1500 {
		t.Errorf("--frame-at 50s resolved to frame %d, want 1500", last)
	}
}

// TestFrameIndices_HonoursFrameAtVideo pins that --frame-at-video is read
// against the VIDEO's own clock rather than the activity's: at a speedup of
// 10, five seconds of video is fifty seconds of activity, so the two flags
// must disagree on which frame that names.
func TestFrameIndices_HonoursFrameAtVideo(t *testing.T) {
	tl, err := panel.NewTimeline(time.Now(), 100*time.Second, 30, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 5s of VIDEO at 30fps is frame 150 -- and, at a 10x speedup, a
	// completely different frame from what --frame-at 5s would have named
	// (--frame-at 5s of ACTIVITY time is frame 15).
	got, err := frameIndices(tl, tl.Frames()-1, nil, []time.Duration{5 * time.Second}, nil, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	if last := got[len(got)-1]; last != 150 {
		t.Errorf("--frame-at-video 5s resolved to frame %d, want 150", last)
	}

	if _, err := frameIndices(tl, tl.Frames()-1, nil, []time.Duration{-time.Second}, nil, nil); err == nil {
		t.Error("frameIndices accepted a negative --frame-at-video offset")
	}
	if _, err := frameIndices(tl, tl.Frames()-1, nil, []time.Duration{11 * time.Second}, nil, nil); err == nil {
		t.Error("frameIndices accepted a --frame-at-video offset past the end of the video")
	} else if !strings.Contains(err.Error(), "0:00:10") {
		t.Errorf("the error should say how long the video runs; got: %v", err)
	}
	// The video's own last instant is a legitimate thing to ask for.
	if _, err := frameIndices(tl, tl.Frames()-1, nil, []time.Duration{10 * time.Second}, nil, nil); err != nil {
		t.Errorf("frameIndices rejected the video's final instant: %v", err)
	}
}

// TestFrameIndices_IncludesEveryHighlightsFirstAndLastFrame pins the
// landmark this feature adds: a highlight's own boundaries, unconditionally,
// because that is exactly where a panel's transition gets it wrong and the
// ordinary quarter-marks will not land near it on a long render.
func TestFrameIndices_IncludesEveryHighlightsFirstAndLastFrame(t *testing.T) {
	tl, err := panel.NewSegmentedTimeline(time.Now(), 100*time.Second, 30, 1, []panel.Highlight{
		{From: 20 * time.Second, To: 30 * time.Second, RateFactor: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := frameIndices(tl, tl.Frames()-1, nil, nil, []panel.Highlight{
		{From: 20 * time.Second, To: 30 * time.Second, RateFactor: 5},
	}, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	first := tl.IndexAt(20 * time.Second)
	last := tl.IndexAt(30*time.Second - time.Nanosecond)
	var sawFirst, sawLast bool
	for _, i := range got {
		if i == first {
			sawFirst = true
		}
		if i == last {
			sawLast = true
		}
	}
	if !sawFirst || !sawLast {
		t.Errorf("frameIndices = %v, want the highlight's first frame %d and last frame %d among them", got, first, last)
	}
}

// TestFrameIndices_RejectsAnOffsetPastTheActivity pins a failure that used to
// be a silent wrong answer.
//
// Timeline.IndexAt clamps, which is right for its own callers and wrong here:
// clamping made --frame-at 40m on a 25-minute activity write the FINAL frame
// and exit 0, handing the user a picture of 25:53 while they believed they
// were looking at 40:00. The sink's unreached-frame error could never fire,
// because clamping had made the index reachable.
func TestFrameIndices_RejectsAnOffsetPastTheActivity(t *testing.T) {
	tl, err := panel.NewTimeline(time.Now(), 25*time.Minute, 30, 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := frameIndices(tl, tl.Frames()-1, []time.Duration{40 * time.Minute}, nil, nil, nil); err == nil {
		t.Fatal("frameIndices accepted an offset past the end of the activity")
	} else if !strings.Contains(err.Error(), "0:25:00") {
		t.Errorf("the error should say how long the activity runs; got: %v", err)
	}
	if _, err := frameIndices(tl, tl.Frames()-1, []time.Duration{-time.Minute}, nil, nil, nil); err == nil {
		t.Error("frameIndices accepted a negative offset")
	}

	// The boundary is inclusive: the very last instant of the activity is a
	// legitimate thing to ask for.
	if _, err := frameIndices(tl, tl.Frames()-1, []time.Duration{25 * time.Minute}, nil, nil, nil); err != nil {
		t.Errorf("frameIndices rejected the activity's final instant: %v", err)
	}

	// And the offset is ACTIVITY time, so a sped-up render accepts offsets far
	// beyond the video's own length.
	fast, err := panel.NewTimeline(time.Now(), 25*time.Minute, 30, 25)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := frameIndices(fast, fast.Frames()-1, []time.Duration{20 * time.Minute}, nil, nil, nil); err != nil {
		t.Errorf("a 20m offset was rejected on a 25m activity rendered as a 1m video: %v", err)
	}
}

// TestNewProgress_QuietReturnsNils pins how --quiet is implemented.
//
// A nil *progress.Display and a nil *progress.Bar are both usable and do
// nothing, so quiet is one decision here rather than a condition at every
// call site -- and the render loop's per-frame callback stays unconditional.
func TestNewProgress_QuietReturnsNils(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)

	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)

	renderOpts.quiet = false
	if d, bar := newProgress(cmd, 100); d == nil || bar == nil {
		t.Error("a display and a bar were expected without --quiet")
	}

	renderOpts.quiet = true
	d, bar := newProgress(cmd, 100)
	if d != nil || bar != nil {
		t.Error("--quiet should produce no display and no bar")
	}
	// Both nils must be usable, or every call site needs a guard.
	bar.Set(1)
	d.Stop()
}

// TestNewProgress_WritesNoEscapeSequencesToANonTerminal keeps a redirected
// stderr from collecting cursor movements.
//
// The decision now belongs to the display rather than to fitdash, which is
// the point of the change: this program's own version tested for a character
// device, and /dev/null is one -- so `fitdash 2>/dev/null` took the inline
// path. The assertion is kept here anyway, because it is fitdash's output
// that would be wrong.
func TestNewProgress_WritesNoEscapeSequencesToANonTerminal(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&buf)
	d, bar := newProgress(cmd, 10)
	if d == nil || bar == nil {
		t.Fatal("no display")
	}
	if d.Live() {
		t.Error("a bytes.Buffer was taken for a terminal")
	}
	bar.Set(0)
	bar.Set(5)
	d.Stop()
	if strings.ContainsAny(buf.String(), "\r\x1b") {
		t.Errorf("progress to a non-terminal used terminal control characters: %q", buf.String())
	}
}

// TestParsePowerSource_MatchesVideofxsVocabulary pins the three spellings.
//
// They are pinned as literals on purpose. The point of this flag is that it
// reads the same as videofx's -- same name, same values, same meanings -- for
// a user moving between two programs that read the same files through the same
// library. A rename here would break that quietly, since both would still
// work; only the consistency would be gone.
func TestParsePowerSource_MatchesVideofxsVocabulary(t *testing.T) {
	cases := []struct {
		in   string
		want fitactivity.PowerSource
	}{
		{"auto", fitactivity.PowerAuto},
		{"stryd", fitactivity.PowerStryd},
		{"native", fitactivity.PowerNative},
	}
	for _, c := range cases {
		got, err := parsePowerSource(c.in)
		if err != nil {
			t.Errorf("parsePowerSource(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePowerSource(%q) = %v, want %v", c.in, got, c.want)
		}
		// And the enum spells itself the same way back, which is what lets a
		// diagnostic name the source the user asked for.
		if got.String() != c.in {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), c.in)
		}
	}

	// An unknown value is refused where it was typed rather than falling back.
	// A silent fallback would render the activity against a different sensor
	// after a typo, and the two disagree by enough to be mistaken for a bad
	// workout rather than a bad flag.
	for _, bad := range []string{"", "Stryd", "garmin", "footpod", "none"} {
		if _, err := parsePowerSource(bad); err == nil {
			t.Errorf("parsePowerSource(%q) was accepted", bad)
		}
	}
}

// TestResolveSpeedup_TakesEitherFlagButNotBoth pins the CLI's half of the
// feature.
//
// The two flags express the same intention in opposite directions, so they are
// mutually exclusive rather than one overriding the other. Silently preferring
// whichever the code happens to check first would give a user who passed both
// a video of a length they did not ask for, with nothing saying which flag won.
//
// The flags are driven through a real cobra command and Set, rather than by
// assigning the option struct directly, because "was this flag given" is a
// question only cobra can answer. An earlier version compared against the
// default value instead, and `--speedup 1 --video-duration 3m` slipped through
// the exclusivity check -- a legal spelling of exactly the thing it forbids.
func TestResolveSpeedup_TakesEitherFlagButNotBoth(t *testing.T) {
	// A one-hour activity.
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: start}, {Time: start.Add(time.Hour)},
	}}
	timer := fitactivity.BuildTimerModel(track)

	cases := []struct {
		name    string
		set     map[string]string
		want    float64
		wantErr string
	}{
		{"neither: real time", nil, 1, ""},
		{"a factor", map[string]string{"speedup": "12"}, 12, ""},
		{"a target duration", map[string]string{"video-duration": "1m"}, 60, ""},
		{"a target longer than the activity", map[string]string{"video-duration": "2h"}, 0.5, ""},
		{"both", map[string]string{"speedup": "10", "video-duration": "1m"}, 0, "both set"},
		// The spelling that used to slip through: a speedup EQUAL to the
		// default is still a speedup the user typed.
		{"both, with the factor at its default", map[string]string{"speedup": "1", "video-duration": "3m"}, 0, "both set"},
		{"a negative factor", map[string]string{"speedup": "-3"}, 0, "must be positive"},
		{"a negative duration", map[string]string{"video-duration": "-1m"}, 0, "must be positive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func(s float64, d time.Duration) {
				renderOpts.speedup, renderOpts.videoDur = s, d
			}(renderOpts.speedup, renderOpts.videoDur)

			cmd := &cobra.Command{Use: "test"}
			bindRenderFlags(cmd)
			for k, v := range c.set {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatalf("setting --%s=%s: %v", k, v, err)
				}
			}

			got, err := resolveSpeedup(cmd, timer, panel.PausesFreeze)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveSpeedup = %v, want an error mentioning %q", got, c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error %v does not mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSpeedup: %v", err)
			}
			if got != c.want {
				t.Errorf("resolveSpeedup = %v, want %v", got, c.want)
			}
		})
	}
}

// TestResolveElevationTuning_FourLevelPrecedenceAndItsSourceLabel pins both
// halves of resolveElevationTuning's own contract: the fitactivity.ElevationOptions
// it resolves to, AND the source label it attaches, at each of the four
// documented precedence levels.
//
// The label half is the one a pixel-comparing render test cannot catch. The
// third level (the file's own totals) and the fourth (the library default)
// both resolve their fitactivity.ElevationOptions by calling
// panel.DefaultElevationTuning(track) -- which internally re-checks the
// identical track.HasElevationTotals condition the switch below already
// branched on -- so for a GIVEN track the returned Options are always
// whatever that one function returns, regardless of which case of the switch
// "chose" it. Swapping elevationTuningSourceFile and elevationTuningSourceDefault
// between those two branches would not change a single byte any render
// produces: only the printed label would lie. Asserting the label here,
// beside the options, is what makes that swap a test failure instead of an
// invisible one -- see this test's own "beats-the-label" subtests below,
// which is where a source-label swap would actually be caught.
//
// The gain/loss level is checked in BOTH directions -- gain alone, then loss
// alone -- because the flags' own help text promises "either one" (an OR):
// a guard written as "gain > 0 && loss > 0", or one copy-pasted to check
// gain twice, would still pass a test that only ever set both fields
// together, or only ever set gain.
//
// The nil-track case exercises resolveElevationTuning's own doc comment,
// which explicitly claims a nil track "simply never matches" the file-totals
// case rather than panicking on track.HasElevationTotals -- a claim with no
// test of its own before this one.
func TestResolveElevationTuning_FourLevelPrecedenceAndItsSourceLabel(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)

	// TotalAscent/TotalDescent chosen independently of DefaultElevationTuning's
	// own arithmetic (TargetGain = TotalAscent, TargetLoss = TotalDescent) --
	// they are what that arithmetic is checked against below, not copied from it.
	trackWithTotals := &fitactivity.Track{HasElevationTotals: true, TotalAscent: 120, TotalDescent: 80}
	trackWithoutTotals := &fitactivity.Track{}

	cases := []struct {
		name       string
		smoothing  float64
		gain, loss float64
		track      *fitactivity.Track
		wantOpts   fitactivity.ElevationOptions
		wantSource string
	}{
		{
			name:       "an explicit --elevation-smoothing wins outright",
			smoothing:  12,
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{Sigma: 12},
			wantSource: elevationTuningSourceExplicit,
		},
		{
			name:      "an explicit --elevation-smoothing beats --elevation-gain/-loss too",
			smoothing: 12, gain: 500, loss: 500,
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{Sigma: 12},
			wantSource: elevationTuningSourceExplicit,
		},
		{
			// gain alone -- one direction of the documented "either one".
			name:       "--elevation-gain alone sets the targets",
			gain:       300,
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{TargetGain: 300, TargetLoss: 0},
			wantSource: elevationTuningSourceTargets,
		},
		{
			// loss alone -- the OTHER direction. A guard checking gain twice,
			// or an "&&" where the help text promises "either one", would
			// pass the case above and fail only this one.
			name:       "--elevation-loss alone sets the targets",
			loss:       150,
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{TargetGain: 0, TargetLoss: 150},
			wantSource: elevationTuningSourceTargets,
		},
		{
			name: "--elevation-gain/-loss together beat the file's own totals",
			gain: 300, loss: 200,
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{TargetGain: 300, TargetLoss: 200},
			wantSource: elevationTuningSourceTargets,
		},
		{
			// Level 3: derived independently from trackWithTotals' own fields
			// above, not from DefaultElevationTuning's implementation.
			name:       "the file's own totals, with no flag typed",
			track:      trackWithTotals,
			wantOpts:   fitactivity.ElevationOptions{TargetGain: 120, TargetLoss: 80},
			wantSource: elevationTuningSourceFile,
		},
		{
			// Level 4: the file carries no totals, so this falls all the way
			// through to the library's own untuned default -- the zero value,
			// since Sigma <= 0 there means "auto" to the library itself.
			name:       "the library default when the file carries no totals",
			track:      trackWithoutTotals,
			wantOpts:   fitactivity.ElevationOptions{},
			wantSource: elevationTuningSourceDefault,
		},
		{
			// The function's own doc comment claims a nil track falls
			// through to the default rather than dereferencing
			// track.HasElevationTotals; this is that claim's only test.
			name:       "a nil track also falls through to the default",
			track:      nil,
			wantOpts:   fitactivity.ElevationOptions{},
			wantSource: elevationTuningSourceDefault,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			renderOpts = renderOptions{
				elevationSmoothing: c.smoothing,
				elevationGain:      c.gain,
				elevationLoss:      c.loss,
			}
			gotOpts, gotSource := resolveElevationTuning(c.track)
			if gotOpts != c.wantOpts {
				t.Errorf("resolveElevationTuning(...) options = %+v, want %+v", gotOpts, c.wantOpts)
			}
			if gotSource != c.wantSource {
				t.Errorf("resolveElevationTuning(...) source = %q, want %q", gotSource, c.wantSource)
			}
		})
	}
}

// elevationSummaryTrack builds a track with enough (distance, elevation)
// samples for fitactivity.BuildElevationModel to produce a non-empty model --
// synthetic data with no bearing on any real recording, since only its
// length and monotone distance matter to the tests below, not its shape.
func elevationSummaryTrack() *fitactivity.Track {
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 10)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time:         start.Add(time.Duration(i) * time.Second),
			HasDistance:  true,
			Distance:     float64(i) * 10,
			HasElevation: true,
			Elevation:    float64(i),
		}
	}
	return &fitactivity.Track{Samples: samples}
}

// TestWriteElevationSummary_ReportsSigmaAndSourceOrNothingAtAll pins the one
// summary line resolveElevationTuning's own resolution feeds -- the sigma
// BuildElevation actually used, and which of the four precedence levels
// produced it -- and the three cases that must print NOTHING at all, the
// same discipline writeHighlightSummary and writeLabelSummary already apply
// to their own flags: an ordinary render without a reason to speak stays
// silent rather than growing a line about a feature that never engaged.
func TestWriteElevationSummary_ReportsSigmaAndSourceOrNothingAtAll(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)

	track := elevationSummaryTrack()
	// Sigma fixed by an explicit ElevationOptions rather than left to the
	// library's own auto-tuning, so the expected text below is independent
	// of whatever sigma the tuning search happens to land on.
	m := fitactivity.BuildElevationModel(track, fitactivity.ElevationOptions{Sigma: 3})
	if m.Empty() {
		t.Fatal("precondition: the fixture track must produce a non-empty elevation model")
	}

	for _, source := range []string{
		elevationTuningSourceExplicit,
		elevationTuningSourceTargets,
		elevationTuningSourceFile,
		elevationTuningSourceDefault,
	} {
		t.Run(source, func(t *testing.T) {
			renderOpts.quiet = false
			var buf bytes.Buffer
			c := &cobra.Command{}
			c.SetErr(&buf)

			writeElevationSummary(c, m, source)

			want := fmt.Sprintf("elevation: smoothing sigma %.1f samples, %s\n", m.Sigma(), source)
			if got := buf.String(); got != want {
				t.Errorf("writeElevationSummary(source=%q) = %q, want %q", source, got, want)
			}
		})
	}

	t.Run("nothing under --quiet", func(t *testing.T) {
		renderOpts.quiet = true
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)

		writeElevationSummary(c, m, elevationTuningSourceFile)

		if got := buf.String(); got != "" {
			t.Errorf("writeElevationSummary under --quiet printed %q, want nothing", got)
		}
	})

	t.Run("nothing with no model", func(t *testing.T) {
		renderOpts.quiet = false
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)

		writeElevationSummary(c, nil, elevationTuningSourceFile)

		if got := buf.String(); got != "" {
			t.Errorf("writeElevationSummary(nil model) printed %q, want nothing", got)
		}
	})

	t.Run("nothing with an empty model", func(t *testing.T) {
		renderOpts.quiet = false
		empty := fitactivity.BuildElevationModel(&fitactivity.Track{}, fitactivity.ElevationOptions{})
		if !empty.Empty() {
			t.Fatal("precondition: an empty track must produce an Empty() elevation model")
		}
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)

		writeElevationSummary(c, empty, elevationTuningSourceDefault)

		if got := buf.String(); got != "" {
			t.Errorf("writeElevationSummary(empty model) printed %q, want nothing", got)
		}
	})
}

// TestSpeedupNote_IsSilentAtRealTime keeps the summary from drawing attention
// to a fact the two equal durations beside it already state.
func TestSpeedupNote_IsSilentAtRealTime(t *testing.T) {
	if got := speedupNote(1); got != "" {
		t.Errorf("speedupNote(1) = %q, want nothing", got)
	}
	for _, c := range []struct {
		in   float64
		want string
	}{
		{60, " (60x)"},
		{2.5, " (2.5x)"},
		{0.5, " (0.5x)"},
		// A target duration rarely divides evenly: three minutes of a
		// four-hour activity is 79.99444444444444, and printing all of that
		// is not more accurate, only harder to read.
		{79.99444444444444, " (79.99x)"},
		{10.004, " (10x)"},
	} {
		if got := speedupNote(c.in); got != c.want {
			t.Errorf("speedupNote(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFormatMultiplier_RoundsAndTrims pins the shared rounding rule
// speedupNote and the highlight table both spell a compression factor with.
func TestFormatMultiplier_RoundsAndTrims(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{60, "60"},
		{2.5, "2.5"},
		{0.5, "0.5"},
		// A target duration rarely divides evenly: three minutes of a
		// four-hour activity is 79.99444444444444, and printing all of that
		// is not more accurate, only harder to read.
		{79.99444444444444, "79.99"},
		{10.004, "10"},
	} {
		if got := formatMultiplier(c.in); got != c.want {
			t.Errorf("formatMultiplier(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBaseSpeedupNote_LabelsTheBaseOnlyWhenHighlightsExist pins the one
// wording change highlights force onto the render summary's first line: a
// bare "(60x)" beside a video running at more than one rate would be exactly
// the confident lie speedupNote's own doc comment describes avoiding at real
// time, so once any highlight is configured the figure is named "base"
// explicitly -- even at a base of 1x, because a highlight can still run its
// own rate while the rest of the render is real time.
func TestBaseSpeedupNote_LabelsTheBaseOnlyWhenHighlightsExist(t *testing.T) {
	if got := baseSpeedupNote(1, false); got != "" {
		t.Errorf("baseSpeedupNote(1, false) = %q, want nothing -- identical to speedupNote at real time with no highlights", got)
	}
	if got, want := baseSpeedupNote(60, false), " (60x)"; got != want {
		t.Errorf("baseSpeedupNote(60, false) = %q, want %q", got, want)
	}
	if got, want := baseSpeedupNote(1, true), " (base 1x)"; got != want {
		t.Errorf("baseSpeedupNote(1, true) = %q, want %q -- a highlight can run its own rate even while the base is real time", got, want)
	}
	if got, want := baseSpeedupNote(60, true), " (base 60x)"; got != want {
		t.Errorf("baseSpeedupNote(60, true) = %q, want %q", got, want)
	}
}

// TestValidateRenderOptions_RejectsFlagsThatCannotMeanWhatTheySay covers
// several review findings, every one a case of a flag quietly doing
// something other than what its help text promised. The three
// --elevation-smoothing/-gain/-loss cases were added on the identical model
// as the --crf 0 case above them -- a negative value there means "auto"
// inside fitactivity's own ElevationOptions (see resolveElevationTuning),
// exactly the same class of defect as 0 silently meaning "unset" for --crf --
// but had no case of their own until now, so a copy-paste that dropped the
// "< 0" guard on any one of the three, or wrote it against the wrong field,
// would have passed every existing test. Zero itself is also pinned as
// ACCEPTED, not merely left untested by omission: the flags' own help text
// documents zero as meaning automatic, so a future change that tightened
// the guard to "<= 0" would silently start rejecting the documented default.
func TestValidateRenderOptions_RejectsFlagsThatCannotMeanWhatTheySay(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)

	cases := []struct {
		name    string
		set     func()
		wantErr string
	}{
		{
			// -o was reinterpreted as a directory under --frames, so
			// `-o preview.png --frames` created a DIRECTORY named preview.png
			// with frame-000000.png inside it.
			name:    "-o with --frames",
			set:     func() { renderOpts.frames, renderOpts.output, renderOpts.crf = true, "preview.png", 20 },
			wantErr: "single file",
		},
		{
			// 0 is x264's lossless and this program's "unset"; it cannot be
			// both, and it silently became the default quality.
			name:    "--crf 0",
			set:     func() { renderOpts.frames, renderOpts.output, renderOpts.crf = false, "", 0 },
			wantErr: "lossless",
		},
		{
			name: "-o without --frames is fine",
			set:  func() { renderOpts.frames, renderOpts.output, renderOpts.crf = false, "out.mp4", 20 },
		},
		{
			name: "--frames without -o is fine",
			set:  func() { renderOpts.frames, renderOpts.output, renderOpts.crf = true, "", 20 },
		},
		{
			// 1 is the near-lossless the error message points at, and must be
			// accepted or the advice is wrong.
			name: "--crf 1 is accepted",
			set:  func() { renderOpts.frames, renderOpts.output, renderOpts.crf = false, "", 1 },
		},
		{
			// The error must name THIS flag, not merely say "negative" -- a
			// guard copy-pasted from one of its two neighbours and left
			// checking the wrong field would still say "negative" but about
			// the wrong flag, and a looser assertion would not catch it.
			name:    "--elevation-smoothing negative",
			set:     func() { renderOpts.crf, renderOpts.elevationSmoothing = 20, -1 },
			wantErr: "--elevation-smoothing",
		},
		{
			name:    "--elevation-gain negative",
			set:     func() { renderOpts.crf, renderOpts.elevationGain = 20, -1 },
			wantErr: "--elevation-gain",
		},
		{
			name:    "--elevation-loss negative",
			set:     func() { renderOpts.crf, renderOpts.elevationLoss = 20, -1 },
			wantErr: "--elevation-loss",
		},
		{
			// Zero means "automatic" for all three (documented in the flags'
			// own help text), not an error -- the fresh renderOptions every
			// other case above already starts from, made explicit here so it
			// is pinned rather than merely assumed.
			name: "--elevation-smoothing/-gain/-loss at zero (automatic) is fine",
			set:  func() { renderOpts.crf = 20 },
		},
		{
			// --basemap-dim is a wash fraction, meaningful only inside
			// [0, 1] -- 0 is full-strength imagery, 1 is invisible, and
			// anything outside that range is not a fraction of anything.
			name:    "--basemap-dim below 0",
			set:     func() { renderOpts.crf, renderOpts.basemapDim = 20, -0.1 },
			wantErr: "--basemap-dim",
		},
		{
			name:    "--basemap-dim above 1",
			set:     func() { renderOpts.crf, renderOpts.basemapDim = 20, 1.1 },
			wantErr: "--basemap-dim",
		},
		{
			// Both ends of the range are valid, not merely tolerated: 0 is
			// full-strength imagery and 1 is a basemap washed out entirely,
			// and either could be a deliberate choice.
			name: "--basemap-dim at 0 is fine",
			set:  func() { renderOpts.crf, renderOpts.basemapDim = 20, 0 },
		},
		{
			name: "--basemap-dim at 1 is fine",
			set:  func() { renderOpts.crf, renderOpts.basemapDim = 20, 1 },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			renderOpts = renderOptions{}
			c.set()
			err := validateRenderOptions()
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("validateRenderOptions = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateRenderOptions accepted it; want an error mentioning %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %v does not mention %q", err, c.wantErr)
			}
		})
	}
}

// --- --basemap wiring --------------------------------------------------

// resolveBasemapTestKeyFile writes a bare Thunderforest key to a fresh file
// with safe permissions, the shape resolveBasemap's own happy path needs.
func resolveBasemapTestKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("aabbccdd11223344aabbccdd11223344"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveBasemap_NoStyleFetchesNothing pins the off-by-default case:
// --basemap unset, or explicitly "none", must resolve to a nil provider and
// print nothing -- a render that never asked for imagery must not even hint
// that it considered it.
func TestResolveBasemap_NoStyleFetchesNothing(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	for _, style := range []string{"", basemapOff} {
		t.Run("style "+style, func(t *testing.T) {
			renderOpts = renderOptions{basemap: style}
			var buf bytes.Buffer
			c := &cobra.Command{}
			c.SetErr(&buf)
			p, err := resolveBasemap(c)
			if err != nil {
				t.Fatalf("resolveBasemap: %v", err)
			}
			if p != nil {
				t.Errorf("resolveBasemap returned a provider with --basemap %q", style)
			}
			if buf.Len() != 0 {
				t.Errorf("resolveBasemap printed %q with no --basemap given", buf.String())
			}
		})
	}
}

// TestResolveBasemap_KeyFileWithNoStyleWarns is the flag combination that
// looks like a mistake rather than a deliberate no-op: a key file was
// clearly meant for something. Said rather than silently ignored, or the
// user is left wondering why the render has no map.
func TestResolveBasemap_KeyFileWithNoStyleWarns(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{basemapKeyFile: resolveBasemapTestKeyFile(t)}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	p, err := resolveBasemap(c)
	if err != nil {
		t.Fatalf("resolveBasemap: %v", err)
	}
	if p != nil {
		t.Error("resolveBasemap returned a provider with no --basemap style given")
	}
	if !strings.Contains(buf.String(), "--basemap-key-file given without --basemap") {
		t.Errorf("no warning about the orphaned key file; got %q", buf.String())
	}
}

// TestResolveBasemap_RefusesAnUnknownStyle keeps a typo from silently
// falling through to "no basemap" the way an unrecognised --theme or
// --layout does not either.
func TestResolveBasemap_RefusesAnUnknownStyle(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{basemap: "satellite-3d", basemapKeyFile: resolveBasemapTestKeyFile(t)}

	c := &cobra.Command{}
	c.SetErr(&bytes.Buffer{})
	_, err := resolveBasemap(c)
	if err == nil {
		t.Fatal("an unknown --basemap style was accepted")
	}
	if !strings.Contains(err.Error(), "satellite-3d") || !strings.Contains(err.Error(), "outdoors") {
		t.Errorf("error does not name the bad value or a valid one: %v", err)
	}
}

// TestResolveBasemap_RequiresAKeyFile pins that fitdash never fetches
// imagery under a key of its own: a style with no --basemap-key-file must
// refuse rather than silently using some default.
func TestResolveBasemap_RequiresAKeyFile(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{basemap: "outdoors"}

	c := &cobra.Command{}
	c.SetErr(&bytes.Buffer{})
	_, err := resolveBasemap(c)
	if err == nil {
		t.Fatal("--basemap with no --basemap-key-file was accepted")
	}
	if !strings.Contains(err.Error(), "--basemap-key-file") {
		t.Errorf("error does not name the missing flag: %v", err)
	}
}

// TestResolveBasemap_ValidStyleAndKeyResolveAThunderforestProvider is the
// happy path: a real style and a real key file must resolve to a working
// Thunderforest provider carrying that exact style, uncached when
// --basemap-cache=off.
func TestResolveBasemap_ValidStyleAndKeyResolveAThunderforestProvider(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{
		basemap: "landscape", basemapKeyFile: resolveBasemapTestKeyFile(t), basemapCache: "off",
	}

	c := &cobra.Command{}
	var buf bytes.Buffer
	c.SetErr(&buf)
	p, err := resolveBasemap(c)
	if err != nil {
		t.Fatalf("resolveBasemap: %v", err)
	}
	tf, ok := p.(*tilemap.Thunderforest)
	if !ok {
		t.Fatalf("resolveBasemap returned %T, want *tilemap.Thunderforest (cache is off)", p)
	}
	if tf.Style != "landscape" {
		t.Errorf("provider style = %q, want %q", tf.Style, "landscape")
	}
	if buf.Len() != 0 {
		t.Errorf("a private key file under a valid style still printed %q", buf.String())
	}
}

// TestResolveBasemap_WrapsInACacheUnlessDisabled pins the OTHER half of the
// wiring the happy-path test above deliberately turned off: by default (and
// under any --basemap-cache value except "off"), the provider returned must
// be wrapped so repeated renders of the same activity do not re-fetch
// identical imagery.
func TestResolveBasemap_WrapsInACacheUnlessDisabled(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{
		basemap: "outdoors", basemapKeyFile: resolveBasemapTestKeyFile(t), basemapCache: t.TempDir(),
	}

	c := &cobra.Command{}
	c.SetErr(&bytes.Buffer{})
	p, err := resolveBasemap(c)
	if err != nil {
		t.Fatalf("resolveBasemap: %v", err)
	}
	cached, ok := p.(*tilemap.Cached)
	if !ok {
		t.Fatalf("resolveBasemap returned %T, want *tilemap.Cached", p)
	}
	if cached.Dir != renderOpts.basemapCache {
		t.Errorf("cache dir = %q, want %q", cached.Dir, renderOpts.basemapCache)
	}
	if _, ok := cached.Provider.(*tilemap.Thunderforest); !ok {
		t.Errorf("Cached wraps %T, want *tilemap.Thunderforest", cached.Provider)
	}
}

// TestResolveBasemap_WorldReadableKeyWarnsButStillResolves pins that a
// permissions problem is advisory: the file is the user's own, and a render
// that refused over it would be this program deciding something that is not
// its decision.
func TestResolveBasemap_WorldReadableKeyWarnsButStillResolves(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("aabbccdd11223344aabbccdd11223344"), 0o644); err != nil {
		t.Fatal(err)
	}
	renderOpts = renderOptions{basemap: "outdoors", basemapKeyFile: path, basemapCache: "off"}

	c := &cobra.Command{}
	var buf bytes.Buffer
	c.SetErr(&buf)
	p, err := resolveBasemap(c)
	if err != nil {
		t.Fatalf("resolveBasemap: %v", err)
	}
	if p == nil {
		t.Error("a world-readable key file still refused to resolve a provider")
	}
	if !strings.Contains(buf.String(), "warning:") {
		t.Errorf("no warning about the world-readable key file; got %q", buf.String())
	}
}

// TestResolveBasemap_MissingKeyFileFails pins that a key file which cannot be
// read is a render error rather than a silent "no basemap" -- a user who
// typoed the path deserves to be told, not to get a plain render and wonder
// why the map never turned up.
func TestResolveBasemap_MissingKeyFileFails(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{basemap: "outdoors", basemapKeyFile: filepath.Join(t.TempDir(), "nope")}

	c := &cobra.Command{}
	c.SetErr(&bytes.Buffer{})
	_, err := resolveBasemap(c)
	if err == nil {
		t.Fatal("a missing key file was accepted")
	}
}

// TestBasemapCacheDir_OffDefaultAndCustom pins the three spellings
// --basemap-cache accepts.
func TestBasemapCacheDir_OffDefaultAndCustom(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)

	renderOpts = renderOptions{basemapCache: "off"}
	if got := basemapCacheDir(); got != "" {
		t.Errorf("--basemap-cache off resolved to %q, want no caching", got)
	}

	renderOpts = renderOptions{basemapCache: ""}
	if got, want := basemapCacheDir(), tilemap.DefaultCacheDir(); got != want {
		t.Errorf("--basemap-cache unset resolved to %q, want the default %q", got, want)
	}

	custom := t.TempDir()
	renderOpts = renderOptions{basemapCache: custom}
	if got := basemapCacheDir(); got != custom {
		t.Errorf("--basemap-cache %q resolved to %q", custom, got)
	}
}

// TestRunRender_RejectsANegativeHighlightTransition covers the one rejection
// path in runRender that has no test of its own: unlike every check above,
// which lives in validateRenderOptions (testable with no activity file at
// all) or is exercised through resolveHighlights/resolveSmoothing directly,
// --highlight-transition's own "< 0" guard is inline in runRender itself,
// reached only after the activity is decoded and the timer model built. So
// this test drives the real command end to end against a synthetic
// activity, through cobra exactly as Execute does -- but the negative value
// must be refused before render.New or ffmpeg is ever reached, so no video
// is produced and no ffmpeg is required.
func TestRunRender_RejectsANegativeHighlightTransition(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)

	opts := fittest.DefaultOptions()
	opts.Count = 50 // a handful of samples is enough for the timer window this check needs; nothing here reads a sample.
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}

	renderOpts = renderOptions{}
	cmd := &cobra.Command{Use: "test", Args: cobra.ExactArgs(1), RunE: runRender, SilenceUsage: true, SilenceErrors: true}
	bindRenderFlags(cmd)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--highlight-transition=-1s", "--frames", "--output-dir", t.TempDir(), path})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("accepted a negative --highlight-transition")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("error should say the value is negative; got: %v", err)
	}
}

// TestRenderFlags_LayoutAndThemeDefaultsAreSelectable pins that the values the
// flags default to are values the flags accept.
//
// A default that is not in the offered set is a program that cannot run without
// arguments, and it fails at the first render rather than at build time.
func TestRenderFlags_LayoutAndThemeDefaultsAreSelectable(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	bindRenderFlags(cmd)

	layoutDefault := cmd.Flags().Lookup("layout").DefValue
	if _, err := panel.SelectLayout(layoutDefault, 1920, 1080); err != nil {
		t.Errorf("--layout's default %q is not selectable: %v", layoutDefault, err)
	}
	themeDefault := cmd.Flags().Lookup("theme").DefValue
	if _, err := panel.SelectTheme(themeDefault); err != nil {
		t.Errorf("--theme's default %q is not selectable: %v", themeDefault, err)
	}
	gaugeStyleDefault := cmd.Flags().Lookup("gauge-style").DefValue
	if _, err := panel.SelectGaugeStyle(gaugeStyleDefault); err != nil {
		t.Errorf("--gauge-style's default %q is not selectable: %v", gaugeStyleDefault, err)
	}
	if gaugeStyleDefault != panel.GaugeStyleNamePlain {
		t.Errorf("--gauge-style's default is %q, want %q -- this step must not change the default", gaugeStyleDefault, panel.GaugeStyleNamePlain)
	}

	// And every name the help text offers must work, or the help lies.
	for _, name := range []string{panel.LayoutAuto, "landscape", "portrait"} {
		if _, err := panel.SelectLayout(name, 1920, 1080); err != nil {
			t.Errorf("--layout %s is offered but rejected: %v", name, err)
		}
	}
	for _, th := range panel.Themes() {
		if _, err := panel.SelectTheme(th.Name); err != nil {
			t.Errorf("--theme %s is offered but rejected: %v", th.Name, err)
		}
	}
	for _, name := range []string{panel.GaugeStyleNamePlain, panel.GaugeStyleNameTrack, panel.GaugeStyleNameDial} {
		if _, err := panel.SelectGaugeStyle(name); err != nil {
			t.Errorf("--gauge-style %s is offered but rejected: %v", name, err)
		}
	}
}

// TestSelectGaugeStyle_RefusesAnUnknownValue is the --gauge-style analogue of
// the theme and layout tests panel's own canvas_test.go and layouts_test.go
// already carry: a typo must be refused rather than silently rendering the
// plain style nobody asked to keep -- see SelectGaugeStyle's own doc comment.
func TestSelectGaugeStyle_RefusesAnUnknownValue(t *testing.T) {
	for _, name := range []string{panel.GaugeStyleNamePlain, panel.GaugeStyleNameTrack, panel.GaugeStyleNameDial} {
		if _, err := panel.SelectGaugeStyle(name); err != nil {
			t.Errorf("SelectGaugeStyle(%q): %v", name, err)
		}
	}
	if _, err := panel.SelectGaugeStyle("bars"); err == nil {
		t.Error("SelectGaugeStyle(\"bars\") was accepted, want an error")
	}
}

// --- the highlight summary block --------------------------------------------

// TestWriteHighlightSummary_DecomposesBaseAndHighlightsAndListsEach pins the
// decomposition line and the per-highlight table.
//
// The fixture is the exact one internal/panel's own
// TestNewSegmentedTimeline_ThirtySecondsPlusNineSecondsOfHighlightsIsThirtyNine
// already verifies independently: a 30-minute activity at a 60x base renders
// in 30s alone, and this highlight -- 10m to 11m, paced to fill 10s of video
// -- adds 9s on top, for 39s total. Reusing it here rather than deriving a
// second fixture means the two tests cannot quietly drift apart about what
// "30s base + 9s of highlights = 39s of video" is supposed to mean.
func TestWriteHighlightSummary_DecomposesBaseAndHighlightsAndListsEach(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	const fps, base = 30.0, 60.0
	highlight := panel.Highlight{Name: "Hill climb", From: 10 * time.Minute, To: 11 * time.Minute, Video: 10 * time.Second}
	tl, err := panel.NewSegmentedTimeline(time.Now(), 30*time.Minute, fps, base, []panel.Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if got, want := tl.Duration(), 39*time.Second; got != want {
		t.Fatalf("precondition: tl.Duration() = %v, want %v -- the fixture no longer matches the one this test's own doc comment describes", got, want)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)

	writeHighlightSummary(c, tl, nil, []panel.Highlight{highlight}, panel.Smoothing{}, panel.DefaultTheme(), false)

	out := buf.String()
	if !strings.Contains(out, "0:00:30 base + 0:00:09 of highlights = 0:00:39 of video") {
		t.Errorf("summary is missing the base/highlight decomposition; got:\n%s", out)
	}
	// Rate = (11m-10m)/10s = 6x; 60s of activity at 6x and 30fps is exactly
	// 300 frames, 10.0s of video -- no rounding remainder to complicate this
	// expectation.
	if !strings.Contains(out, `highlights: "Hill climb" 0:10:00 -> 10.0s (6x)`) {
		t.Errorf("summary is missing the highlight's own table line; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_WarnsOnClippedOneFrameAndPaused pins the three
// "adjusted, and reported" surprises resolveHighlights marks rather than
// refuses -- a highlight running past the activity's own end, one whose own
// rate rounds its frame count down to one, and one lying wholly inside a
// paused stretch. Each is a warning this printer owes the reader BECAUSE
// resolveHighlights already decided not to make it fatal.
func TestWriteHighlightSummary_WarnsOnClippedOneFrameAndPaused(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	highlights := []panel.Highlight{
		{Name: "Final push", From: 0, To: 5 * time.Second, Clipped: true},
		// A RateFactor this extreme over ten milliseconds rounds well below
		// one frame at 30fps, so Timeline forces it to exactly one.
		{Name: "Blip", From: 10 * time.Second, To: 10*time.Second + 10*time.Millisecond, RateFactor: 1000},
		{Name: "Water stop", From: 20 * time.Second, To: 25 * time.Second, PausedThroughout: true},
	}
	tl, err := panel.NewSegmentedTimeline(time.Now(), time.Minute, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, nil, highlights, panel.Smoothing{}, panel.DefaultTheme(), false)

	out := buf.String()
	for _, want := range []string{
		`highlight "Final push" clipped to the activity's end (0:00:05)`,
		`highlight "Blip" occupies one frame; its speedup is higher than the render can show`,
		`highlight "Water stop" lies inside a paused stretch; the dashboard is frozen through it`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q; got:\n%s", want, out)
		}
	}
}

// TestWriteHighlightSummary_ReportsResolvedColourAndContrastWarnings pins
// the two things a background= adds to the summary: the resolved colour,
// unconditionally, and a legibility warning ONLY when the colour actually
// earns one -- against the real shipped dark theme, not a synthetic stand-in,
// because the whole point of the relative check (see
// backgroundContrastWarnings) is that it must be satisfiable against a
// theme this program actually ships.
//
// The three cases are chosen so that each isolates one outcome, and the
// first of them is the one that would have caught both earlier versions of
// this check (see chromeContrastFloor's own doc comment for what they were
// and why they failed).
//
// "Reasonable" is #1B2A4A, an unremarkable dark navy and exactly the sort
// of colour a user reaching for background= would type. It measures 12.7:1
// against Foreground, 2.8:1 against Dim and 1.6:1 against Absent, so it
// clears WCAG 2's 4.5:1 where that applies and sits above chromeContrastFloor
// where that applies. It must earn NO warning of any kind. Under the two
// rejected rules it warned -- which is the whole reason it is pinned here.
//
// "TooLight" is pure white. It moves FURTHER from Dim and Absent than the
// theme's own near-black Background does (5.0:1 and 8.8:1), so the chrome
// half of the check stays silent, while Foreground -- itself near-white --
// collapses to 1.1:1. An isolated exercise of the ABSOLUTE half.
//
// "Collided" is DarkTheme's own Absent, 0x4A4A54, set as a background. It
// measures exactly 1.0:1 against Absent, because it IS Absent: the
// placeholder colour has become genuinely invisible against the wash. This
// is the single failure chromeContrastFloor exists to catch, and it clears
// 4.5:1 against Foreground with real margin (7.8:1) and 1.5:1 against Dim
// with real margin too (1.7:1), so it exercises the chrome half -- against
// absent alone -- with both other checks silent, and with room to spare on
// each. An earlier version of this fixture used the theme's own Dim
// instead, which collides with Dim exactly as this collides with Absent,
// but happens to measure only 4.51:1 against Foreground -- 0.01 over the
// 4.5:1 threshold this test asserts it clears, which is not a margin a test
// should depend on staying on the right side of. Absent gives the same
// exercise of the same code path with an order of magnitude more room.
func TestWriteHighlightSummary_ReportsResolvedColourAndContrastWarnings(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	theme := panel.DefaultTheme() // the shipped dark theme, not a stand-in
	highlights := []panel.Highlight{
		{Name: "Reasonable", From: 0, To: 5 * time.Second, HasBackground: true, Background: color.NRGBA{R: 0x1B, G: 0x2A, B: 0x4A, A: 0xFF}},
		{Name: "TooLight", From: 10 * time.Second, To: 15 * time.Second, HasBackground: true, Background: color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}},
		{Name: "Collided", From: 20 * time.Second, To: 25 * time.Second, HasBackground: true, Background: color.NRGBA{R: 0x4A, G: 0x4A, B: 0x54, A: 0xFF}},
	}
	tl, err := panel.NewSegmentedTimeline(time.Now(), time.Minute, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, nil, highlights, panel.Smoothing{}, theme, false)

	out := buf.String()
	for _, want := range []string{
		`highlight "Reasonable" background #1B2A4A`,
		`highlight "TooLight" background #FFFFFF`,
		`highlight "Collided" background #4A4A54`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing the resolved colour line %q; got:\n%s", want, out)
		}
	}

	// The load-bearing one: an ordinary dark navy must earn no warning at
	// all. Both rejected versions of this check warned here, and a rule that
	// fires on every colour a user would plausibly type is noise that trains
	// them to ignore the whole summary.
	if strings.Contains(out, `"Reasonable" background contrast`) {
		t.Errorf("an ordinary dark navy was warned about; the check has become noise again. got:\n%s", out)
	}

	// White: the absolute half fires, the chrome half stays silent.
	if !strings.Contains(out, `highlight "TooLight" background contrast against foreground is 1.1:1, below the WCAG 2 threshold of 4.5:1`) {
		t.Errorf("summary is missing the absolute foreground warning for a near-white background under the dark theme; got:\n%s", out)
	}
	for _, role := range []string{"dim", "absent"} {
		if strings.Contains(out, `"TooLight" background contrast against `+role) {
			t.Errorf("white was warned about against %s, but it moves further from both than the theme's own background does; got:\n%s", role, out)
		}
	}

	// The theme's own Absent as a background: the chrome half fires against
	// absent at exactly 1.0:1, and stays silent against dim (1.7:1, clears
	// the 1.5:1 floor) and against foreground (7.8:1, clears 4.5:1 with
	// real margin).
	if !strings.Contains(out, `highlight "Collided" background contrast against absent is 1.0:1, below the floor of 1.5:1`) {
		t.Errorf("a background equal to the theme's own Absent did not warn; this is the failure the floor exists to catch. got:\n%s", out)
	}
	if strings.Contains(out, `"Collided" background contrast against foreground`) {
		t.Errorf("the Absent-coloured background was warned about against foreground, which it clears at 7.8:1; got:\n%s", out)
	}
	if strings.Contains(out, `"Collided" background contrast against dim`) {
		t.Errorf("the Absent-coloured background was warned about against dim, which it clears at 1.7:1; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_ReportsAnUnmarkableHighlight pins the summary's
// half of the unmarkable-highlight case in route marking's absent-data
// policy: a highlight whose span has no GPS fix anywhere near it cannot be
// drawn on the
// map at all -- there is no placeholder available where the missing thing IS
// the location -- so the honest analogue of "decline and announce" is this
// line.
//
// It goes through the SAME route.FromTrack(track, route.DefaultMaxPoints) and
// route.SpanIndices RoutePanel's own Prepare uses (internal/panel/route.go),
// never a second copy of the predicate -- which is why this test's fixture is
// a track with a real, permanent GPS dropout rather than a synthetic call
// into route.SpanIndices directly: it is proving the CALL SITE agrees with
// the panel, not the function in isolation (already covered by
// internal/route's own table test).
func TestWriteHighlightSummary_ReportsAnUnmarkableHighlight(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 100)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time: start.Add(time.Duration(i) * time.Second),
			// GPS only for the first half of the activity -- a dropout that
			// never comes back, which is the real way a highlight placed
			// later in the activity ends up with nothing to mark.
			HasGPS: i < 50,
			Lat:    55 + float64(i)*1e-5, Lon: 12 + float64(i)*1e-5,
		}
	}
	track := &fitactivity.Track{Samples: samples}

	highlights := []panel.Highlight{
		{Name: "Covered", From: 10 * time.Second, To: 20 * time.Second},
		{Name: "Lost signal", From: 80 * time.Second, To: 90 * time.Second},
	}
	tl, err := panel.NewSegmentedTimeline(start, 99*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, track, highlights, panel.Smoothing{}, panel.DefaultTheme(), false)

	out := buf.String()
	if strings.Contains(out, `"Covered" has no GPS fixes`) {
		t.Errorf("a highlight entirely inside the GPS-covered stretch was reported as unmarkable; got:\n%s", out)
	}
	if !strings.Contains(out, `highlight "Lost signal" has no GPS fixes; it is not marked on the route`) {
		t.Errorf("summary is missing the unmarkable-highlight line for a highlight past the last GPS fix; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_ReportsAHighlightUnplaceableOnTheElevationProfile
// pins the summary's half of D.1's own policy: a highlight whose bounds fall
// where distance is unknown cannot be placed on the elevation profile's axis
// at all -- there is no placeholder for a mark with no position, the same
// reasoning TestWriteHighlightSummary_ReportsAnUnmarkableHighlight already
// pins for the route -- so the honest analogue is this line.
//
// markersOnProfile is passed true directly rather than built through a real
// render.New, unlike that route test: writeHighlightSummary's own
// hasProfileDistanceSpan call is the thing under test here, and building a
// whole Renderer to reach it would only add a second fixture this test does
// not need. render_test.go's TestNew_MarkerPanelAbsorbedWhenTheProfileTakesTheBand
// is what proves markersOnProfile itself is computed correctly.
func TestWriteHighlightSummary_ReportsAHighlightUnplaceableOnTheElevationProfile(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 100)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time: start.Add(time.Duration(i) * time.Second),
			// Distance only for the first half -- a dropout that never comes
			// back, the identical shape the route test above uses for GPS,
			// applied to distance instead.
			HasDistance: i < 50,
			Distance:    float64(i) * 3,
		}
	}
	track := &fitactivity.Track{Samples: samples}

	highlights := []panel.Highlight{
		{Name: "Covered", From: 10 * time.Second, To: 20 * time.Second},
		{Name: "Lost signal", From: 80 * time.Second, To: 90 * time.Second},
	}
	tl, err := panel.NewSegmentedTimeline(start, 99*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, track, highlights, panel.Smoothing{}, panel.DefaultTheme(), true)

	out := buf.String()
	if strings.Contains(out, `"Covered" has no distance`) {
		t.Errorf("a highlight entirely inside the distance-covered stretch was reported as unplaceable; got:\n%s", out)
	}
	if !strings.Contains(out, `highlight "Lost signal" has no distance at its bounds; it is not marked on the elevation profile`) {
		t.Errorf("summary is missing the unplaceable-highlight line for a highlight past the last distance reading; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_UnplaceableOnProfileSilentWhenMarkersNotAbsorbed
// is the guard's own negative case: the identical fixture as the test above,
// with markersOnProfile left false, must print nothing about the elevation
// profile at all -- a highlight with no distance at its bounds is only
// unplaceable news once something was actually trying to place it there
// (see markersOnProfile's own doc comment on writeHighlightSummary).
func TestWriteHighlightSummary_UnplaceableOnProfileSilentWhenMarkersNotAbsorbed(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 100)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time:        start.Add(time.Duration(i) * time.Second),
			HasDistance: i < 50,
			Distance:    float64(i) * 3,
		}
	}
	track := &fitactivity.Track{Samples: samples}

	highlights := []panel.Highlight{
		{Name: "Lost signal", From: 80 * time.Second, To: 90 * time.Second},
	}
	tl, err := panel.NewSegmentedTimeline(start, 99*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, track, highlights, panel.Smoothing{}, panel.DefaultTheme(), false)

	if out := buf.String(); strings.Contains(out, "elevation profile") {
		t.Errorf("markersOnProfile was false, but the summary reported an unplaceable mark on the elevation profile anyway; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_ReportsAHighlightUnplaceableWithOnlyOneEndpointMissingDistance
// is the partial-span case neither test above covers: D.1's own policy is
// that a highlight needs distance at BOTH bounds, and one resolvable
// endpoint with the other unresolvable is STILL unplaceable -- not "half
// marked" and not silently accepted because the From end happened to
// resolve. hasProfileDistanceSpan's AND is what this pins; an
// implementation that only checked one end (e.g. From, since that is
// nearly always the one a user picks first) would wrongly call this
// highlight placeable.
func TestWriteHighlightSummary_ReportsAHighlightUnplaceableWithOnlyOneEndpointMissingDistance(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 100)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time: start.Add(time.Duration(i) * time.Second),
			// Distance only for the first half -- identical fixture shape to
			// the two tests above, but this highlight straddles the boundary
			// rather than sitting fully on one side of it.
			HasDistance: i < 50,
			Distance:    float64(i) * 3,
		}
	}
	track := &fitactivity.Track{Samples: samples}

	highlights := []panel.Highlight{
		// From (10s) is well inside the covered stretch; To (80s) is well
		// past the last distance reading.
		{Name: "Partial", From: 10 * time.Second, To: 80 * time.Second},
	}
	tl, err := panel.NewSegmentedTimeline(start, 99*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, track, highlights, panel.Smoothing{}, panel.DefaultTheme(), true)

	out := buf.String()
	if !strings.Contains(out, `highlight "Partial" has no distance at its bounds; it is not marked on the elevation profile`) {
		t.Errorf("a highlight with only ONE endpoint missing distance was not reported as unplaceable; got:\n%s", out)
	}
}

// TestWriteHighlightSummary_PrintsNothingWithNoHighlights keeps an ordinary
// render's summary identical to one from before this feature existed -- the
// same promise the highlight panel's own Accepts makes for the pixels.
func TestWriteHighlightSummary_PrintsNothingWithNoHighlights(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	tl, err := panel.NewTimeline(time.Now(), time.Minute, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeHighlightSummary(c, tl, nil, nil, panel.Smoothing{}, panel.DefaultTheme(), false)
	if buf.Len() != 0 {
		t.Errorf("writeHighlightSummary printed something with no highlights configured: %q", buf.String())
	}
}

// --- the two decline headings ------------------------------------------------

// summaryDecliner is a panel that always declines, standing in for a panel
// whose activity genuinely carries none of its data -- the "carries no such
// data" heading's own real case.
type summaryDecliner string

func (d summaryDecliner) Name() string              { return string(d) }
func (summaryDecliner) Accepts(*panel.Context) bool { return false }
func (summaryDecliner) Prepare(*panel.Context, panel.Box) panel.Painter {
	// Never actually called: Accepts is false, so layout.Resolve prunes this
	// leaf before any Prepare is invoked. A panic here would say so loudly if
	// that ever stopped being true.
	panic("summaryDecliner.Prepare called despite declining")
}

// summaryAccepter is a panel that always accepts, so the test layout has at
// least one survivor and "panels: ..." has something real to report.
type summaryAccepter string

func (a summaryAccepter) Name() string              { return string(a) }
func (summaryAccepter) Accepts(*panel.Context) bool { return true }
func (summaryAccepter) Prepare(*panel.Context, panel.Box) panel.Painter {
	return summaryPainter{}
}

type summaryPainter struct{ panel.NoStatic }

func (summaryPainter) Dynamic(*panel.Canvas, panel.Frame) {}

// summaryTestContext builds the minimum panel.Context render.New will accept,
// with no real FIT data behind it -- this suite is about what the summary
// PRINTS, not about a real activity, so a two-sample track and a bare timer
// are enough to satisfy render.New's own nil checks.
func summaryTestContext(t *testing.T, highlights []panel.Highlight) *panel.Context {
	t.Helper()
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: start}, {Time: start.Add(time.Minute)},
	}}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	return &panel.Context{
		Track: track, Timer: timer, Timeline: tl,
		Width: 200, Height: 120, FontScale: 0.05, Fonts: fonts,
		Highlights: highlights,
	}
}

// TestWritePanelSummary_SplitsTheHighlightDeclineFromTheActivityDataOne is
// the fix this step exists for: with no --highlight given at all, the
// highlight panel declines because of a FLAG, not because of anything the
// activity does or does not carry, and folding its name into "this activity
// carries no such data" tells the user their FIT file lacks something no FIT
// file has ever recorded. Verified against a real render.Renderer, built
// through render.New exactly as the CLI builds one, rather than against a
// hand-built Declined() list that could quietly stop matching what New
// actually reports.
func TestWritePanelSummary_SplitsTheHighlightDeclineFromTheActivityDataOne(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := summaryTestContext(t, nil) // no --highlight given at all
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: summaryAccepter("clock")},
		{Panel: summaryDecliner("power")},
		{Panel: panel.MarkerPanel{}},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	out := buf.String()
	// The activity-data heading names ONLY power -- never the marker panel,
	// and never both together under this heading, which is exactly the bug
	// this step fixes: `declined (...carries no such data): power, markers`.
	if !strings.Contains(out, "declined (this activity carries no such data): power") {
		t.Errorf("the activity-data decline heading is missing or wrong; got:\n%s", out)
	}
	if strings.Contains(out, "no such data): power, markers") || strings.Contains(out, "no such data): markers") {
		t.Fatalf("the marker panel's decline was folded into the activity-data heading; got:\n%s", out)
	}
	// The configuration heading names BOTH flags that could have placed this
	// panel, not just --highlight: once --label can place it too, a heading
	// naming only one of them tells a user reaching for labels that the
	// wrong flag is missing.
	if !strings.Contains(out, "declined (no --highlight or --label given): markers") {
		t.Errorf("the marker panel's decline is missing its own configuration heading; got:\n%s", out)
	}
}

// TestWritePanelSummary_ReportsNoHighlightDeclineWhenHighlightsAreConfigured
// is the other half: once --highlight is given, the highlight panel accepts
// and there is nothing to report under either decline heading for it.
func TestWritePanelSummary_ReportsNoHighlightDeclineWhenHighlightsAreConfigured(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	highlights := []panel.Highlight{{From: 5 * time.Second, To: 10 * time.Second}}
	ctx := summaryTestContext(t, highlights)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: summaryAccepter("clock")},
		{Panel: panel.MarkerPanel{}},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	if out := buf.String(); strings.Contains(out, "declined") {
		t.Errorf("nothing declined once --highlight is configured, but the summary printed a decline line; got:\n%s", out)
	}
}

// TestWritePanelSummary_ReportsBottomBandOmissionAsAThirdReasonNotADecline is
// the third heading's own test: a panel removed by --bottom-band distance is
// neither "this activity carries no such data" (the fixture panel below
// accepts unconditionally, standing in for an activity that DOES carry
// elevation) nor "no --highlight or --label given" (that heading is reserved
// for the marker panel). Folding it into either would tell the reader
// something false about either the activity or the flags they passed.
func TestWritePanelSummary_ReportsBottomBandOmissionAsAThirdReasonNotADecline(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := summaryTestContext(t, nil)
	ctx.BottomBand = panel.BottomBandDistance
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: summaryAccepter("clock")},
		{Panel: summaryAccepter("elevation")},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	out := buf.String()
	if !strings.Contains(out, "panels: clock\n") {
		t.Errorf("elevation should not be among the drawn panels; got:\n%s", out)
	}
	if !strings.Contains(out, "omitted (--bottom-band distance): elevation") {
		t.Errorf("missing the third heading naming the flag-omitted panel; got:\n%s", out)
	}
	if strings.Contains(out, "declined") {
		t.Errorf("a flag-omitted panel must never be reported under either decline heading; got:\n%s", out)
	}
}

// TestWritePanelSummary_BottomBandProfileReportsNothingOmitted is the
// default's own negative case: with BottomBand left at its zero value, an
// accepting panel named like the elevation panel is placed exactly as any
// other accepting panel, and the summary's new heading never appears at all
// -- an ordinary render's summary must read identically to one from before
// this flag existed.
func TestWritePanelSummary_BottomBandProfileReportsNothingOmitted(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := summaryTestContext(t, nil) // BottomBand left at its zero value
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: summaryAccepter("clock")},
		{Panel: summaryAccepter("elevation")},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	out := buf.String()
	if !strings.Contains(out, "panels: clock, elevation\n") {
		t.Errorf("both panels should have been placed and drawn; got:\n%s", out)
	}
	if strings.Contains(out, "omitted") {
		t.Errorf("the default --bottom-band must not print the omitted heading at all; got:\n%s", out)
	}
}

// elevationAbsorptionTestContext builds a real, fittest-backed Context
// carrying real elevation and distance -- unlike summaryTestContext's bare
// two-sample track, this exercises render.New's keep filter against the REAL
// (panel.ElevationPanel{}).Accepts, which is what profileTakesTheBand
// actually calls (see internal/render's own doc comment on it). A stand-in
// panel named "elevation" would never reach that call at all.
func elevationAbsorptionTestContext(t *testing.T, highlights []panel.Highlight) *panel.Context {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	opts := fittest.DefaultOptions()
	opts.Count = 60
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	// Elevation is built here, once, the same way runRender now builds it:
	// ElevationPanel's own Accepts and Prepare read Context.Elevation rather
	// than building a copy of the model themselves, so a Context that left
	// this nil would make the real ElevationPanel this test places decline
	// regardless of what the fixture's own track carries.
	elevTuning := panel.DefaultElevationTuning(track)
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: 1920, Height: 1080, FontScale: 0.05, Fonts: fonts,
		Highlights: highlights,
		Elevation:  panel.BuildElevation(track, elevTuning),
	}
}

// TestWritePanelSummary_ReportsAbsorbedMarkersAsAFourthReasonNotADecline
// pins the fourth heading writePanelSummary gains in this step: once the
// elevation profile is drawing the configured highlight as a mark on its own
// axis, the marker panel is neither placed (it draws nothing in its own
// box) nor declined (it had something to show) -- it is absorbed, and the
// summary must say so under its own heading rather than either.
func TestWritePanelSummary_ReportsAbsorbedMarkersAsAFourthReasonNotADecline(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	highlights := []panel.Highlight{{Name: "h", From: 5 * time.Second, To: 15 * time.Second}}
	ctx := elevationAbsorptionTestContext(t, highlights)
	// ctx.BottomBand left at its zero value: the profile must be free to
	// take the band.
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.ElevationPanel{}},
		{Panel: panel.MarkerPanel{}},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	out := buf.String()
	if !strings.Contains(out, "panels: elevation\n") {
		t.Errorf("only the elevation panel should have been placed in its own box; got:\n%s", out)
	}
	if !strings.Contains(out, "absorbed into the elevation profile: markers\n") {
		t.Errorf("missing the fourth heading naming the absorbed marker panel; got:\n%s", out)
	}
	if strings.Contains(out, "declined") {
		t.Errorf("an absorbed panel must never be reported under either decline heading; got:\n%s", out)
	}
}

// TestWritePanelSummary_NoAbsorptionHeadingWithoutHighlightsOrLabels is the
// guard's own negative case: the identical fixture as the test above, with
// no --highlight or --label configured at all, must never print the fourth
// heading -- MarkerPanel's own Accepts already declines with nothing
// configured, and that decline (a fact about the flags) must not be
// relabelled as an absorption (a fact about where marks went) when there
// were no marks to begin with.
func TestWritePanelSummary_NoAbsorptionHeadingWithoutHighlightsOrLabels(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := elevationAbsorptionTestContext(t, nil) // no --highlight or --label at all
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.ElevationPanel{}},
		{Panel: panel.MarkerPanel{}},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})

	out := buf.String()
	if strings.Contains(out, "absorbed") {
		t.Errorf("nothing was configured to absorb, but the summary printed the absorption heading anyway; got:\n%s", out)
	}
	if !strings.Contains(out, "declined (no --highlight or --label given): markers") {
		t.Errorf("the marker panel should still decline for the ordinary reason; got:\n%s", out)
	}
}

// TestParseBottomBand_RefusesUnknownValues follows the precedent
// parseHighlightStyle, parsePowerSource, SelectLayout and SelectTheme all
// set: a typo in --bottom-band is refused where it was typed, not silently
// rendered with the default.
func TestParseBottomBand_RefusesUnknownValues(t *testing.T) {
	for _, band := range []string{panel.BottomBandProfile, panel.BottomBandDistance} {
		got, err := parseBottomBand(band)
		if err != nil {
			t.Errorf("parseBottomBand(%q): %v", band, err)
		}
		if got != band {
			t.Errorf("parseBottomBand(%q) = %q", band, got)
		}
	}
	if _, err := parseBottomBand("elevation"); err == nil {
		t.Error("parseBottomBand accepted an unknown value")
	}
}

// TestParseGauges_RefusesUnknownValues mirrors
// TestParseBottomBand_RefusesUnknownValues for --gauges: both legal values
// round-trip, and a typo is refused rather than silently rendering the
// default -- the same precedent parseBottomBand, parseHighlightStyle,
// SelectLayout and SelectTheme all follow.
func TestParseGauges_RefusesUnknownValues(t *testing.T) {
	for _, g := range []string{panel.GaugesMetrics, panel.GaugesBalance} {
		got, err := parseGauges(g)
		if err != nil {
			t.Errorf("parseGauges(%q): %v", g, err)
		}
		if got != g {
			t.Errorf("parseGauges(%q) = %q", g, got)
		}
	}
	if _, err := parseGauges("bars"); err == nil {
		t.Error("parseGauges accepted an unknown value")
	}
}

// --- the --gauges summary ---------------------------------------------------

// gaugeSelectionTestContext builds a Context over a track that carries pace
// and heart rate, plus -- when withBalance is true -- a genuine
// StanceTimeBalance too. false is what stands in for "this activity carries
// none of the four balance metrics" for
// TestWriteGaugeSelectionSummary_ReportsTheFallbackWhenNoBalanceDataExists,
// below: withBalance false never sets the field at all, rather than setting
// it to a refused zero, so that test is about the ordinary "no such field"
// case rather than the separate "field present but every value refused" one
// balance_test.go already covers at the panel layer.
func gaugeSelectionTestContext(t *testing.T, withBalance bool) *panel.Context {
	t.Helper()
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const n = 30
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		s := fitactivity.Sample{
			Time: start.Add(time.Duration(i) * time.Second),
			// So Pace's own Accepts (and ContactBalance's, whether or not it
			// is expected to matter here) both have something to walk.
			HasHeartRate: true, HeartRate: uint8(140 + i%10),
			HasSpeed: true, Speed: 3.0,
		}
		if withBalance {
			s.HasStanceTimeBalance, s.StanceTimeBalance = true, 55
		}
		samples[i] = s
	}
	track := &fitactivity.Track{Samples: samples}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, 30, 1, nil)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: 1920, Height: 1080, FontScale: 0.05, Fonts: fonts,
		Gauges: panel.GaugesBalance,
	}
}

// gaugeSelectionTestLayout places panel.ContactBalance() and panel.HeartRate()
// side by side, standing in for the real gauge Alt slot's own two candidates
// without needing the full real tree: this test's own subject is
// writeGaugeSelectionSummary's own two messages, which read back r.Placed()
// and ctx.Gauges alone, not the layout that produced them.
func gaugeSelectionTestLayout() panel.Layout {
	return panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.ContactBalance()},
		{Panel: panel.HeartRate()},
	}}}
}

// TestWriteGaugeSelectionSummary_ReportsBalanceIsShowing pins the first of
// --gauges balance's two outcomes: the activity carries a balance metric, it
// is placed, and the summary names that pace was kept and that heart rate,
// power and cadence are not shown.
func TestWriteGaugeSelectionSummary_ReportsBalanceIsShowing(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSelectionTestContext(t, true)
	r, err := render.New(ctx, gaugeSelectionTestLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSelectionSummary(c, r, ctx)

	out := buf.String()
	if !strings.Contains(out, "gauges: balance") {
		t.Fatalf("summary does not report balance is showing; got:\n%s", out)
	}
	if !strings.Contains(out, "pace") {
		t.Errorf("summary does not name that pace was kept; got:\n%s", out)
	}
	if !strings.Contains(out, "heart rate, power and cadence") {
		t.Errorf("summary does not name the three gauges that are not shown; got:\n%s", out)
	}
}

// TestWriteGaugeSelectionSummary_ReportsTheFallbackWhenNoBalanceDataExists
// pins the second outcome: an activity carrying none of the four falls back
// to the ordinary gauges, and the summary says so rather than staying
// silent, which would be the confusing outcome the plan this feature came
// from explicitly calls out.
func TestWriteGaugeSelectionSummary_ReportsTheFallbackWhenNoBalanceDataExists(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSelectionTestContext(t, false)
	r, err := render.New(ctx, gaugeSelectionTestLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	for _, p := range r.Placed() {
		if p.Panel.Name() == "contact-balance" {
			t.Fatal("precondition failed: contact-balance was placed despite carrying no genuine reading")
		}
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSelectionSummary(c, r, ctx)

	out := buf.String()
	if !strings.Contains(out, "gauges: balance was requested") {
		t.Fatalf("summary does not report the fallback; got:\n%s", out)
	}
	if !strings.Contains(out, "none of the four balance") {
		t.Errorf("summary does not say why it fell back; got:\n%s", out)
	}
}

// TestWriteGaugeSelectionSummary_SilentUnderMetricsSelection pins that an
// ordinary --gauges metrics render (the default) prints nothing here at
// all -- the same discipline every other flag-gated summary line in this
// file follows, so a render that never asked for --gauges balance sees a
// summary identical to one from before this flag existed.
func TestWriteGaugeSelectionSummary_SilentUnderMetricsSelection(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSelectionTestContext(t, true)
	ctx.Gauges = panel.GaugesMetrics
	r, err := render.New(ctx, gaugeSelectionTestLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSelectionSummary(c, r, ctx)
	if buf.String() != "" {
		t.Errorf("writeGaugeSelectionSummary printed something under --gauges metrics: %q", buf.String())
	}
}

// TestWriteGaugeSelectionSummary_SilentUnderQuiet mirrors every other
// summary writer's own --quiet check.
func TestWriteGaugeSelectionSummary_SilentUnderQuiet(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = true

	ctx := gaugeSelectionTestContext(t, true)
	r, err := render.New(ctx, gaugeSelectionTestLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSelectionSummary(c, r, ctx)
	if buf.String() != "" {
		t.Errorf("writeGaugeSelectionSummary printed something under --quiet: %q", buf.String())
	}
}

// TestWriteGaugeSelectionSummary_NilContextPrintsNothing mirrors the
// identical defensive check writeGaugeSummary's own test makes: this
// function must not be called with anything but the render's own non-nil
// Context in practice, but a nil one must not panic.
func TestWriteGaugeSelectionSummary_NilContextPrintsNothing(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSelectionTestContext(t, true)
	r, err := render.New(ctx, gaugeSelectionTestLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSelectionSummary(c, r, nil)
	if buf.String() != "" {
		t.Errorf("writeGaugeSelectionSummary printed something with a nil ctx: %q", buf.String())
	}
}

// --- the --gauge-style summary ----------------------------------------------

// gaugeSummaryTestContext builds a Context styled GaugeStyleTrack over a
// track carrying two gauge metrics with two different outcomes:
//
//   - HeartRate: 101 present readings ramping 100, 101, ..., 200 -- the same
//     ramp shape TestRobustGaugeScale_SnapsOutwardToStep and
//     TestReadoutGauge_InRangeFillsProportionally (internal/panel) already
//     derive floor=100/ceiling=200 from with step=10, reused here so this
//     test cannot silently disagree with what those pin about the same
//     arithmetic.
//   - Cadence: two present readings -- enough for Report.Carries (which
//     requires only Present > 0, so the panel is actually PLACED) but far
//     under Quantiles' own floor of ten, so Cadence's own gauge must report
//     "no usable range".
func gaugeSummaryTestContext(t *testing.T) *panel.Context {
	t.Helper()
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const n = 101
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time: start.Add(time.Duration(i) * time.Second),
			// +1 so the ramp never carries a recorded zero -- HeartRate's own
			// value() refuses one (see readout.go) -- and stays 100..200.
			HasHeartRate: true, HeartRate: uint8(100 + i),
		}
	}
	samples[0].HasCadence, samples[0].Cadence = true, 80
	samples[1].HasCadence, samples[1].Cadence = true, 82

	track := &fitactivity.Track{Samples: samples}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, 30, 1, nil)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: 1920, Height: 1080, FontScale: 0.05, Fonts: fonts,
		GaugeStyle: panel.GaugeStyleTrack,
	}
}

// TestWriteGaugeSummary_ReportsResolvedRangesAndFallback is this step's
// headline case: one gauge with a usable range and one without, in the SAME
// render, so the summary must name both -- a track silently missing from one
// gauge and not its neighbour is exactly the unexplained difference this
// line exists to report rather than leave for a viewer to notice on their
// own.
func TestWriteGaugeSummary_ReportsResolvedRangesAndFallback(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSummaryTestContext(t)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
		{Panel: panel.Cadence()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, ctx)

	out := buf.String()
	if !strings.HasPrefix(out, "gauges: track; ") {
		t.Fatalf("summary does not start with the expected prefix; got:\n%s", out)
	}
	if !strings.Contains(out, "heart rate 100-200 bpm") {
		t.Errorf("missing heart rate's resolved range; got:\n%s", out)
	}
	if !strings.Contains(out, "cadence no usable range - plain") {
		t.Errorf("missing cadence's fallback line; got:\n%s", out)
	}
}

// TestWriteGaugeSummary_ReportsResolvedRangesAndFallbackForDial is
// TestWriteGaugeSummary_ReportsResolvedRangesAndFallback's own analogue for
// GaugeStyleDial: the SAME range arithmetic (GaugeStyle's own doc comment,
// gauge.go) is reported, and the line's own prefix names "dial" rather than
// "track" -- panel.GaugeStyleName's whole reason for existing is so this
// line can never say "track" for a render that actually drew a dial.
func TestWriteGaugeSummary_ReportsResolvedRangesAndFallbackForDial(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSummaryTestContext(t)
	ctx.GaugeStyle = panel.GaugeStyleDial
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
		{Panel: panel.Cadence()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, ctx)

	out := buf.String()
	if !strings.HasPrefix(out, "gauges: dial; ") {
		t.Fatalf("summary does not start with the expected prefix; got:\n%s", out)
	}
	if !strings.Contains(out, "heart rate 100-200 bpm") {
		t.Errorf("missing heart rate's resolved range; got:\n%s", out)
	}
	if !strings.Contains(out, "cadence no usable range - plain") {
		t.Errorf("missing cadence's fallback line; got:\n%s", out)
	}
}

// TestWriteGaugeSummary_OmitsReadoutsWithNoGaugeVariant pins that a placed
// readout with no gauge variant at all (Distance, which only ever
// increases) is never reported here -- reporting it would misname a fact
// about that readout's own definition as a fact about this activity's
// range, which is exactly what HasGaugeRule exists to prevent (see its own
// doc comment).
func TestWriteGaugeSummary_OmitsReadoutsWithNoGaugeVariant(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSummaryTestContext(t)
	for i := range ctx.Track.Samples {
		ctx.Track.Samples[i].HasDistance = true
		ctx.Track.Samples[i].Distance = float64(i) * 10
	}
	ctx.Report = inspect.Build(ctx.Track)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
		{Panel: panel.Distance()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, ctx)
	if strings.Contains(buf.String(), "distance") {
		t.Errorf("distance (no gauge variant) appeared in the gauge summary: %q", buf.String())
	}
}

// TestWriteGaugeSummary_PlainStylePrintsNothing pins that an ordinary
// GaugeStylePlain render -- today's default -- prints nothing here at all,
// the same discipline writeHighlightSummary and writeElevationSummary apply
// to their own features, so a render that never asked for a gauge track
// sees a summary identical to one from before this flag existed.
func TestWriteGaugeSummary_PlainStylePrintsNothing(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSummaryTestContext(t)
	ctx.GaugeStyle = panel.GaugeStylePlain
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, ctx)
	if buf.Len() != 0 {
		t.Errorf("writeGaugeSummary printed something under GaugeStylePlain: %q", buf.String())
	}
}

// TestWriteGaugeSummary_QuietPrintsNothing follows every other summary
// writer's own --quiet guard.
func TestWriteGaugeSummary_QuietPrintsNothing(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = true

	ctx := gaugeSummaryTestContext(t)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, ctx)
	if buf.Len() != 0 {
		t.Errorf("writeGaugeSummary printed something under --quiet: %q", buf.String())
	}
}

// TestWriteGaugeSummary_NilContextPrintsNothing guards the defensive nil
// check: writeGaugeSummary is always called with the render's own non-nil
// Context in production, but a nil one must not panic through
// Readout.GaugeRangeText.
func TestWriteGaugeSummary_NilContextPrintsNothing(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := gaugeSummaryTestContext(t)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.HeartRate()},
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeGaugeSummary(c, r, nil)
	if buf.Len() != 0 {
		t.Errorf("writeGaugeSummary printed something with a nil ctx: %q", buf.String())
	}
}

// TestWritePanelSummary_ANameThatAlsoDrewIsNotReportedAsMissingData pins the
// one rule that keeps the two summary lines from contradicting each other.
//
// --gauges balance seats pace in the balance branch as well as in the metrics
// branch. When a file carries none of the four balance metrics the balance
// branch loses the Alt, so that copy of pace declines while the metrics
// branch's own pace draws -- and the summary printed `pace` under "carries no
// such data" on the very same render whose `panels:` line listed pace as
// drawn. Both statements cannot be true, and the false one is the decline:
// the activity plainly carries pace, because a pace panel just drew from it.
//
// The rule under test is about the two lists rather than about pace, so it
// keeps holding for the next panel seated in two branches. The fallback is
// still explained -- writeGaugeSelectionSummary says balance was requested and
// the activity has none -- so nothing is silently dropped here, only moved out
// of a heading that would be lying about it.
func TestWritePanelSummary_ANameThatAlsoDrewIsNotReportedAsMissingData(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	ctx := summaryTestContext(t, nil)
	layout := panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: summaryAccepter("pace")},  // the metrics branch's copy: draws
		{Panel: summaryDecliner("pace")},  // the balance branch's copy: declines
		{Panel: summaryDecliner("power")}, // a genuine absence, must survive
	}}}
	r, err := render.New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writePanelSummary(c, r, ctx.Timeline, layout.Name, "dark", panel.Smoothing{})
	out := buf.String()

	if !strings.Contains(out, "panels: pace") {
		t.Fatalf("the drawn pace panel is missing from the panels line; got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "declined (this activity carries no such data):") {
			continue
		}
		for _, n := range strings.Split(strings.SplitN(line, ": ", 2)[1], ", ") {
			if n == "pace" {
				t.Errorf("a name that also drew was reported as missing data; got:\n%s", out)
			}
		}
	}
	// The filter must be surgical: a panel that genuinely declined and did
	// NOT draw is still the user's news, and swallowing it would trade one
	// dishonest line for a silent one.
	if !strings.Contains(out, "declined (this activity carries no such data): power") {
		t.Errorf("a genuine decline was swallowed along with the Alt loser; got:\n%s", out)
	}
}

// --- --clock and --pauses ---------------------------------------------------

// TestParseClockAndPauses_RefuseUnknownValues mirrors
// TestParseBottomBand_RefusesUnknownValues for the two new flags: every legal
// value round-trips, and a typo is refused where the user typed it rather
// than costing a whole render.
func TestParseClockAndPauses_RefuseUnknownValues(t *testing.T) {
	for _, c := range clocks {
		got, err := parseClock(c)
		if err != nil || got != c {
			t.Errorf("parseClock(%q) = %q, %v", c, got, err)
		}
	}
	if _, err := parseClock("moving"); err == nil {
		t.Error("parseClock accepted an unknown value")
	}

	for _, p := range pauseModes {
		got, err := parsePauses(p)
		if err != nil || got != p {
			t.Errorf("parsePauses(%q) = %q, %v", p, got, err)
		}
	}
	if _, err := parsePauses("cut"); err == nil {
		t.Error("parsePauses accepted an unknown value")
	}
}

// TestResolveSpeedup_SkipCompressesTheRunningTimeNotTheElapsedSpan pins what
// --video-duration means once the pauses are gone.
//
// A user asking for a one-minute video of an activity with four minutes of
// rest stops wants a one-minute video. Compressing the elapsed span while
// rendering only the running time produces a video short by exactly the
// pauses -- an error nothing on screen explains, and one that grows with how
// much the person stopped.
func TestResolveSpeedup_SkipCompressesTheRunningTimeNotTheElapsedSpan(t *testing.T) {
	const (
		elapsed = 30 * time.Minute
		paused  = 4 * time.Minute
		video   = time.Minute
	)
	timer := syntheticTimer(elapsed, [2]time.Duration{10 * time.Minute, 14 * time.Minute})
	if got := timer.PausedTotal(); got != paused {
		t.Fatalf("precondition: the fixture pauses for %v, want %v", got, paused)
	}

	cmd := &cobra.Command{}
	bindRenderFlags(cmd)
	if err := cmd.Flags().Set("video-duration", video.String()); err != nil {
		t.Fatal(err)
	}
	renderOpts.videoDur = video

	frozen, err := resolveSpeedup(cmd, timer, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveSpeedup(freeze): %v", err)
	}
	skipped, err := resolveSpeedup(cmd, timer, panel.PausesSkip)
	if err != nil {
		t.Fatalf("resolveSpeedup(skip): %v", err)
	}

	if want := panel.SpeedupFor(elapsed, video); frozen != want {
		t.Errorf("freeze resolved %v, want %v (the whole elapsed span)", frozen, want)
	}
	if want := panel.SpeedupFor(elapsed-paused, video); skipped != want {
		t.Errorf("skip resolved %v, want %v (the running time alone)", skipped, want)
	}
	if skipped >= frozen {
		t.Error("skipping asked for at least as much compression as freezing; it renders less activity, so it needs less")
	}
}

// TestResolveHighlights_RefusesAHighlightInsideASkippedPause covers the one
// combination of the two features that has no coherent answer.
//
// Under --pauses freeze the highlight is rendered and reported as a warning:
// the dashboard is frozen through it, which is odd but is what the recording
// says. Under --pauses skip every instant it names is gone from the render,
// so there is no video for it to pace, name or mark -- and the Timeline's own
// floor-at-one-frame rule cannot save it, because there is no segment left to
// floor.
func TestResolveHighlights_RefusesAHighlightInsideASkippedPause(t *testing.T) {
	timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 14 * time.Minute})
	raw := []string{"from=11m,to=13m,name=Rest stop"}

	got, err := resolveHighlights(raw, timer, panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("freeze refused a highlight it should merely warn about: %v", err)
	}
	if len(got) != 1 || !got[0].PausedThroughout {
		t.Fatalf("freeze resolved %v; the highlight should be marked PausedThroughout and kept", got)
	}

	_, err = resolveHighlights(raw, timer, panel.HighlightStyleBorder, panel.PausesSkip)
	if err == nil {
		t.Fatal("skip accepted a highlight whose every instant it removes; it would occupy no video at all")
	}
	if !strings.Contains(err.Error(), "Rest stop") {
		t.Errorf("the error does not name the highlight: %v", err)
	}
}

// TestHighlightLiesInPause_ReadsTheListRatherThanSamplingIt is the regression
// test for what this check used to be.
//
// It used to sample Paused at nine evenly spaced points, which was good
// enough while the answer was only a warning. It is not good enough now that
// a true answer REFUSES the render: the fixture below has a one-second
// running gap between two pauses, positioned so that no point of that grid
// lands in it, so the old check reported a span containing real activity as
// wholly paused -- and a user would have been told a highlight they could see
// was fine could not be rendered.
func TestHighlightLiesInPause_ReadsTheListRatherThanSamplingIt(t *testing.T) {
	// Nine samples across [10m, 11m) land every 7.5s; the gap at 10m32s-10m33s
	// falls between two of them.
	timer := syntheticTimer(30*time.Minute,
		[2]time.Duration{10 * time.Minute, 10*time.Minute + 32*time.Second},
		[2]time.Duration{10*time.Minute + 33*time.Second, 11 * time.Minute},
	)
	start, _ := timer.Window()

	if highlightLiesInPause(timer, start, 10*time.Minute, 11*time.Minute) {
		t.Error("a span containing a second of real activity was reported as wholly paused; " +
			"the check is sampling rather than reading the pause list")
	}
	// The genuine case still answers yes, on the exact bounds of one pause.
	if !highlightLiesInPause(timer, start, 10*time.Minute, 10*time.Minute+32*time.Second) {
		t.Error("a span exactly matching a pause was not reported as paused")
	}
	// And a span one nanosecond wider than the pause is not inside it.
	if highlightLiesInPause(timer, start, 10*time.Minute-time.Nanosecond, 10*time.Minute+32*time.Second) {
		t.Error("a span starting before the pause was reported as inside it")
	}
	// A span with no pause anywhere near it.
	if highlightLiesInPause(timer, start, 20*time.Minute, 21*time.Minute) {
		t.Error("a span with no pause in it was reported as paused")
	}
}

// TestWritePauseSummary_ReportsEachWayTheFlagCanSurprise covers the branches
// that exist because the flag can do something other than what a user assumes
// from having typed it. Reporting only the happy case would leave all of them
// looking like a flag that worked.
func TestWritePauseSummary_ReportsEachWayTheFlagCanSurprise(t *testing.T) {
	run := func(t *testing.T, ctx *panel.Context, in renderInputs) string {
		t.Helper()
		renderOpts.quiet = false
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)
		writePauseSummary(c, ctx, in)
		return buf.String()
	}

	newTimeline := func(t *testing.T, timer *fitactivity.TimerModel, mode string) panel.Timeline {
		t.Helper()
		tl, err := panel.NewTimelineForActivityWithPauses(timer, 30, 60, nil, mode)
		if err != nil {
			t.Fatalf("NewTimelineForActivityWithPauses: %v", err)
		}
		return tl
	}

	t.Run("silent under freeze", func(t *testing.T) {
		timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 14 * time.Minute})
		ctx := &panel.Context{Timer: timer, Pauses: panel.PausesFreeze}
		if got := run(t, ctx, renderInputs{tl: newTimeline(t, timer, panel.PausesFreeze)}); got != "" {
			t.Errorf("a freeze render printed %q, want nothing", got)
		}
	})

	t.Run("a mid-activity pause is reported as a seam", func(t *testing.T) {
		timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 14 * time.Minute})
		ctx := &panel.Context{Timer: timer, Pauses: panel.PausesSkip}
		got := run(t, ctx, renderInputs{tl: newTimeline(t, timer, panel.PausesSkip)})
		if !strings.Contains(got, "1 seam") || !strings.Contains(got, panel.FormatClock(4*time.Minute)) {
			t.Errorf("summary = %q, want one seam of %s", got, panel.FormatClock(4*time.Minute))
		}
	})

	t.Run("a trailing pause is reported separately from the seams", func(t *testing.T) {
		// Two removals: one mid-activity (a seam) and one running to the
		// activity's own end (no seam to mark). The two figures must not be
		// conflated, or the notices on screen would not add up to the total.
		timer := syntheticTimer(30*time.Minute,
			[2]time.Duration{10 * time.Minute, 14 * time.Minute},
			[2]time.Duration{28 * time.Minute, 30 * time.Minute},
		)
		ctx := &panel.Context{Timer: timer, Pauses: panel.PausesSkip}
		got := run(t, ctx, renderInputs{tl: newTimeline(t, timer, panel.PausesSkip)})
		if !strings.Contains(got, "1 seam") {
			t.Errorf("summary = %q, want exactly one seam", got)
		}
		if !strings.Contains(got, "a further "+panel.FormatClock(2*time.Minute)) {
			t.Errorf("summary = %q, want the %s trailing pause reported on its own", got, panel.FormatClock(2*time.Minute))
		}
	})

	t.Run("an activity that never stopped says so", func(t *testing.T) {
		timer := syntheticTimer(30 * time.Minute)
		ctx := &panel.Context{Timer: timer, Pauses: panel.PausesSkip}
		got := run(t, ctx, renderInputs{tl: newTimeline(t, timer, panel.PausesSkip)})
		if !strings.Contains(got, "never stopped") {
			t.Errorf("summary = %q, want it to say the activity never stopped", got)
		}
	})

	t.Run("a file with no timer events says that instead", func(t *testing.T) {
		// Not the same fact as "never stopped": nothing was looked at. A
		// file with no timer events cannot locate a pause at all, so
		// reporting it as an activity that ran straight through would be
		// claiming a measurement that was never made.
		track := &fitactivity.Track{Timing: fitactivity.ActivityTiming{
			Start: highlightEpoch, TotalElapsed: 30 * time.Minute, HasTotals: true,
		}}
		timer := fitactivity.BuildTimerModel(track)
		if timer.HasTimerEvents() {
			t.Fatal("precondition: this fixture must carry no timer events")
		}
		ctx := &panel.Context{Timer: timer, Pauses: panel.PausesSkip}
		got := run(t, ctx, renderInputs{tl: newTimeline(t, timer, panel.PausesSkip)})
		if !strings.Contains(got, "no timer events") {
			t.Errorf("summary = %q, want it to say the file carries no timer events", got)
		}
	})
}

// TestWriteClockSummary_NamesTheOrderOnlyWhenItIsNotTheDefault keeps a render
// that does not use --clock printing exactly the summary it always has.
func TestWriteClockSummary_NamesTheOrderOnlyWhenItIsNotTheDefault(t *testing.T) {
	run := func(clock string) string {
		renderOpts.quiet = false
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetErr(&buf)
		writeClockSummary(c, &panel.Context{Clock: clock})
		return buf.String()
	}
	if got := run(panel.ClockElapsed); got != "" {
		t.Errorf("the default order printed %q, want nothing", got)
	}
	if got := run(""); got != "" {
		t.Errorf("an unset Clock printed %q, want nothing", got)
	}
	if got := run(panel.ClockActive); !strings.Contains(got, panel.ClockActive) {
		t.Errorf("--clock active printed %q, want it to name the order", got)
	}
}

// TestFrameIndices_FrameAtIsBoundedByTheActivityNotTheRender is the
// regression test for what --pauses skip breaks if the bound is taken from
// the wrong figure.
//
// The offset a user types into --frame-at is elapsed time into the ACTIVITY.
// Under skip the frames cover less than the activity ran, so bounding
// against Timeline.ActivityDuration refuses an offset that is plainly inside
// the recording -- and the error message quotes a length the user has never
// seen anywhere.
func TestFrameIndices_FrameAtIsBoundedByTheActivityNotTheRender(t *testing.T) {
	timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 14 * time.Minute})
	tl, err := panel.NewTimelineForActivityWithPauses(timer, 30, 60, nil, panel.PausesSkip)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithPauses: %v", err)
	}
	if tl.ActivitySpan() <= tl.ActivityDuration() {
		t.Fatal("precondition: skipping must leave the render covering less than the activity ran")
	}

	// Inside the activity, past the rendered duration.
	at := 28 * time.Minute
	if at <= tl.ActivityDuration() {
		t.Fatalf("precondition: %v must exceed the rendered duration %v for this test to mean anything", at, tl.ActivityDuration())
	}
	if _, err := frameIndices(tl, tl.Frames()-1, []time.Duration{at}, nil, nil, nil); err != nil {
		t.Errorf("--frame-at %v was refused on a 30-minute activity: %v", at, err)
	}
	// And genuinely past the end is still refused.
	if _, err := frameIndices(tl, tl.Frames()-1, []time.Duration{31 * time.Minute}, nil, nil, nil); err == nil {
		t.Error("--frame-at past the activity's own end was accepted")
	}
}

// TestFrameIndices_EachSeamIsALandmark keeps the fast visual loop showing the
// one thing --pauses skip adds to the frame: the splice, and the notice that
// names it.
func TestFrameIndices_EachSeamIsALandmark(t *testing.T) {
	timer := syntheticTimer(30*time.Minute,
		[2]time.Duration{10 * time.Minute, 14 * time.Minute},
		[2]time.Duration{20 * time.Minute, 21 * time.Minute},
	)
	tl, err := panel.NewTimelineForActivityWithPauses(timer, 30, 60, nil, panel.PausesSkip)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithPauses: %v", err)
	}
	cuts := tl.Cuts()
	if len(cuts) != 2 {
		t.Fatalf("precondition: two seams expected, got %d", len(cuts))
	}

	got, err := frameIndices(tl, tl.Frames()-1, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	for _, c := range cuts {
		if !slices.Contains(got, c.FirstFrame) {
			t.Errorf("frame %d, where the splice lands, is not a landmark", c.FirstFrame)
		}
		mid := (c.FirstFrame + c.LastFrame) / 2
		if !slices.Contains(got, mid) {
			t.Errorf("frame %d, the middle of the seam's notice, is not a landmark", mid)
		}
	}
}
