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
	Use:   "inspect ACTIVITY [ACTIVITY ...]",
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
	doc := output.Document{Data: jsonReport(rep), Table: inspectTable(rep, summedDistance(sources))}
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
func inspectTable(rep inspect.Report, summed bool) *table.Table {
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
	//
	// "std" rather than "fit": a standard field is the format's own, and the
	// activity may be a GPX or a TCX file whose heart rate is labelled "fit"
	// by nothing but this column.
	add := func(m inspect.Metric, source string) {
		lo, hi := fmt.Sprintf("%.3g", m.Min), fmt.Sprintf("%.3g", m.Max)
		if m.Present == 0 {
			lo, hi = "", ""
		}
		t.MustAppend(m.Name, source, m.Unit, m.Coverage(rep.Samples)*100, m.Present, lo, hi)
	}
	// A distance summed from the fixes is no field of the file's, and says
	// so: "gps", where the format's own would be "std".
	for _, m := range rep.Metrics {
		if summed && m.Name == inspect.MetricDistance {
			add(m, "gps")
			continue
		}
		add(m, "std")
	}
	for _, m := range rep.DevFields {
		add(m, "dev")
	}
	return t
}

// summedDistance is whether any of sources' distance was summed from GPS.
func summedDistance(sources []activitySource) bool {
	for _, s := range sources {
		if s.SummedDistance {
			return true
		}
	}
	return false
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

// inspectJSON is the report as JSON and YAML write it: snake_case names in
// both, as course writes its reports, and times in seconds.
//
// Its own type rather than inspect.Report with tags on it. The report is
// what the panels read, and its Go names were the JSON's -- "HasTimerEvents",
// the elapsed time in nanoseconds -- while YAML lowered them to
// "hastimerevents": the same data named two ways, neither of them the
// other tools'.
type inspectJSON struct {
	Path           string           `json:"path" yaml:"path"`
	Paths          []string         `json:"paths" yaml:"paths"`
	Sport          string           `json:"sport,omitempty" yaml:"sport,omitempty"`
	Samples        int              `json:"samples" yaml:"samples"`
	Start          *time.Time       `json:"start,omitempty" yaml:"start,omitempty"`
	End            *time.Time       `json:"end,omitempty" yaml:"end,omitempty"`
	ElapsedS       float64          `json:"elapsed_s" yaml:"elapsed_s"`
	ActiveS        float64          `json:"active_s" yaml:"active_s"`
	HasTimerEvents bool             `json:"has_timer_events" yaml:"has_timer_events"`
	Metrics        []inspectMetricJ `json:"metrics" yaml:"metrics"`
	DevFields      []inspectMetricJ `json:"dev_fields" yaml:"dev_fields"`
}

// inspectMetricJ is one metric's coverage: how many samples carry it, as a
// count and as a share of the activity's, and the range of its values where
// any carry it -- none written where none do, since a range of no values is
// not "0 to 0".
type inspectMetricJ struct {
	Name     string   `json:"name" yaml:"name"`
	Unit     string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Present  int      `json:"present" yaml:"present"`
	Coverage float64  `json:"coverage" yaml:"coverage"`
	Min      *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max      *float64 `json:"max,omitempty" yaml:"max,omitempty"`
}

func jsonReport(rep inspect.Report) inspectJSON {
	metrics := func(ms []inspect.Metric) []inspectMetricJ {
		out := []inspectMetricJ{}
		for _, m := range ms {
			j := inspectMetricJ{Name: m.Name, Unit: m.Unit, Present: m.Present, Coverage: m.Coverage(rep.Samples)}
			if m.Present > 0 {
				lo, hi := m.Min, m.Max
				j.Min, j.Max = &lo, &hi
			}
			out = append(out, j)
		}
		return out
	}
	j := inspectJSON{
		Path: rep.Path, Paths: rep.Paths, Sport: rep.Sport, Samples: rep.Samples,
		ElapsedS: rep.Elapsed.Seconds(), ActiveS: rep.Active.Seconds(), HasTimerEvents: rep.HasTimerEvents,
		Metrics: metrics(rep.Metrics), DevFields: metrics(rep.DevFields),
	}
	if !rep.Start.IsZero() {
		start, end := rep.Start, rep.End
		j.Start, j.End = &start, &end
	}
	if j.Paths == nil {
		j.Paths = []string{}
	}
	return j
}
