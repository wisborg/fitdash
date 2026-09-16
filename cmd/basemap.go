package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/osmbase/acquire"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/output"
	"github.com/wisborg/output/progress"
	"github.com/wisborg/output/table"

	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

var basemapCmd = &cobra.Command{
	Use:   "basemap",
	Short: "Manage the map data a local basemap is drawn from",
	Long: `basemap manages the OpenStreetMap data that "--basemap local" draws from.

A local basemap is drawn on this machine from vector tiles on this machine, so
a render contacts nobody and works on a plane. The data has to get there once,
which is what "basemap fetch" is for -- and that fetch is the only moment
anything about your activity's whereabouts crosses the network.`,
}

var basemapFetchCmd = &cobra.Command{
	Use:   "fetch ACTIVITY.fit [ACTIVITY.fit ...]",
	Short: "Download the map data around an activity, once",
	Long: `fetch copies the map around an activity onto this machine, once, so that
"fitdash ACTIVITY.fit --basemap local" can draw it without contacting anybody.

It takes ACTIVITIES rather than coordinates. fitdash already knows where the
activity went, so the area is computed from the samples that carry a GPS fix,
padded by --pad, and rounded outward to whole cells -- squares a few kilometres
across. An activity with no fix is refused rather than guessed at: a bounding
box taken without checking would average absent positions into 0,0 and download
the Gulf of Guinea.

Several activity files are merged into one activity exactly as a render merges
them, so the area covers all of them.

How deep the data goes follows the size of the area, because a park run and a
flight want different treatment -- an 8 km box is fetched to street detail and a
continental one to coastlines. Pass --max-zoom to say instead.

WHAT THE HOST LEARNS is the cells you asked for, once: squares several
kilometres across, at the time of the fetch. Not the route, not the times, and
nothing at all when you render afterwards. Pass a local .pmtiles file to
--source and it learns nothing even now. The size of the download is stated
exactly before anything is transferred -- it is read from the archive's own
directories rather than estimated -- and nothing is written until you agree to
it.

examples:
  fitdash basemap fetch run.fit --dry-run
  fitdash basemap fetch run.fit
  fitdash basemap fetch morning.fit race.fit --pad 5
  fitdash ACTIVITY.fit --basemap local`,
	Args: cobra.MinimumNArgs(1),
	RunE: runBasemapFetch,
}

// basemapFetchOptions are `basemap fetch`'s flags.
type basemapFetchOptions struct {
	store   string
	source  string
	pad     float64
	maxZoom int
	dryRun  bool
	yes     bool
}

var basemapFetchOpts basemapFetchOptions

func init() {
	f := basemapFetchCmd.Flags()
	f.StringVar(&basemapFetchOpts.store, "store", "",
		"directory to keep the map data in (default: the osmbase folder under your user cache directory). It is the "+
			"same directory --basemap-store names at render time, and it is deliberately shared between programs "+
			"rather than private to fitdash: download an area once and anything else built on the same library "+
			"renders from it")
	f.StringVar(&basemapFetchOpts.source, "source", "",
		"the map archive to copy from: an https URL, or the path to a local .pmtiles file, which reaches no network "+
			"at all (default: "+tilemap.DefaultArchive+")")
	f.Float64Var(&basemapFetchOpts.pad, "pad", 2,
		"how far beyond the activity to fetch, in kilometres. The route is drawn inside a panel with its own margins, "+
			"and a zoomed --highlight reaches further still, so a box drawn tight around the fixes leaves the map "+
			"stopping short of the frame")
	f.IntVar(&basemapFetchOpts.maxZoom, "max-zoom", acquire.AutoZoom,
		"deepest zoom to take. The default chooses one from the size of the area, which is almost always what you "+
			"want: depth costs four times as much per level, and a route that crosses a country does not want the "+
			"detail a parkrun does")
	f.BoolVar(&basemapFetchOpts.dryRun, "dry-run", false,
		"say exactly what would be downloaded and stop. Nothing is transferred and nothing is written -- not even "+
			"the store directory -- so this is also the way to see what an area costs before creating one")
	f.BoolVar(&basemapFetchOpts.yes, "yes", false,
		"do not ask before downloading. The confirmation exists because this is the one command that sends anything "+
			"anywhere; skip it in a script that has already made that decision")

	basemapCmd.AddCommand(basemapFetchCmd)
	root.AddCommand(basemapCmd)
}

