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
)

// mergeBase is an arbitrary round instant for the fixtures below to hang off.
// It is not a real recording's window, and nothing here depends on the value.
var mergeBase = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)

// mergePieceSpan is the wall-clock span of one fixture built by mergePiece:
// Count-1 seconds, per fittest.Options.Count's own doc comment.
const mergePieceSpan = 119 * time.Second

// mergePiece writes one leg of a split workout and returns its path. 120
// records is enough for a timeline and an elevation model, and small enough
// that three of them render frames in well under a second.
func mergePiece(t *testing.T, dir, name string, start time.Time) string {
	t.Helper()
	opts := fittest.DefaultOptions()
	opts.Start = start
	opts.Count = 120
	path := filepath.Join(dir, name)
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating %s: %v", name, err)
	}
	return path
}

// runCLI drives one command end to end through cobra, exactly as Execute
// does, and returns its stdout and stderr. renderOpts is package state, so
// every caller restores it -- see the existing render tests.
func runCLI(t *testing.T, run func(*cobra.Command, []string) error, bindFlags bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	defer func(o renderOptions) { renderOpts = o }(renderOpts)
	renderOpts = renderOptions{}

	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{
		Use: "test", Args: cobra.MinimumNArgs(1), RunE: run,
		SilenceUsage: true, SilenceErrors: true,
	}
	if bindFlags {
		bindRenderFlags(cmd)
	}
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

// TestRunRender_MergesSeveralActivitiesIntoOneRender is the end-to-end case
// this feature exists for: a race recorded as its own file partway through a
// long run, rendered as the one afternoon it was.
//
// The files are passed deliberately out of order, because argument order is
// whatever a shell glob produced -- `fitdash *.fit` hands them over
// alphabetically, which for warmup/race/cooldown is not the order they
// happened in. The render must span the whole workout regardless.
func TestRunRender_MergesSeveralActivitiesIntoOneRender(t *testing.T) {
	dir := t.TempDir()
	warmup := mergePiece(t, dir, "warmup.fit", mergeBase)
	race := mergePiece(t, dir, "race.fit", mergeBase.Add(10*time.Minute))
	cooldown := mergePiece(t, dir, "cooldown.fit", mergeBase.Add(20*time.Minute))

	// Alphabetical, as a glob would give them: cooldown, race, warmup.
	stdout, stderr, err := runCLI(t, runRender, true,
		"--frames", "--output-dir", t.TempDir(), "--size", "640x360",
		cooldown, race, warmup)
	if err != nil {
		t.Fatalf("render of three activities: %v\nstderr:\n%s", err, stderr)
	}
	if stdout == "" {
		t.Fatal("no frames written")
	}

	// Announced, not silent: every figure the render prints describes an
	// activity that exists nowhere on disk.
	if !strings.Contains(stderr, "merged 3 files into one activity") {
		t.Errorf("the merge was not announced:\n%s", stderr)
	}

	// Both gaps found, and the render's timeline spans them: ten minutes
	// between one file's start and the next, less the 119s that file
	// covered, twice over. A render that decoded only args[0] would still
	// succeed and would print none of this, which is why the assertion is on
	// the resolved duration rather than on the exit status.
	//
	// The figure is read off the same TimerModel the timeline freezes
	// through, so it also pins that the gaps became real pauses rather than
	// a stretch the dashboard would animate across.
	if want := "0:16:02 not recorded across 2 pauses"; !strings.Contains(stderr, want) {
		t.Errorf("summary does not report both %v gaps (looking for %q):\n%s",
			10*time.Minute-mergePieceSpan, want, stderr)
	}
	// Named in the order they happened, not the order they were typed.
	warmupAt, cooldownAt := strings.Index(stderr, warmup), strings.Index(stderr, cooldown)
	if warmupAt < 0 || cooldownAt < 0 {
		t.Fatalf("summary does not name every file:\n%s", stderr)
	}
	if warmupAt > cooldownAt {
		t.Errorf("summary lists %s before %s -- files must be reported in time order, not argument order:\n%s",
			cooldown, warmup, stderr)
	}
	// Said outright, because it is the part a reader would not predict: the
	// ground covered between two recordings was never measured, so the
	// merged distance leaves it out rather than guessing.
	if !strings.Contains(stderr, "no distance added") {
		t.Errorf("the summary does not say the gaps add no distance:\n%s", stderr)
	}
}

// TestRunRender_RefusesOverlappingActivities pins the refusal at the CLI
// boundary, where a user meets it. Merging two recordings of the same stretch
// would double the reported distance, and a rendered frame of a run that says
// 24km instead of 12km looks exactly as convincing as a correct one -- so this
// has to fail loudly, before any pixel is drawn.
func TestRunRender_RefusesOverlappingActivities(t *testing.T) {
	dir := t.TempDir()
	first := mergePiece(t, dir, "first.fit", mergeBase)

	cases := []struct {
		name string
		args []string
	}{
		{"the same file twice", []string{first, first}},
		{"two recordings of one stretch", []string{first, mergePiece(t, dir, "watch.fit", mergeBase.Add(time.Minute))}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--frames", "--output-dir", t.TempDir()}, c.args...)
			_, _, err := runCLI(t, runRender, true, args...)
			if err == nil {
				t.Fatal("rendered two overlapping activities; want a refusal")
			}
			if !strings.Contains(err.Error(), "overlap") {
				t.Errorf("error should say the activities overlap; got: %v", err)
			}
			// The refusal is useless if it does not say which files.
			for _, want := range c.args {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %s; got: %v", want, err)
				}
			}
		})
	}
}

