package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/output"
	"github.com/wisborg/output/table"

	"github.com/wisborg/fitdash/internal/inspect"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect ACTIVITY.fit [ACTIVITY.fit ...]",
	Short: "Report what metrics an activity actually contains",
	Long: `inspect reports, metric by metric, what a recorded activity contains and how
much of it: the fraction of samples carrying each metric and the range it spans.

Coverage rather than presence is the unit, because a metric is rarely all there
or all missing -- a GPS receiver loses lock under trees, a heart rate strap
drops out for a minute, a footpod connects two minutes into the run. A panel
has to handle exactly those cases, so "heart rate: present 4% of the time" is
the answer worth having.

Metrics with no coverage at all are still listed. "Does this file have power?"
is what a reader is asking, and a missing row cannot be told apart from a
metric fitdash forgot about.

Several activity files are merged and reported as one activity, exactly as the
render merges them -- so what this prints is what the dashboard will have to
draw, which is the whole point of the report.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runInspect,
}

func init() { root.AddCommand(inspectCmd) }

func runInspect(cmd *cobra.Command, args []string) error {
	// The same decode-and-merge the render performs, rather than a decode of
	// args[0]: this report is the single source of truth for whether an
	// activity carries a metric, so it has to be built from the same Track
	// the render builds its panels from. Reporting on one file of a merge
	// would answer a question nobody asked.
	track, sources, err := decodeActivities(args)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	rep := inspect.Build(track)

	// Two representations, built independently: the object carries the whole
	// report for JSON/YAML, the table carries the rows worth reading on a
	// terminal. Deriving one from the other forces the richer shape through
	// the poorer one and makes both worse -- see github.com/wisborg/output.
	// The activity-wide summary is a header on the text view and a set of
	// fields on the object. It is NOT folded into the table: a table of
	// metrics whose first rows are "sport" and "elapsed" has two different
	// kinds of thing in one column, and the CSV a reader computes with would
	// carry them too.
	out := cmd.OutOrStdout()
	if format.Format == output.Text {
		writeSummary(out, rep, sources)
	}

	// Two representations, built independently: the object carries the whole
	// report for JSON/YAML, the table carries the rows worth reading on a
	// terminal. Deriving one from the other forces the richer shape through
	// the poorer one and makes both worse -- see github.com/wisborg/output.
	doc := output.Document{Data: rep, Table: inspectTable(rep)}
	if err := doc.Write(out, format.Format); err != nil {
		return fmt.Errorf("inspect: writing report: %w", err)
	}
	return nil
}

// writeSummary prints the activity-wide header above the metric table.
//
// Elapsed and active time are both shown, always, even when they are equal.
// They differ on any activity with a stop in it, and a dashboard has to choose
// which one its timeline runs on -- so a reader should see the pair and the
// choice, not one number that silently answered the question for them. When
// the file carried no timer events there is nothing to derive active time
// from, and that is said outright rather than printing a number equal to
// elapsed and letting it pass for a measurement.
func writeSummary(w io.Writer, rep inspect.Report, sources []activitySource) {
	sport := rep.Sport
	if sport == "" {
		sport = "(not recorded)"
	}
	// Every file on its own line rather than one joined line: these are
	// paths, and a reader who has to check one against their filesystem
	// should be able to select it without trimming a separator off the end.
	// The count is stated too -- a merge of three files reporting one
	// activity is the surprising part, not the paths themselves.
	//
	// The offsets come from sources rather than from rep, because they are a
	// property of the MERGE and not of the activity the report describes:
	// inspect.Build is handed one Track and cannot know where its files
	// began. rep.Paths still carries the list for the JSON/YAML views, which
	// is why both exist.
	if len(sources) > 1 {
		fmt.Fprintf(w, "%d files merged into one activity; each offset is into the\n", len(sources))
		fmt.Fprintf(w, "activity's elapsed time, ready for --label at= or --highlight from=:\n")
		for _, line := range mergedSourceLines(sources) {
			fmt.Fprintf(w, "%s\n", line)
		}
	} else {
		fmt.Fprintf(w, "%s\n", rep.Path)
	}
	fmt.Fprintf(w, "sport    %s\n", sport)
	fmt.Fprintf(w, "samples  %d\n", rep.Samples)
	if rep.Samples > 0 {
		fmt.Fprintf(w, "start    %s\n", rep.Start.Format(time.RFC3339))
		fmt.Fprintf(w, "elapsed  %s\n", fmtDuration(rep.Elapsed))
		if rep.HasTimerEvents {
			fmt.Fprintf(w, "active   %s\n", fmtDuration(rep.Active))
		} else {
			fmt.Fprintf(w, "active   (file has no timer events)\n")
		}
	}
	fmt.Fprintln(w)
}

// inspectTable renders rep as the terminal view: one row per metric, coverage
// as a percentage, and the range.
//
// A metric with no coverage gets an empty range rather than "0 - 0". Its Min
// and Max are meaningless when nothing was present (see inspect.Metric), and
// printing zeros there would read as a real measurement of zero -- which is
// the same absent-is-not-zero mistake this project spends its care avoiding,
// just committed in a table instead of in pixels.
func inspectTable(rep inspect.Report) *table.Table {
	t := table.New(
		table.Column{Header: "metric"},
		table.Column{Header: "source"},
		table.Column{Header: "unit"},
		table.Column{Header: "coverage", Align: table.Right, Format: "%.1f%%"},
		table.Column{Header: "samples", Align: table.Right},
		table.Column{Header: "min", Align: table.Right},
		table.Column{Header: "max", Align: table.Right},
	)
	// The source column is not decoration. A Stryd footpod registers its
	// running power under the developer-field name "Power", and FIT has a
	// standard Record.power field, so a file carrying both produces two rows
	// legitimately called "Power" with different values -- they are different
	// sensors' readings. Without the column the table shows the same metric
	// twice, disagreeing with itself, and a reader has no way to tell which
	// row is which.
	add := func(m inspect.Metric, source string) {
		lo, hi := fmt.Sprintf("%.3g", m.Min), fmt.Sprintf("%.3g", m.Max)
		if m.Present == 0 {
			lo, hi = "", ""
		}
		t.MustAppend(m.Name, source, m.Unit, m.Coverage(rep.Samples)*100, m.Present, lo, hi)
	}
	for _, m := range rep.Metrics {
		add(m, "fit")
	}
	for _, m := range rep.DevFields {
		add(m, "dev")
	}
	return t
}

// fmtDuration is here rather than inline so the header and any future summary
// row spell a duration the same way.
//
// TRUNCATED, not rounded, to match the clock burned into a rendered dashboard
// -- which truncates because it is a stopwatch. The two describe the same
// number, and rounding here made them disagree by a second on a real file:
// inspect said 25m54s while the video's final frame read 0:25:53. A user
// comparing them has no way to know that is a formatting choice rather than a
// discrepancy in the data.
func fmtDuration(d time.Duration) string { return d.Truncate(time.Second).String() }
