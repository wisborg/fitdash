package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
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

	got, err := frameIndices(tl, nil)
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
	got, err = frameIndices(tl, []time.Duration{50 * time.Second})
	if err != nil {
		t.Fatalf("frameIndices: %v", err)
	}
	if last := got[len(got)-1]; last != 1500 {
		t.Errorf("--frame-at 50s resolved to frame %d, want 1500", last)
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

	if _, err := frameIndices(tl, []time.Duration{40 * time.Minute}); err == nil {
		t.Fatal("frameIndices accepted an offset past the end of the activity")
	} else if !strings.Contains(err.Error(), "0:25:00") {
		t.Errorf("the error should say how long the activity runs; got: %v", err)
	}
	if _, err := frameIndices(tl, []time.Duration{-time.Minute}); err == nil {
		t.Error("frameIndices accepted a negative offset")
	}

	// The boundary is inclusive: the very last instant of the activity is a
	// legitimate thing to ask for.
	if _, err := frameIndices(tl, []time.Duration{25 * time.Minute}); err != nil {
		t.Errorf("frameIndices rejected the activity's final instant: %v", err)
	}

	// And the offset is ACTIVITY time, so a sped-up render accepts offsets far
	// beyond the video's own length.
	fast, err := panel.NewTimeline(time.Now(), 25*time.Minute, 30, 25)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := frameIndices(fast, []time.Duration{20 * time.Minute}); err != nil {
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
