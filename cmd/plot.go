package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"image"
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
	y             []string
	x             string
	out           string
	width, height int
	smooth        time.Duration
	theme         string
	fonts         []string
	terminal      string
	power         string
	units         string
	unitEach      []string
}

// The size of a PNG where --width and --height do not say: wide, as a
// chart over time reads best, and at a size a screen shows whole -- for
// one metric; see plotImageHeightFor for several.
const (
	plotImageWidth  = 1600
	plotImageHeight = 600
)

// plotImageHeightFor is a PNG's default height for a chart of strips
// strips: plotImageHeight for one, and 300 pixels more for each further
// one, so each strip has room to read without the picture outgrowing a
// screen before the fourth.
func plotImageHeightFor(strips int) int { return plotImageHeight + 300*max(strips-1, 0) }

var plotCmd = &cobra.Command{
	Use:   "plot ACTIVITY [ACTIVITY ...] --y METRIC[,METRIC...]",
	Short: "Chart one of an activity's metrics against time or distance, in the terminal or as a PNG",
	Long: `plot draws one metric of an activity -- power, heart rate, pace, a
footpod's developer field -- against elapsed time or distance. With --out
it is a PNG, in the dashboard's type and themes, with a faint band behind
the line from each moment's lowest reading to its highest.

Without --out the chart is drawn in the terminal. iTerm2, WezTerm, kitty
and Ghostty are shown that picture, the width of the window. Any other
terminal gets the chart in braille dots, two by four to a character, so a
chart the width of a window is a couple of hundred dots across. Output
into a pipe or a file, or through tmux or screen, is braille too: a
picture would arrive there as a page of base64. --terminal chooses
outright.

Without --y it lists the metrics the activity can be charted by, with how
much of it carries each: the names are inspect's, so plot never disagrees
with inspect about what a file holds. Several metrics -- --y power,heart-rate
-- are charted one above the other, each on its own scale, sharing the x
axis, so a moment in one is directly above the same moment in the next.

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
	f.StringSliceVar(&plotOpts.y, "y", nil, "the metric to chart, as inspect names it with hyphens -- heart-rate, power, "+
		"cadence, speed, pace, elevation, distance, temperature, vertical-oscillation, stance-time, step-length -- or a "+
		"developer field's own name; several, comma-separated or repeated, are charted one above the other; without it, "+
		"the metrics this activity has are listed")
	f.StringVar(&plotOpts.x, "x", "time", "what the chart runs along -- \"time\" (elapsed, pauses included) or \"distance\"")
	f.StringVar(&plotOpts.out, "out", "", "write the chart to this PNG rather than drawing it in the terminal")
	f.IntVar(&plotOpts.width, "width", 0, "the chart's width: in characters in the terminal, labels included (default: the "+
		"terminal's, or 100); in pixels in a PNG (default: "+strconv.Itoa(plotImageWidth)+")")
	f.IntVar(&plotOpts.height, "height", 0, "the chart's height: in lines in the terminal, title and labels included (default: "+
		"24, and 10 more for each further metric, or less in a short terminal); in pixels in a PNG (default: "+strconv.Itoa(plotImageHeight)+", and 300 more for each further metric)")
	f.StringVar(&plotOpts.theme, "theme", panel.LightTheme().Name, "the picture's palette, in a PNG or the terminal, as the "+
		"render's --theme names them -- light (a chart is read on a page more often than over video), dark or night")
	f.StringVar(&plotOpts.terminal, "terminal", termAuto, "how the chart is drawn in the terminal -- \""+termAuto+
		"\" (a picture in a terminal known to show one, braille otherwise), \""+termITerm+"\" (a picture "+
		"in iTerm2's protocol, which WezTerm reads too), \""+termKitty+"\" (kitty's, which Ghostty reads too) or \""+
		termBraille+"\"")
	f.StringArrayVar(&plotOpts.fonts, "font", nil, "a TrueType or OpenType font for a PNG's letters the built-in font lacks, "+
		"as the render's --font; repeat for several")
	f.DurationVar(&plotOpts.smooth, "smooth", 0, "average each reading with those within half of this either side of it in time "+
		"-- 30s takes the noise out of a power trace -- never across a break in the line")
	f.StringVar(&plotOpts.power, "power-source", "auto", "which power --y power charts, as the render's --power-source: "+
		"\"auto\" (default: a Stryd footpod's where the activity has it, the watch's otherwise), \"stryd\" or \"native\" "+
		"(the watch's own); the two are different sensors and can disagree by half")
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
	power, err := parsePowerSource(plotOpts.power)
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	rep := inspect.Build(track)
	metrics := plotMetrics(track.Sport, u, rep.DevFields, power, powerFrom(track, power))
	out := cmd.OutOrStdout()
	if len(plotOpts.y) == 0 {
		writePlotMetrics(out, track, metrics)
		return nil
	}
	var ms []plotMetric
	for _, name := range plotOpts.y {
		m, err := findPlotMetric(metrics, strings.TrimSpace(name))
		if err != nil {
			return fmt.Errorf("plot: %w", err)
		}
		for _, had := range ms {
			if had.name == m.name {
				return fmt.Errorf("plot: --y names %s twice", m.name)
			}
		}
		if m.coverage(track) == 0 {
			if m.name == "power" && power != fitactivity.PowerAuto {
				return fmt.Errorf("plot: this activity has no %s power: --power-source auto charts whichever it has", plotOpts.power)
			}
			return fmt.Errorf("plot: this activity has no %s: inspect lists what it has", m.name)
		}
		ms = append(ms, m)
	}

	widthSet, heightSet := cmd.Flags().Changed("width"), cmd.Flags().Changed("height")
	if plotOpts.out != "" {
		err = writePlotImage(track, ms, u, widthSet, heightSet)
	} else {
		err = writePlotTerminal(out, cmd.ErrOrStderr(), track, ms, u, widthSet, heightSet)
	}
	if err != nil {
		return fmt.Errorf("plot: %w", err)
	}
	if plotOpts.out != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "chart      %s\n", plotOpts.out)
	}
	for _, m := range ms {
		if c := m.coverage(track); c < 0.995 {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s present %.0f%% of the time; the line breaks where it is missing\n", m.name, 100*c)
		}
	}
	return nil
}

// writePlotTerminal draws m's chart in the terminal, as a picture or in
// braille as chooseTerminal decides, the size of the terminal or of
// --width and --height in characters, with chooseTerminal's note on
// errOut.
func writePlotTerminal(out, errOut io.Writer, track *fitactivity.Track, ms []plotMetric, u units.Set, widthSet, heightSet bool) error {
	ts, tty := terminalSize(os.Stdout)
	if f, ok := out.(*os.File); !ok || f != os.Stdout {
		// Written somewhere other than the process's own output -- a test's
		// buffer -- which is no terminal, whatever stdout is.
		tty = false
	}
	how, note, err := chooseTerminal(plotOpts.terminal, os.Getenv, tty)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(errOut, note)
	}
	width, height := chartSize(ts, tty, len(ms), widthSet, heightSet)
	if how == termBraille {
		return writePlotBraille(out, track, ms, u, width, height)
	}
	pw, ph := terminalPixels(ts, width, height, len(ms))
	img, err := plotPicture(track, ms, u, pw, ph)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	if how == termKitty {
		return writeKittyImage(out, buf.Bytes(), width)
	}
	return writeITermImage(out, buf.Bytes(), width)
}

// terminalPixels is the picture drawn for a chart of strips strips, cols
// by rows characters in a terminal of size ts: as many pixels as those
// characters cover -- twice as many, so that on a screen of two pixels a
// point the terminal still draws it sharp -- in a PNG's proportions for
// that many strips, and no taller than the rows, less one for the prompt. A terminal that does not say its size in
// pixels is taken to have characters ten pixels wide and twenty tall. The
// picture is held to plotTerminalMaxWidth, and its height with it.
func terminalPixels(ts termSize, cols, rows, strips int) (width, height int) {
	cellW, cellH := 10.0, 20.0
	if ts.Width > 0 && ts.Height > 0 && ts.Cols > 0 && ts.Rows > 0 {
		cellW, cellH = float64(ts.Width)/float64(ts.Cols), float64(ts.Height)/float64(ts.Rows)
	}
	scale := min(1, plotTerminalMaxWidth/(2*cellW*float64(cols)))
	width = int(2 * cellW * float64(cols) * scale)
	height = min(width*plotImageHeightFor(strips)/plotImageWidth, int(2*cellH*float64(max(rows-1, 1))*scale))
	return width, height
}

// plotTerminalMaxWidth is the widest picture drawn for the terminal, in
// pixels. A terminal that reports its pixels as the screen's own, on a
// screen of two to a point, would otherwise have a full-width chart drawn
// some six thousand pixels across: seconds to draw and megabytes to send,
// for a picture the terminal shows no sharper than this.
const plotTerminalMaxWidth = 3200.0

// writePlotBraille draws the chart of ms in braille to out, width by
// height characters.
func writePlotBraille(out io.Writer, track *fitactivity.Track, ms []plotMetric, u units.Set, width, height int) error {
	// Each strip's rows, as Braille shares them out, ticked about every
	// three -- a tick that would share a row with another is left
	// unlabelled, so more steps cost nothing, and fewer round the scale out
	// to steps so coarse a third of a short strip is empty -- and the x
	// axis about every twelve characters.
	rows := (height - 3 - len(ms)) / len(ms)
	if len(ms) == 1 {
		rows = height - 3
	}
	chart, err := buildChart(track, ms, u, plotOpts.x, 2*width, max(width/12, 2), max(rows/3, 3), plotOpts.smooth)
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

// writePlotImage draws the chart of ms as a PNG at --out, --width by
// --height pixels or the default size.
func writePlotImage(track *fitactivity.Track, ms []plotMetric, u units.Set, widthSet, heightSet bool) error {
	if !strings.EqualFold(filepath.Ext(plotOpts.out), ".png") {
		return fmt.Errorf("--out %q: a chart is written as a PNG; name it .png", plotOpts.out)
	}
	width, height := plotImageWidth, plotImageHeightFor(len(ms))
	if widthSet {
		width = plotOpts.width
	}
	if heightSet {
		height = plotOpts.height
	}
	img, err := plotPicture(track, ms, u, width, height)
	if err != nil {
		return err
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

// plotPicture is the chart of ms drawn width by height pixels, in --theme
// and with --font.
func plotPicture(track *fitactivity.Track, ms []plotMetric, u units.Set, width, height int) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("a chart %d by %d pixels has nothing to draw on", width, height)
	}
	theme, err := panel.SelectTheme(plotOpts.theme)
	if err != nil {
		return nil, err
	}
	fallbacks, err := loadFonts(plotOpts.fonts)
	if err != nil {
		return nil, err
	}
	fonts, err := panel.NewFaceCacheWith(fallbacks...)
	if err != nil {
		return nil, err
	}
	// Ticks are spaced by the picture's size in proportion to its text: a
	// picture drawn at twice the pixels for a sharp terminal gets the same
	// ticks, not twice as many. A strip's ticks are spaced by its share of
	// the height.
	text := plot.TextPx(width, height, len(ms))
	strip := float64(height) / float64(len(ms))
	chart, err := buildChart(track, ms, u, plotOpts.x, width, max(int(float64(width)/(text*7)), 2), max(int(strip/(text*4.5)), 2), plotOpts.smooth)
	if err != nil {
		return nil, err
	}
	img, err := chart.Image(width, height, theme, fonts)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, fmt.Errorf("%d by %d pixels is too small for a chart", width, height)
	}
	return img, nil
}

// chartSize is the size in characters of a chart of strips strips:
// --width and --height where given; otherwise the terminal's width, and a
// height of 24 lines for one strip and ten more for each further one, held
// to what leaves the prompt and a line of notes on screen. COLUMNS and
// LINES, then 100 columns, stand in where there is no terminal to ask.
func chartSize(ts termSize, tty bool, strips int, widthSet, heightSet bool) (width, height int) {
	cols, rows := ts.Cols, ts.Rows
	if !tty {
		cols, rows = envInt("COLUMNS", 100), envInt("LINES", 0)
	}
	width, height = cols, 24+10*(strips-1)
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

// buildChart is the chart of ms over track against x, "time" or
// "distance", for a drawing of resolution columns -- dots across, or
// pixels -- with at most xTicks steps between the x axis's ticks and
// yTicks between each strip's, smoothed over smooth.
func buildChart(track *fitactivity.Track, ms []plotMetric, u units.Set, x string, resolution, xTicks, yTicks int, smooth time.Duration) (plot.Chart, error) {
	if len(track.Samples) == 0 {
		return plot.Chart{}, errors.New("the activity has no samples")
	}
	start, end := fitactivity.BuildTimerModel(track).Window()
	span := end.Sub(start)

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

	// The line breaks where the readings stop for longer than the
	// recording's own rhythm allows -- or than two columns of the drawing,
	// whichever is longer: a gap narrower than that cannot be seen as one,
	// and a break there would only shred the line.
	maxGap := max(track.GapTolerance(), 2*span/time.Duration(max(resolution, 1)))
	chart := plot.Chart{X: xs}
	var names []string
	for _, m := range ms {
		points := make([]plot.Point, 0, len(track.Samples))
		for _, s := range track.Samples {
			p := plot.Point{T: s.Time.Sub(start).Seconds()}
			p.Y, p.Ok = m.value(s)
			if x == "distance" {
				// A sample with no distance has no place on the axis,
				// whatever its reading.
				p.X, p.Ok = u.Distance.FromSI(s.Distance), p.Ok && s.HasDistance
			} else {
				p.X = p.T
			}
			points = append(points, p)
		}
		segs := plot.Smooth(plot.Segments(points, maxGap), smooth)
		lo, hi, ok := plot.RobustRange(segs)
		if !ok {
			return plot.Chart{}, fmt.Errorf("no %s to chart", m.name)
		}
		ys := plot.NewScale(lo, hi, yTicks, m.kind)
		ys.Inverted = m.inverted
		title := m.name
		switch {
		case m.unit != "" && m.from != "":
			title += " (" + m.unit + ", " + m.from + ")"
		case m.unit != "":
			title += " (" + m.unit + ")"
		}
		chart.Strips = append(chart.Strips, plot.Strip{Title: title, Y: ys, Segments: segs})
		names = append(names, m.name)
	}

	// One metric is named with its unit; several by name, each strip
	// heading its own with its unit.
	chart.Title = chart.Strips[0].Title
	if len(ms) > 1 {
		chart.Title = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	chart.Title += " against " + against
	if smooth > 0 {
		chart.Title += fmt.Sprintf(", smoothed over %s", smooth)
	}
	return chart, nil
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
// Power's source is named beside it, as its chart's title names it.
func writePlotMetrics(w io.Writer, track *fitactivity.Track, ms []plotMetric) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "--y\tunit\tcoverage")
	for _, m := range ms {
		c := m.coverage(track)
		if c == 0 {
			continue
		}
		name := m.name
		if strings.ContainsRune(name, ' ') {
			name = strconv.Quote(name)
		}
		unit := m.unit
		if m.from != "" {
			unit += ", " + m.from
		}
		fmt.Fprintf(tw, "%s\t%s\t%.1f%%\n", name, unit, 100*c)
	}
	tw.Flush()
}
