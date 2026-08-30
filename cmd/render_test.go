package cmd

import (
	"bytes"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/render"
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
func TestOutputPath_DerivesFromTheActivityAndRefusesToClobber(t *testing.T) {
	dir := t.TempDir()

	got, err := outputPath("/some/where/2026-08-01 Morning Run.fit", "", dir, ".mp4")
	if err != nil {
		t.Fatalf("outputPath: %v", err)
	}
	if want := filepath.Join(dir, "2026-08-01 Morning Run.mp4"); got != want {
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
	_, err = outputPath("/some/where/2026-08-01 Morning Run.fit", "", dir, ".mp4")
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

	got, err := frameIndices(tl, nil, nil, nil, nil)
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
	got, err = frameIndices(tl, []time.Duration{50 * time.Second}, nil, nil, nil)
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
	got, err := frameIndices(tl, nil, []time.Duration{5 * time.Second}, nil, nil)
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	if last := got[len(got)-1]; last != 150 {
		t.Errorf("--frame-at-video 5s resolved to frame %d, want 150", last)
	}

	if _, err := frameIndices(tl, nil, []time.Duration{-time.Second}, nil, nil); err == nil {
		t.Error("frameIndices accepted a negative --frame-at-video offset")
	}
	if _, err := frameIndices(tl, nil, []time.Duration{11 * time.Second}, nil, nil); err == nil {
		t.Error("frameIndices accepted a --frame-at-video offset past the end of the video")
	} else if !strings.Contains(err.Error(), "0:00:10") {
		t.Errorf("the error should say how long the video runs; got: %v", err)
	}
	// The video's own last instant is a legitimate thing to ask for.
	if _, err := frameIndices(tl, nil, []time.Duration{10 * time.Second}, nil, nil); err != nil {
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
	got, err := frameIndices(tl, nil, nil, []panel.Highlight{
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

	if _, err := frameIndices(tl, []time.Duration{40 * time.Minute}, nil, nil, nil); err == nil {
		t.Fatal("frameIndices accepted an offset past the end of the activity")
	} else if !strings.Contains(err.Error(), "0:25:00") {
		t.Errorf("the error should say how long the activity runs; got: %v", err)
	}
	if _, err := frameIndices(tl, []time.Duration{-time.Minute}, nil, nil, nil); err == nil {
		t.Error("frameIndices accepted a negative offset")
	}

	// The boundary is inclusive: the very last instant of the activity is a
	// legitimate thing to ask for.
	if _, err := frameIndices(tl, []time.Duration{25 * time.Minute}, nil, nil, nil); err != nil {
		t.Errorf("frameIndices rejected the activity's final instant: %v", err)
	}

	// And the offset is ACTIVITY time, so a sped-up render accepts offsets far
	// beyond the video's own length.
	fast, err := panel.NewTimeline(time.Now(), 25*time.Minute, 30, 25)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := frameIndices(fast, []time.Duration{20 * time.Minute}, nil, nil, nil); err != nil {
		t.Errorf("a 20m offset was rejected on a 25m activity rendered as a 1m video: %v", err)
	}
}

// TestNewReporter_QuietReturnsNil pins how --quiet is implemented.
//
// A nil *progress.Reporter is safe to call, so quiet is one decision here
// rather than a condition at every call site -- and the render loop's progress
// callback stays unconditional.
func TestNewReporter_QuietReturnsNil(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)

	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)

	renderOpts.quiet = false
	if newReporter(cmd, 100) == nil {
		t.Error("a reporter was expected without --quiet")
	}

	renderOpts.quiet = true
	r := newReporter(cmd, 100)
	if r != nil {
		t.Error("--quiet should produce no reporter")
	}
	// The nil must be usable, or every call site needs a guard.
	r.Update(1)
	r.Done()
}

// TestNewReporter_UsesInlineOnlyForATerminal keeps a redirected stderr from
// collecting one enormous line full of carriage returns.
func TestNewReporter_UsesInlineOnlyForATerminal(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	// A bytes.Buffer is not an *os.File at all, so inline must be off.
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&buf)
	rep := newReporter(cmd, 10)
	if rep == nil {
		t.Fatal("no reporter")
	}
	rep.Update(0)
	rep.Update(5)
	if strings.Contains(buf.String(), "\r") {
		t.Error("progress to a non-terminal used a carriage return")
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

			got, err := resolveSpeedup(cmd, timer)
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

// TestValidateRenderOptions_RejectsFlagsThatCannotMeanWhatTheySay covers two
// review findings, both cases of a flag quietly doing something other than
// what its help text promised.
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

	writeHighlightSummary(c, tl, nil, []panel.Highlight{highlight}, panel.Smoothing{}, panel.DefaultTheme())

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
	writeHighlightSummary(c, tl, nil, highlights, panel.Smoothing{}, panel.DefaultTheme())

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
	writeHighlightSummary(c, tl, nil, highlights, panel.Smoothing{}, theme)

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
// half of the third row in the plan's absent-data table for route marking: a
// highlight whose span has no GPS fix anywhere near it cannot be drawn on the
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
	writeHighlightSummary(c, tl, track, highlights, panel.Smoothing{}, panel.DefaultTheme())

	out := buf.String()
	if strings.Contains(out, `"Covered" has no GPS fixes`) {
		t.Errorf("a highlight entirely inside the GPS-covered stretch was reported as unmarkable; got:\n%s", out)
	}
	if !strings.Contains(out, `highlight "Lost signal" has no GPS fixes; it is not marked on the route`) {
		t.Errorf("summary is missing the unmarkable-highlight line for a highlight past the last GPS fix; got:\n%s", out)
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
	writeHighlightSummary(c, tl, nil, nil, panel.Smoothing{}, panel.DefaultTheme())
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