// fetchReport is what the command produces, for --format to render.
//
// Built as data and printed once, at the end, so that `--format json` gets one
// document rather than a narrative with a result somewhere in it. The
// narrative -- the plan, the privacy statement, the prompt, the progress bar
// -- goes to stderr, which is where this repository already puts a render's
// summary, and it is what a person reads while deciding. This is what they
// have afterwards.
type fetchReport struct {
	Store     string   `json:"store"`
	Archive   string   `json:"archive"`
	Remote    bool     `json:"remote"`
	Paths     []string `json:"paths"`
	Bounds    bounds   `json:"bounds"`
	WidthKM   float64  `json:"width_km"`
	HeightKM  float64  `json:"height_km"`
	PadKM     float64  `json:"pad_km"`
	MinZoom   uint8    `json:"min_zoom"`
	MaxZoom   uint8    `json:"max_zoom"`
	Depth     string   `json:"depth,omitempty"`
	Cells     int      `json:"cells"`
	CellsToDo int      `json:"cells_to_fetch"`
	Tiles     int      `json:"tiles_to_fetch"`
	Held      int      `json:"tiles_already_held"`
	Absent    int      `json:"tiles_not_in_archive"`
	Transfer  int64    `json:"download_bytes"`
	Requests  int      `json:"requests"`

	// Credit is who the map data is owed to, in the plain text a frame can
	// carry rather than the markup the archive wrote. It is recorded here,
	// and printed, because this command is the moment the obligation is
	// taken on: from here the data is on the user's disk and every render
	// from it carries this line. A fetch that never said whose data it was
	// downloading leaves the user to discover the credit in their own video.
	Credit string `json:"credit,omitempty"`

	// Fetched is nil when nothing was downloaded, which covers three
	// different endings -- a dry run, an area already held, and a prompt
	// answered no -- so Outcome says which.
	Outcome string       `json:"outcome"`
	Fetched *fetchedInfo `json:"fetched,omitempty"`
}

type bounds struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}

type fetchedInfo struct {
	Tiles    int     `json:"tiles"`
	Bytes    int64   `json:"bytes"`
	Transfer int64   `json:"download_bytes"`
	Requests int     `json:"requests"`
	Cells    int     `json:"cells"`
	Seconds  float64 `json:"seconds"`
}

