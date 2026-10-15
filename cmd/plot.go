package cmd

import (
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/plot"
)

var plotOpts struct {
	y, x          string
	out           string
	width, height int
	smooth        time.Duration
	theme         string
	fonts         []string
	units         string
	unitEach      []string
}

// The size of a PNG where --width and --height do not say: wide, as a
// chart over time reads best, and at a size a screen shows whole.
const (
	plotImageWidth  = 1600
	plotImageHeight = 600
)

var plotCmd = &cobra.Command{
	Use:   "plot ACTIVITY [ACTIVITY ...] --y METRIC",
	Short: "Chart one of an activity's metrics against time or distance, in the terminal or as a PNG",
	Long: `plot draws one metric of an activity -- power, heart rate, pace, a
footpod's developer field -- against elapsed time or distance. In the
terminal it is drawn in braille dots, two by four to a character, so a
chart the width of a terminal window is a couple of hundred dots across;
with --out it is a PNG, in the dashboard's type and themes, with a faint
band behind the line from each moment's lowest reading to its highest.

Without --y it lists the metrics the activity can be charted by, with how
much of it carries each: the names are inspect's, so plot never disagrees
with inspect about what a file holds.

A missing reading is never drawn as zero. The line breaks where the
readings stop for longer than the recording's own rhythm and the chart's
resolution can hide, and resumes where they come back. The y axis is set
from where most of the readings lie -- the highest and lowest hundredth are
left out -- so a power meter's dropouts to zero and its sprints do not
flatten the rest of the ride; a reading beyond the axis is drawn at its edge.

Several activity files are merged into one activity, as the render and
inspect merge them.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPlot,
}

func init() {
	f := plotCmd.Flags()
	f.StringVar(&plotOpts.y, "y", "", "the metric to chart, as inspect names it with hyphens -- heart-rate, power, cadence, "+
		"speed, pace, elevation, distance, temperature, vertical-oscillation, stance-time, step-length -- or a developer "+
		"field's own name; without it, the metrics this activity has are listed")
	f.StringVar(&plotOpts.x, "x", "time", "what the chart runs along -- \"time\" (elapsed, pauses included) or \"distance\"")
	f.StringVar(&plotOpts.out, "out", "", "write the chart to this PNG rather than drawing it in the terminal")
	f.IntVar(&plotOpts.width, "width", 0, "the chart's width: in characters in the terminal, labels included (default: the "+
		"terminal's, or 100); in pixels in a PNG (default: "+strconv.Itoa(plotImageWidth)+")")
	f.IntVar(&plotOpts.height, "height", 0, "the chart's height: in lines in the terminal, title and labels included (default: "+
		"24, or less in a short terminal); in pixels in a PNG (default: "+strconv.Itoa(plotImageHeight)+")")
	f.StringVar(&plotOpts.theme, "theme", panel.LightTheme().Name, "a PNG's palette, as the render's --theme names them -- "+
		"light (the default: a chart is read on a page more often than over video), dark or night")
	f.StringArrayVar(&plotOpts.fonts, "font", nil, "a TrueType or OpenType font for a PNG's letters the built-in font lacks, "+
		"as the render's --font; repeat for several")
	f.DurationVar(&plotOpts.smooth, "smooth", 0, "average each reading with those within half of this either side of it in time "+
		"-- 30s takes the noise out of a power trace -- never across a break in the line")
	f.StringVar(&plotOpts.units, "units", string(units.Metric), "the units readings and distances are drawn in -- \"metric\" "+
		"or \"imperial\"; a flight's default is nautical miles, feet and knots")
	f.StringArrayVar(&plotOpts.unitEach, "unit", nil, "one quantity's unit, over --units (repeatable) -- distance=km|mi|nm, "+
		"elevation=m|ft, speed=km/h|mph|kn|m/s, pace=min/km|min/mi")
	root.AddCommand(plotCmd)
}

func runPlot(cmd *cobra.Command, args []string) error {
	if plotOpts.x != "time" && plotOpts.x != "distance" {
		return fmt.Errorf("plot: --x %q: use time or distance", plotOpts.x)
	}
	track, _, err := decodeActivities(args)
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	u, err := parseUnits(plotOpts.units, plotOpts.unitEach)
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	// A flight is read in aviation's units unless --units said otherwise,
	// as the render reads it.
	if (track.Sport == "flying" || len(track.Flights) > 0) && !cmd.Flags().Changed("units") {
		if u, err = aviationUnits(plotOpts.unitEach); err != nil {
			return fmt.Errorf("plot: %w", err)
		}
	}
	rep := inspect.Build(track)
	metrics := plotMetrics(track.Sport, u, rep.DevFields)
	out := cmd.OutOrStdout()
	if plotOpts.y == "" {
		writePlotMetrics(out, rep, metrics)
		return nil
	}
	m, err := findPlotMetric(metrics, plotOpts.y)
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	cov, _ := rep.Metric(m.inspect)
	if cov.Present == 0 {
		return fmt.Errorf("plot: this activity has no %s: inspect lists what it has", m.name)
	}

	if plotOpts.out != "" {
		err = writePlotImage(track, m, u, cmd.Flags().Changed("width"), cmd.Flags().Changed("height"))
	} else {
		err = writePlotBraille(out, track, m, u, cmd.Flags().Changed("width"), cmd.Flags().Changed("height"))
	}
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	if plotOpts.out != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "chart      %s\n", plotOpts.out)
	}
	if c := cov.Coverage(rep.Samples); c < 0.995 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s present %.0f%% of the time; the line breaks where it is missing\n", m.name, 100*c)
	}
	return nil
}

// writePlotBraille draws m's chart in braille to out, the size of the
// terminal or of --width and --height.
func writePlotBraille(out io.Writer, track *fitactivity.Track, m plotMetric, u units.Set, widthSet, heightSet bool) error {
	width, height := chartSize(widthSet, heightSet)
	chart, err := buildChart(track, m, u, plotOpts.x, 2*width, max(width/12, 2), 5, plotOpts.smooth)
	if err != nil {
		return err
	}
	lines := chart.Braille(width, height)
	if lines == nil {
		return fmt.Errorf("%d by %d characters is too small for a chart", width, height)
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	return nil
}

// writePlotImage draws m's chart as a PNG at --out, --width by --height
// pixels or the default size.
func writePlotImage(track *fitactivity.Track, m plotMetric, u units.Set, widthSet, heightSet bool) error {
	if !strings.EqualFold(filepath.Ext(plotOpts.out), ".png") {
		return fmt.Errorf("--out %q: a chart is written as a PNG; name it .png", plotOpts.out)
	}
	width, height := plotImageWidth, plotImageHeight
	if widthSet {
		width = plotOpts.width
	}
	if heightSet {
		height = plotOpts.height
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("a chart %d by %d pixels has nothing to draw on", width, height)
	}
	theme, err := panel.SelectTheme(plotOpts.theme)
	if err != nil {
		return err
	}
	fallbacks, err := loadFonts(plotOpts.fonts)
	if err != nil {
		return err
	}
	fonts, err := panel.NewFaceCacheWith(fallbacks...)
	if err != nil {
		return err
	}
	chart, err := buildChart(track, m, u, plotOpts.x, width, max(width/140, 2), max(height/90, 3), plotOpts.smooth)
	if err != nil {
		return err
	}
	img, err := chart.Image(width, height, theme, fonts)
	if err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("%d by %d pixels is too small for a chart", width, height)
	}
	f, err := os.Create(plotOpts.out)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", plotOpts.out, err)
	}
	return f.Close()
}

// chartSize is the chart's size in characters: --width and --height where
// given; otherwise the terminal's width and a height that leaves the prompt
// and a line of notes on screen, with COLUMNS and LINES, then 100 by 24,
// standing in where there is no terminal to ask.
func chartSize(widthSet, heightSet bool) (width, height int) {
	cols, rows, ok := terminalSize(os.Stdout)
	if !ok {
		cols, rows = envInt("COLUMNS", 100), envInt("LINES", 0)
	}
	width, height = cols, 24
	if rows > 0 {
		height = min(height, rows-3)
	}
	if widthSet {
		width = plotOpts.width
	}
	if heightSet {
		height = plotOpts.height
	}
	return width, height
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// buildChart is m's readings over track charted against x, "time" or
// "distance", for a drawing of resolution columns -- dots across, or
// pixels -- with at most xTicks and yTicks steps between ticks, smoothed
// over smooth.
func buildChart(track *fitactivity.Track, m plotMetric, u units.Set, x string, resolution, xTicks, yTicks int, smooth time.Duration) (plot.Chart, error) {
	if len(track.Samples) == 0 {
		return plot.Chart{}, errors.New("the activity has no samples")
	}
	start, end := fitactivity.BuildTimerModel(track).Window()
	points := make([]plot.Point, 0, len(track.Samples))
	for _, s := range track.Samples {
		p := plot.Point{T: s.Time.Sub(start).Seconds()}
		p.Y, p.Ok = m.value(s)
		if x == "distance" {
			// A sample with no distance has no place on the axis, whatever
			// its reading.
			p.X, p.Ok = u.Distance.FromSI(s.Distance), p.Ok && s.HasDistance
		} else {
			p.X = p.T
		}
		points = append(points, p)
	}

	// The line breaks where the readings stop for longer than the
	// recording's own rhythm allows -- or than two columns of the drawing,
	// whichever is longer: a gap narrower than that cannot be seen as one,
	// and a break there would only shred the line.
	span := end.Sub(start)
	maxGap := max(track.GapTolerance(), 2*span/time.Duration(max(resolution, 1)))
	segs := plot.Smooth(plot.Segments(points, maxGap), smooth)
	lo, hi, ok := plot.RobustRange(segs)
	if !ok {
		return plot.Chart{}, fmt.Errorf("no %s to chart", m.name)
	}

	var xs plot.Scale
	against := "time"
	if x == "distance" {
		first, last, ok := distanceSpan(track)
		if !ok {
			return plot.Chart{}, errors.New("the activity records no distance to chart against; use --x time")
		}
		xs = plot.ExactScale(u.Distance.FromSI(first), u.Distance.FromSI(last), xTicks, plot.Number)
		against = "distance (" + u.Distance.Name + ")"
	} else {
		xs = plot.ExactScale(0, span.Seconds(), xTicks, plot.Clock)
	}
	ys := plot.NewScale(lo, hi, yTicks, m.kind)
	ys.Inverted = m.inverted

	title := m.name
	if m.unit != "" {
		title += " (" + m.unit + ")"
	}
	title += " against " + against
	if smooth > 0 {
		title += fmt.Sprintf(", smoothed over %s", smooth)
	}
	return plot.Chart{Title: title, X: xs, Y: ys, Segments: segs}, nil
}

// distanceSpan is the first and last distance the activity records, in
// metres.
func distanceSpan(track *fitactivity.Track) (first, last float64, ok bool) {
	for _, s := range track.Samples {
		if !s.HasDistance {
			continue
		}
		if !ok {
			first, ok = s.Distance, true
		}
		last = s.Distance
	}
	return first, last, ok && last > first
}

// writePlotMetrics lists the metrics the activity can be charted by, with
// how much of it carries each; the ones it does not carry are left out,
// since there is nothing of theirs to chart, and inspect lists them.
func writePlotMetrics(w io.Writer, rep inspect.Report, ms []plotMetric) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "--y\tunit\tcoverage")
	for _, m := range ms {
		cov, ok := rep.Metric(m.inspect)
		if !ok || cov.Present == 0 {
			continue
		}
		name := m.name
		if strings.ContainsRune(name, ' ') {
			name = strconv.Quote(name)
		}
		fmt.Fprintf(tw, "%s\t%s\t%.1f%%\n", name, m.unit, 100*cov.Coverage(rep.Samples))
	}
	tw.Flush()
}
