package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/encode"
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

// TestDecodeActivities_ReportsWhereEachFileStarts pins the offsets the merge
// summary prints, and the reason they exist: once five files are one
// timeline, "the race" is a stretch some way into it rather than a file, and
// marking it up with --highlight or --label means knowing where each leg
// begins.
//
// The files are passed shuffled, because the offsets have to come out in the
// merged activity's order rather than the caller's.
func TestDecodeActivities_ReportsWhereEachFileStarts(t *testing.T) {
	dir := t.TempDir()
	warmup := mergePiece(t, dir, "warmup.fit", mergeBase)
	race := mergePiece(t, dir, "race.fit", mergeBase.Add(10*time.Minute))
	cooldown := mergePiece(t, dir, "cooldown.fit", mergeBase.Add(20*time.Minute))

	_, sources, err := decodeActivities([]string{cooldown, warmup, race})
	if err != nil {
		t.Fatalf("decodeActivities: %v", err)
	}

	want := []activitySource{
		{Path: warmup, Offset: 0},
		{Path: race, Offset: 10 * time.Minute},
		{Path: cooldown, Offset: 20 * time.Minute},
	}
	if !reflect.DeepEqual(sources, want) {
		t.Errorf("sources = %v, want %v", sources, want)
	}
}

// TestDecodeActivities_OffsetsArePastableIntoTheFlags is the property that
// makes the offsets worth printing at all. A summary that reported "0:19:43"
// would still have to be converted by hand before --label at= would take it,
// which is exactly the work this is meant to remove -- so the rendered line
// has to parse as the Go duration those flags accept.
func TestDecodeActivities_OffsetsArePastableIntoTheFlags(t *testing.T) {
	dir := t.TempDir()
	first := mergePiece(t, dir, "first.fit", mergeBase)
	second := mergePiece(t, dir, "second.fit", mergeBase.Add(19*time.Minute+43*time.Second))

	_, sources, err := decodeActivities([]string{first, second})
	if err != nil {
		t.Fatalf("decodeActivities: %v", err)
	}

	lines := mergedSourceLines(sources)
	if len(lines) != 2 {
		t.Fatalf("mergedSourceLines returned %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[1], "19m43s") {
		t.Errorf("line %q does not carry the offset as a Go duration", lines[1])
	}
	// The literal a user would copy, parsed the way the flag parses it.
	for _, line := range lines {
		offset := strings.Fields(line)[0]
		if _, err := time.ParseDuration(offset); err != nil {
			t.Errorf("offset %q from %q is not a duration --label at= would accept: %v", offset, line, err)
		}
	}
}

// TestRunRender_DryRunWritesNothingButReportsEverything covers the flag's two
// halves together, because either alone would pass while the feature was
// broken: a dry run that printed the summary but still encoded, or one that
// wrote nothing and reported nothing, would each satisfy half this test.
func TestRunRender_DryRunWritesNothingButReportsEverything(t *testing.T) {
	dir := t.TempDir()
	first := mergePiece(t, dir, "first.fit", mergeBase)
	second := mergePiece(t, dir, "second.fit", mergeBase.Add(10*time.Minute))
	outDir := t.TempDir()

	stdout, stderr, err := runCLI(t, runRender, true,
		"--dry-run", "--output-dir", outDir, "--size", "640x360", "--video-duration", "10s",
		"--label", "at=10m,name=Second leg",
		first, second)
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}

	// Nothing written, and nothing on stdout: runVideo puts the output path
	// there so it can be piped, and a dry run must not hand a script a path
	// to a file that was never created.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("reading output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d file(s) into the output directory, want none: %v", len(entries), entries)
	}
	if stdout != "" {
		t.Errorf("dry run wrote to stdout, which is where a real render puts the path it created: %q", stdout)
	}

	// It says so, names the destination, and still produces the summary the
	// flag exists to show -- including the label, which is the thing being
	// checked in the workflow this serves.
	for _, want := range []string{
		"dry run",
		"would write",
		filepath.Join(outDir, "first.mp4"),
		"merged 2 files into one activity",
		"panels:",
		"Second leg",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dry run summary is missing %q:\n%s", want, stderr)
		}
	}
}