// TestRunRender_OneActivityStillWorks is the control. Every render in this
// program now goes through the merge path, so the single-file case -- which
// is every other test in this package and every real invocation until now --
// has to be proven unchanged by it rather than assumed.
func TestRunRender_OneActivityStillWorks(t *testing.T) {
	path := mergePiece(t, t.TempDir(), "activity.fit", mergeBase)

	stdout, stderr, err := runCLI(t, runRender, true,
		"--frames", "--output-dir", t.TempDir(), "--size", "640x360", path)
	if err != nil {
		t.Fatalf("render of one activity: %v\nstderr:\n%s", err, stderr)
	}
	if stdout == "" {
		t.Fatal("no frames written")
	}
	// Nothing about merging is printed for one file: the path is already on
	// the command line, so restating it is noise.
	if strings.Contains(stderr, "merged") {
		t.Errorf("a single-file render announced a merge:\n%s", stderr)
	}
}

// TestRunInspect_ReportsTheMergedActivity pins that inspect and the render
// agree. inspect is this project's single source of truth for whether an
// activity carries a metric, and a panel asks it rather than walking samples
// itself -- so a report built from one file of a merge would answer a
// question about an activity the render is not going to draw.
func TestRunInspect_ReportsTheMergedActivity(t *testing.T) {
	dir := t.TempDir()
	first := mergePiece(t, dir, "first.fit", mergeBase)
	second := mergePiece(t, dir, "second.fit", mergeBase.Add(10*time.Minute))

	stdout, _, err := runCLI(t, runInspect, false, second, first)
	if err != nil {
		t.Fatalf("inspect of two activities: %v", err)
	}

	if !strings.Contains(stdout, "2 files merged into one activity") {
		t.Errorf("inspect did not report the merge:\n%s", stdout)
	}
	for _, want := range []string{first, second} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect does not name %s:\n%s", want, stdout)
		}
	}
	// 240 records, not 120: a report on args[0] alone would still look
	// entirely plausible here.
	if !strings.Contains(stdout, "samples  240") {
		t.Errorf("inspect reports the wrong sample count for the merged activity:\n%s", stdout)
	}
	// Active excludes the ten-minute gap between the recordings: 2x119s.
	if !strings.Contains(stdout, "active   3m58s") {
		t.Errorf("inspect counted the gap between files as moving time:\n%s", stdout)
	}
	// The window spans both files and the gap between them, because that is
	// the window the renderer lays its timeline over.
	if !strings.Contains(stdout, "elapsed  11m59s") {
		t.Errorf("inspect reports the wrong elapsed span for the merged activity:\n%s", stdout)
	}
}

// TestOutputPath_NamesAMergeAfterItsEarliestFile pins where the rendered
// video lands when several activities went into it.
//
// The name comes from Track.SourcePath, which for a merge is the file the
// activity STARTS in -- not args[0]. The two differ exactly when it matters:
// `fitdash *.fit` hands the files over alphabetically, so naming the output
// after the first argument would call a run that began with the warm-up
// "cooldown.mp4". Asserted here rather than through a real render because
// encoding a video to check a filename needs ffmpeg and a minute.
func TestOutputPath_NamesAMergeAfterItsEarliestFile(t *testing.T) {
	dir := t.TempDir()
	warmup := mergePiece(t, dir, "warmup.fit", mergeBase)
	cooldown := mergePiece(t, dir, "cooldown.fit", mergeBase.Add(20*time.Minute))

	// Alphabetical, as a glob gives them: cooldown first.
	track, err := fitactivity.DecodeAll(cooldown, warmup)
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}

	out, err := outputPath(track.SourcePath, "", t.TempDir(), ".mp4")
	if err != nil {
		t.Fatalf("outputPath: %v", err)
	}
	if got, want := filepath.Base(out), "warmup.mp4"; got != want {
		t.Errorf("output named %q, want %q (the file the activity starts in)", got, want)
	}
}