func runBasemapFetch(cmd *cobra.Command, args []string) error {
	// The same decode-and-merge a render performs. A workout recorded in
	// pieces is ONE activity, so the box has to cover all of it -- fetching
	// for the first file would leave the map stopping where the second began.
	track, sources, err := decodeActivities(args)
	if err != nil {
		return fmt.Errorf("basemap fetch: %w", err)
	}
	area, err := activityBounds(track, basemapFetchOpts.pad)
	if err != nil {
		return err
	}

	root, err := fetchStoreDir()
	if err != nil {
		return err
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

	archive, err := openFetchArchive(errw)
	if err != nil {
		return err
	}
	defer archive.Close()
	// Read BEFORE the plan, because this is the cheapest reason to stop: a
	// store fitdash cannot credit is a store fitdash refuses to draw from,
	// and finding that out after the download is finding it out too late.
	credit, err := archive.Attribution()
	if err != nil {
		return fmt.Errorf("basemap fetch: %w", err)
	}

	// Opened, never created: a dry run must leave no trace, and a store that
	// does not exist yet holds nothing, which is exactly what planning needs
	// to know. The cell zoom is the store's own when there is one, because it
	// is recorded in its manifest precisely so a later change of default does
	// not orphan what is already on disk.
	store, src, cellZoom := openStoreForPlanning(root, archive)

	plan, err := archive.Plan(fetchContext(cmd), src, area, cellZoom, basemapFetchOpts.maxZoom)
	if err != nil {
		return fmt.Errorf("basemap fetch: %w", err)
	}
	rep := newFetchReport(root, archive, credit, sources, area, plan)

	writeFetchPlan(errw, rep, plan)
	switch {
	case plan.Empty():
		rep.Outcome = "nothing to fetch: the store already holds this area"
		fmt.Fprintf(errw, "\n%s.\n", rep.Outcome)
		return writeFetchReport(out, rep, plan)
	case basemapFetchOpts.dryRun:
		rep.Outcome = "dry run: nothing was downloaded and nothing was written"
		fmt.Fprintf(errw, "\n%s.\n", rep.Outcome)
		return writeFetchReport(out, rep, plan)
	}
	if archive.Remote() && !basemapFetchOpts.yes {
		ok, err := confirmFetch(cmd, plan, archive)
		if err != nil {
			return err
		}
		if !ok {
			rep.Outcome = "stopped; nothing was downloaded"
			fmt.Fprintf(errw, "%s.\n", rep.Outcome)
			return writeFetchReport(out, rep, plan)
		}
	}

	if store == nil {
		if store, err = slice.Create(root, slice.Config{}); err != nil {
			return fmt.Errorf("basemap fetch: creating the map store at %s: %w", root, err)
		}
	}
	if src, err = archive.AddTo(store, credit); err != nil {
		return fmt.Errorf("basemap fetch: %w", err)
	}

	// The trace and the bar cannot share a stream: the carriage return that
	// redraws the bar lands in the middle of a trace line and both become
	// unreadable. Planning keeps its trace, because planning is the slow
	// silent part and has no bar to fight with.
	archive.Silence()
	display, bar := newFetchBar(errw, plan)
	res, err := archive.Fetch(fetchContext(cmd), plan, src, func(pr acquire.Progress) {
		bar.Set(pr.DoneTransfer)
	})
	bar.Done()
	display.Stop()
	if err != nil {
		return fmt.Errorf("basemap fetch: %w", err)
	}

	rep.Outcome = "fetched"
	rep.Fetched = &fetchedInfo{
		Tiles: res.Written, Bytes: res.Bytes, Transfer: res.Transfer,
		Requests: res.Requests, Cells: len(res.Cells),
		Seconds: res.Elapsed.Seconds(),
	}
	writeFetchResult(errw, rep, res, plan, store, root)
	return writeFetchReport(out, rep, plan)
}

// activityBounds is the ground the activity covered, padded by padKM.
//
// # GPS-present samples only
//
// This is the Null Island trap in the one place it can still be walked into.
// Sample.Lat and Sample.Lon are meaningless when HasGPS is false -- they are
// what a decoded sentinel left behind, which is zero -- so a box taken over
// every sample stretches from the activity to 0,0 in the Gulf of Guinea, and
// the fetch that follows is not wrong in a way anybody notices until it is
// several hundred megabytes in. route.FromTrack keeps fixes and only fixes,
// which is the same filter the route panel draws through, so this box and the
// drawn line cannot disagree about where the activity was.
//
// The cap is the sample count rather than route.DefaultMaxPoints: that
// constant thins the outline down to what a few hundred pixels can show, and
// a box drawn from a thinned track can fall short of the real extreme. Here
// every fix counts, since dropping the one that reached furthest is exactly
// the point where the map stops before the route does.
//
// # Why fewer than two fixes is refused
//
// A pool swim, a turbo session and a treadmill run carry no position at all,
// and there is no sensible area to fetch for them -- so the refusal names the
// reason rather than fetching something. One fix is refused for the same
// reason: it is a lock the watch got once, not a route, and honouring it
// would download a city around wherever the receiver happened to settle.
func activityBounds(track *fitactivity.Track, padKM float64) (slice.Bounds, error) {
	if padKM < 0 {
		return slice.Bounds{}, fmt.Errorf("basemap fetch: --pad %g is not a distance", padKM)
	}
	total := 0
	if track != nil {
		total = len(track.Samples)
	}
	pts := route.FromTrack(track, total)
	if len(pts) < 2 {
		return slice.Bounds{}, fmt.Errorf(
			"basemap fetch: this activity carries a GPS fix in %d of its %d samples, and an area needs at least two; "+
				"an activity recorded indoors -- a pool swim, a turbo session, a treadmill run -- has no ground to fetch a map of, "+
				"and a box taken from samples without a fix would name the Gulf of Guinea",
			len(pts), total)
	}

	b := slice.Bounds{
		West: math.Inf(1), South: math.Inf(1),
		East: math.Inf(-1), North: math.Inf(-1),
	}
	for _, p := range pts {
		b.West, b.East = math.Min(b.West, p.Lon), math.Max(b.East, p.Lon)
		b.South, b.North = math.Min(b.South, p.Lat), math.Max(b.North, p.Lat)
	}
	return padBounds(b, padKM)
}

// padBounds widens a box by padKM on every side.
//
// The kilometres-to-degrees conversion is asked of acquire.BoundsAround --
// once per corner, taking the outer edge of each -- rather than spelled again
// here. It is the same arithmetic osmbase does for its own --radius, including
// the cosine taken at the latitude furthest from the equator so the box
// contains the intended distance along its whole length rather than only at
// its middle, and a second copy of it would be a second answer to how wide a
// kilometre is.
//
// It is also what makes a degenerate box an area: an activity whose every fix
// landed on the same point -- a watch that locked once and never moved -- has
// no width at all, and a pad turns it into a square rather than into an error
// from three layers down.
func padBounds(b slice.Bounds, padKM float64) (slice.Bounds, error) {
	if padKM > 0 {
		sw, err := acquire.BoundsAround(b.South, b.West, padKM)
		if err != nil {
			return slice.Bounds{}, fmt.Errorf("basemap fetch: padding the area by %g km: %w", padKM, err)
		}
		ne, err := acquire.BoundsAround(b.North, b.East, padKM)
		if err != nil {
			return slice.Bounds{}, fmt.Errorf("basemap fetch: padding the area by %g km: %w", padKM, err)
		}
		b = slice.Bounds{West: sw.West, South: sw.South, East: ne.East, North: ne.North}
	}
	if b.East <= b.West || b.North <= b.South {
		return slice.Bounds{}, fmt.Errorf(
			"basemap fetch: every GPS fix in this activity is at the same place, so the area to fetch has no size; pass --pad to fetch the ground around it")
	}
	return b, nil
}

// fetchStoreDir resolves --store, defaulting to the store osmbase shares
// between programs -- the same default --basemap-store takes at render time,
// asked of the same function so the two cannot drift apart.
func fetchStoreDir() (string, error) {
	if basemapFetchOpts.store != "" {
		return basemapFetchOpts.store, nil
	}
	dir, err := tilemap.DefaultStoreDir()
	if err != nil {
		return "", fmt.Errorf("basemap fetch: %w; pass --store to say where to keep the map data", err)
	}
	return dir, nil
}

// openFetchArchive opens --source, announcing on stderr anything that reaches
// the network before it is reached.
//
// The announcement is not decoration. This command is the one moment fitdash
// contacts anybody, and a program that does it silently on the user's behalf
// is the thing this repository exists not to be. It is also slow enough --
// planning is a few tens of range requests against a host on the other side
// of the world -- that a blank terminal reads as a hang, which is what the
// per-request trace is for.
func openFetchArchive(w io.Writer) (*tilemap.Archive, error) {
	source := basemapFetchOpts.source
	if source == "" {
		fmt.Fprintf(w, "fitdash: no --source given, so reading the default map archive over the network:\n")
		fmt.Fprintf(w, "fitdash:   %s\n", tilemap.DefaultArchive)
		fmt.Fprintf(w, "fitdash: this tells that host which few-kilometre squares of the map you asked about.\n")
		fmt.Fprintf(w, "fitdash:   Pass --source with a local .pmtiles file to avoid it.\n")
	}
	a, err := tilemap.OpenArchive(source, func(line string) {
		fmt.Fprintf(w, "fitdash: %s\n", line)
	})
	if err != nil {
		return nil, fmt.Errorf("basemap fetch: %w", err)
	}
	if a.Remote() && source != "" {
		fmt.Fprintf(w, "fitdash: reading %s over HTTP range requests\n", a.Name())
	}
	return a, nil
}

// openStoreForPlanning opens the store if it is there, and reports what a plan
// needs to know when it is not.
//
// Every return is usable: a nil store and a nil source mean "holds nothing",
// which is the truth about a machine that has never fetched, and the cell zoom
// falls back to the default slice.Create would have chosen -- so the plan a dry
// run prints for a fresh machine is the plan the first real fetch carries out.
func openStoreForPlanning(root string, a *tilemap.Archive) (*slice.Store, *slice.Source, uint8) {
	store, err := slice.Open(root)
	if err != nil {
		return nil, nil, slice.DefaultCellZoom
	}
	// A source the store does not hold yet is not an error here: it is a
	// first fetch from this archive into a store filled from another. What it
	// must not be is a DIFFERENT source, which is why the identity comes from
	// the archive rather than from whichever source happens to be newest --
	// planning against another build's tiles would report an area as held
	// when none of these tiles are there.
	src, err := store.Source(a.SourceID())
	if err != nil {
		src = nil
	}
	return store, src, store.CellZoom()
}

// fetchContext is the context the plan and the fetch run under.
//
// cobra fills one in during Execute, and a test that calls RunE directly does
// not, so this is what keeps a nil out of osmbase's ctx.Done().
func fetchContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// newFetchBar builds the progress display for the transfer, or a pair of nils
// -- both usable and both silent -- when there is no terminal to draw on.
//
// The bar counts BYTES rather than tiles or groups. A group is one range
// request and a tile is not an event the network knows about, so counting
// either would report a hundred and eighty tiles arriving in twenty jumps;
// groups differ in size by two orders of magnitude. Bytes are also the number
// the prompt was answered against, which is the point of showing progress at
// all: it is the figure the user agreed to, filling up.
func newFetchBar(w io.Writer, p *acquire.Plan) (*progress.Display, *progress.Bar) {
	d := progress.New(w, progress.Options{Palette: progress.DefaultGradient()})
	return d, d.Bar(progress.BarSpec{Label: "map data", Total: p.Transfer, Unit: "B"})
}

// writeFetchPlan states the cost before anything is downloaded.
//
// The byte figure is EXACT rather than estimated, and that is a property of
// the archive format rather than a claim about this code: the planner reads
// the archive's own directories, so what is printed here is what the download
// will weigh rather than a guess that could be out by a factor of two. A
// prompt that can state the true cost is a different thing from one that
// hedges, and it is the reason this command asks at all instead of just
// downloading.
func writeFetchPlan(w io.Writer, rep fetchReport, p *acquire.Plan) {
	fmt.Fprintf(w, "%-12s %s\n", "source", rep.Archive)
	fmt.Fprintf(w, "%-12s %s\n", "store", rep.Store)
	fmt.Fprintf(w, "%-12s west %.4f, south %.4f, east %.4f, north %.4f\n",
		"area", rep.Bounds.West, rep.Bounds.South, rep.Bounds.East, rep.Bounds.North)
	fmt.Fprintf(w, "%-12s %.1f by %.1f km, including %.1f km of padding\n",
		"", rep.WidthKM, rep.HeightKM, rep.PadKM)
	fmt.Fprintf(w, "%-12s %d of %d cells, zooms %d to %d\n",
		"cells", rep.CellsToDo, rep.Cells, rep.MinZoom, rep.MaxZoom)
	if rep.Depth != "" {
		fmt.Fprintf(w, "%-12s %s\n", "", rep.Depth)
	}
	fmt.Fprintf(w, "%-12s %d to fetch", "tiles", rep.Tiles)
	if rep.Held > 0 {
		fmt.Fprintf(w, ", %d already held", rep.Held)
	}
	if rep.Absent > 0 {
		fmt.Fprintf(w, ", %d not in the archive", rep.Absent)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-12s %s in %d range requests", "download", humanBytes(rep.Transfer), rep.Requests)
	if p.Transfer > p.Bytes {
		fmt.Fprintf(w, " (%s of it is gaps joined to save requests)", humanBytes(p.Transfer-p.Bytes))
	}
	fmt.Fprintln(w)
}

// confirmFetch puts the decision in front of the user, once.
//
// A closed or empty stdin is a no rather than an error: a pipeline that
// reached this prompt did not mean to download anything, and a program that
// took silence for consent here would be doing the one thing this command is
// careful not to.
func confirmFetch(cmd *cobra.Command, p *acquire.Plan, a *tilemap.Archive) (bool, error) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "\nThis contacts %s and downloads %s.\n", hostOf(a.Name()), humanBytes(p.Transfer))
	fmt.Fprintf(w, "It tells that host which cells you asked for, once. Rendering afterwards contacts nobody.\n")
	fmt.Fprintf(w, "Continue? [y/N] ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// writeFetchResult says what actually happened, and what to do with it.
func writeFetchResult(w io.Writer, rep fetchReport, res acquire.Result, p *acquire.Plan, store *slice.Store, root string) {
	fmt.Fprintf(w, "%-12s %d tiles in %d requests, %s in %s\n",
		"fetched", res.Written, res.Requests, humanBytes(res.Transfer), res.Elapsed.Round(time.Millisecond))
	if res.Transfer != p.Transfer {
		// Said out loud rather than hidden: the plan's figure is the one the
		// prompt was answered against, so a difference is the user's to see.
		fmt.Fprintf(w, "%-12s the plan said %s\n", "", humanBytes(p.Transfer))
	}
	if n, err := store.Bytes(); err == nil {
		fmt.Fprintf(w, "%-12s %s at %s\n", "store", humanBytes(n), root)
	}
	if rep.Credit != "" {
		fmt.Fprintf(w, "%-12s %s\n", "credit", rep.Credit)
	}
	fmt.Fprintf(w, "\nNow: fitdash ACTIVITY.fit --basemap %s", tilemap.LocalProvider)
	if basemapFetchOpts.store != "" {
		fmt.Fprintf(w, " --basemap-store %s", root)
	}
	fmt.Fprintf(w, "\nNothing about that reaches the network.\n")
}

// writeFetchReport writes the machine-readable record, once, at the end.
//
// Two representations built independently, as cmd/inspect does: the object
// carries the whole report for JSON and YAML, the table carries the rows
// worth reading on a terminal. The table is one row per GROUP -- the shared
// overview and then one cell at a time -- because that is the only repeated
// record here and because it is precisely what the privacy statement above is
// about: these cells, and nothing else, are what the host was told. The
// activity-wide totals are fields on the object and lines on stderr rather
// than rows in it, since a table whose first rows are "area" and "download"
// puts two kinds of thing in one column.
func writeFetchReport(w io.Writer, rep fetchReport, p *acquire.Plan) error {
	t := table.New(
		table.Column{Header: "group"},
		table.Column{Header: "tiles", Align: table.Right},
		table.Column{Header: "download", Align: table.Right},
		table.Column{Header: "requests", Align: table.Right},
	)
	for _, g := range p.Groups {
		t.MustAppend(g.Label(), len(g.Tiles), humanBytes(g.Transfer), len(g.Ranges))
	}
	doc := output.Document{Data: rep, Table: t}
	if err := doc.Write(w, format.Format); err != nil {
		return fmt.Errorf("basemap fetch: writing the report: %w", err)
	}
	return nil
}

// newFetchReport assembles the record from the plan, which is where every
// figure in it comes from -- none of them are this command's arithmetic.
func newFetchReport(root string, a *tilemap.Archive, credit string, sources []activitySource, b slice.Bounds, p *acquire.Plan) fetchReport {
	w, h := acquire.ExtentKM(b)
	rep := fetchReport{
		Store: root, Archive: a.Name(), Remote: a.Remote(), Credit: osm.PlainCredit(credit),
		Bounds:  bounds{West: b.West, South: b.South, East: b.East, North: b.North},
		WidthKM: w, HeightKM: h, PadKM: basemapFetchOpts.pad,
		MinZoom: p.Zoom.Min, MaxZoom: p.Zoom.Max,
		Depth: p.Depth.Why,
		Cells: len(p.Cells), CellsToDo: p.CellsToFetch,
		Tiles: p.Tiles - p.Held - p.Absent,
		Held:  p.Held, Absent: p.Absent,
		Transfer: p.Transfer, Requests: p.Requests,
	}
	for _, s := range sources {
		rep.Paths = append(rep.Paths, s.Path)
	}
	return rep
}

// hostOf is the host a URL names, for the one sentence that says who is being
// contacted. A path with no host comes back whole rather than empty, since the
// alternative is a prompt that names nobody.
func hostOf(archive string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(archive, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return archive
	}
	return s
}

// humanBytes renders a size the way osmbase's own fetch does, deliberately to
// the digit: the two commands fill the same store from the same archives, and
// a download reported as "41.6 MiB" by one and "43,619,020 bytes" by the other
// invites the reader to think they are different numbers.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