// TestRunRender_DryRunRefusesQuiet pins the contradiction. --dry-run's entire
// output is the summary and --quiet suppresses the summary, so together they
// resolve the whole render and then print nothing at all.
func TestRunRender_DryRunRefusesQuiet(t *testing.T) {
	path := mergePiece(t, t.TempDir(), "activity.fit", mergeBase)

	_, _, err := runCLI(t, runRender, true, "--dry-run", "--quiet", "--output-dir", t.TempDir(), path)
	if err == nil {
		t.Fatal("accepted --dry-run with --quiet")
	}
	if !strings.Contains(err.Error(), "--dry-run") || !strings.Contains(err.Error(), "--quiet") {
		t.Errorf("error should name both flags; got: %v", err)
	}
}

// TestRunRender_DryRunHasNoFilesystemSideEffects pins that a dry run does not
// quietly do the parts of the job that are not the encode. outputPath creates
// the output directory and refuses an existing file; plannedOutputPath exists
// precisely so a dry run does neither.
func TestRunRender_DryRunHasNoFilesystemSideEffects(t *testing.T) {
	path := mergePiece(t, t.TempDir(), "activity.fit", mergeBase)
	base := t.TempDir()
	missing := filepath.Join(base, "not", "created", "yet")

	_, stderr, err := runCLI(t, runRender, true,
		"--dry-run", "--output-dir", missing, "--size", "640x360", "--video-duration", "10s", path)
	if err != nil {
		t.Fatalf("dry run into a missing directory: %v\nstderr:\n%s", err, stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("dry run created %s; it must not touch the filesystem", missing)
	}

	t.Run("an existing destination is reported, not refused", func(t *testing.T) {
		outDir := t.TempDir()
		existing := filepath.Join(outDir, "activity.mp4")
		if err := os.WriteFile(existing, []byte("not a video"), 0o644); err != nil {
			t.Fatalf("seeding an existing output: %v", err)
		}

		_, stderr, err := runCLI(t, runRender, true,
			"--dry-run", "--output-dir", outDir, "--size", "640x360", "--video-duration", "10s", path)
		// Refusing here would stop the summary the user came for, and the
		// clash is exactly the kind of thing a dry run should report.
		if err != nil {
			t.Fatalf("dry run refused an existing destination: %v", err)
		}
		if !strings.Contains(stderr, "already exists") {
			t.Errorf("dry run did not report the existing destination:\n%s", stderr)
		}
		if got, err := os.ReadFile(existing); err != nil || string(got) != "not a video" {
			t.Errorf("dry run overwrote the existing file (read %q, %v)", got, err)
		}
	})
}

// TestRunRender_DryRunFramesNamesTheFilesTheSinkWouldWrite pins that the
// listing agrees with the real thing on both the names and their order. The
// names come from encode.FrameName, the same function the sink uses, and the
// order is ascending because that is the order the sink writes them in --
// frameIndices returns the --frame-at extras last.
func TestRunRender_DryRunFramesNamesTheFilesTheSinkWouldWrite(t *testing.T) {
	path := mergePiece(t, t.TempDir(), "activity.fit", mergeBase)
	outDir := t.TempDir()

	_, stderr, err := runCLI(t, runRender, true,
		"--dry-run", "--frames", "--output-dir", outDir, "--size", "640x360",
		"--video-duration", "10s", "--frame-at", "1m", path)
	if err != nil {
		t.Fatalf("dry run with --frames: %v\nstderr:\n%s", err, stderr)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("reading output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d frame(s), want none", len(entries))
	}
	if !strings.Contains(stderr, encode.FrameName(outDir, 0)) {
		t.Errorf("frame listing does not name the file the sink would write:\n%s", stderr)
	}

	var listed []int
	for _, line := range strings.Split(stderr, "\n") {
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), filepath.Join(outDir, "frame-%06d.png"), &i); err == nil {
			listed = append(listed, i)
		}
	}
	if len(listed) < 2 {
		t.Fatalf("expected several frames listed, got %v:\n%s", listed, stderr)
	}
	if !sort.IntsAreSorted(listed) {
		t.Errorf("frames listed %v, want ascending -- the order the sink writes them", listed)
	}
}
