package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
)

// activitySource is one file that went into the activity being rendered, and
// where it begins in that activity's own ELAPSED timeline.
//
// Offset is the number a person needs and cannot otherwise get. A workout
// split across five files is one timeline once merged, and "the race" is no
// longer a file, it is a stretch some way into that timeline -- so marking it
// up with --highlight or --label means knowing where each leg starts. Without
// this, the only way to find out is to render the whole thing and watch it.
type activitySource struct {
	Path   string
	Offset time.Duration
}

// decodeActivities decodes every path and merges them into one Track, also
// reporting where each file begins in the merged activity.
//
// This decodes and merges in two steps rather than calling
// fitactivity.DecodeAll, which does exactly this in one. The reason is the
// offsets: DecodeAll returns only the merged Track, and a merged Track
// deliberately does not record where its sources begin -- it is one activity,
// and no panel or model may care which file an instant came from. That is the
// right shape for the library and the wrong shape for a summary written for
// somebody about to author a --label, so the pieces are kept here, in the
// layer that wants them, rather than pushed upstream into the type that is
// meant not to have them.
//
// The ORDER comes from merged.Sources, never from re-sorting the tracks here.
// Merge owns the ordering rule (by each file's own start time), and a second
// sort written at this call site would be free to disagree with the order the
// samples are actually in.
func decodeActivities(paths []string) (*fitactivity.Track, []activitySource, error) {
	tracks := make([]*fitactivity.Track, len(paths))
	for i, p := range paths {
		t, err := fitactivity.Decode(p)
		if err != nil {
			return nil, nil, err
		}
		tracks[i] = t
	}
	merged, err := fitactivity.Merge(tracks...)
	if err != nil {
		return nil, nil, err
	}

	// Built after Merge has succeeded, which is what makes keying by path
	// safe: two arguments naming one file would collide here, and Merge has
	// already refused that pair for overlapping.
	starts := make(map[string]time.Time, len(tracks))
	for _, t := range tracks {
		start, _ := fitactivity.BuildTimerModel(t).Window()
		starts[t.SourcePath] = start
	}

	// Elapsed, not a plain subtraction: it is the merged activity's own
	// clock, the same one --label at= and --highlight from= are measured
	// against (see Timeline.IndexAt), so an offset printed here is a value
	// that can be typed straight back in.
	timer := fitactivity.BuildTimerModel(merged)
	sources := make([]activitySource, len(merged.Sources))
	for i, path := range merged.Sources {
		sources[i] = activitySource{Path: path, Offset: timer.Elapsed(starts[path])}
	}
	return merged, sources, nil
}

// mergedSourceLines renders sources as one aligned "OFFSET  PATH" line each,
// for the summaries in this package that list what an activity was made of.
//
// The offset is a Go duration ("19m43s"), not the h:mm:ss clock every other
// summary in this program prints. That is deliberate and is the whole point
// of showing it: --label at= and --highlight from= parse Go durations, so a
// clock reading would have to be converted by hand before it could be used,
// which is exactly the work this is meant to remove. Rounded to the second
// because a label a second early is the same label, and "19m43.317s" is not a
// value anybody wants to retype.
func mergedSourceLines(sources []activitySource) []string {
	width := 0
	offsets := make([]string, len(sources))
	for i, s := range sources {
		offsets[i] = s.Offset.Round(time.Second).String()
		if len(offsets[i]) > width {
			width = len(offsets[i])
		}
	}
	lines := make([]string, len(sources))
	for i, s := range sources {
		lines[i] = fmt.Sprintf("  %-*s  %s", width, offsets[i], s.Path)
	}
	return lines
}

// writeMergeSummary names the files a merged activity came from, where each
// one starts, and how much time separates them -- before any figure derived
// from the merge is printed.
//
// A single file prints nothing: the path is already on the command line and
// the video is named after it, so restating it is noise. Several files are a
// different matter -- every number below this line (total distance, elapsed
// time, the whole activity's span) describes an activity that exists nowhere
// on disk, and the reader has to be able to see what was combined to produce
// it. This project renders personal data and declines to make that kind of
// decision quietly.
//
// The gaps are reported because they are the part a reader will not predict.
// fitdash never fills them in: the ground covered between two recordings was
// never measured, so the merged distance omits it, and the gap becomes a pause
// that the video freezes through (or cuts, under --pauses skip). A 40-minute
// hole between two files is worth seeing before watching the render, not
// after.
func writeMergeSummary(cmd *cobra.Command, track *fitactivity.Track, sources []activitySource) {
	if renderOpts.quiet || track == nil || len(sources) < 2 {
		return
	}
	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "merged %d files into one activity, ordered by their own start times; each\n", len(sources))
	fmt.Fprintf(out, "offset is into the activity's elapsed time, ready for --label at= or --highlight from=:\n")
	for _, line := range mergedSourceLines(sources) {
		fmt.Fprintf(out, "%s\n", line)
	}
	// Read off the merged track's pauses rather than re-deriving the seams
	// from the files: a gap between two recordings resolves as a pause like
	// any other, and this must report the same intervals the timeline
	// freezes through. Recomputing them here would be free to disagree with
	// what the render actually did.
	if pauses := fitactivity.BuildTimerModel(track).Pauses(); len(pauses) > 0 {
		var total time.Duration
		for _, p := range pauses {
			total += p.End.Sub(p.Start)
		}
		fmt.Fprintf(out, "  %s not recorded across %d pause%s (gaps between files, and any the watch was stopped for) -- frozen through, and no distance added\n",
			panel.FormatClock(total), len(pauses), plural(len(pauses)))
	}
}
