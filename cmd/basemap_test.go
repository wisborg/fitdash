package cmd

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/output"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// The synthetic ground these tests use, which is fittest's own default start
// and deliberately nowhere: nothing derived from a real recording -- not a
// coordinate, not a date -- belongs in a committed test.
const (
	fetchLat = 12.345678
	fetchLon = 98.765432
)

// fetchArchiveCredit is what the fixture archive's metadata declares, in the
// HTML an archive built for a web map actually carries.
const fetchArchiveCredit = `<a href="https://example.test/copyright" target="_blank">&copy; OpenStreetMap contributors</a>`

// TestActivityBounds_RefusesAnActivityWithNoGPSRatherThanFetchingNullIsland is
// the Null Island trap, checked at the one place it can still be walked into.
//
// fitactivity decodes an absent position as HasGPS false and leaves Lat and
// Lon at what the sentinel became, which is zero. A bounding box taken over
// every sample therefore reaches from wherever the activity was to 0,0 in the
// Gulf of Guinea -- an area of several thousand kilometres, which the depth
// rule answers by fetching the whole world. Nothing about that looks like a
// bug from the outside: the download completes and the store fills.
//
// The samples below carry exactly that shape, an indoor activity with a heart
// rate and no fix, and one fix is refused for its own reason: a single lock is
// not a route, and honouring it would download a city around wherever the
// receiver settled.
// This is the ONLY place the rule can be checked, and that is worth writing
// down. The fixture generator always writes positions, so no .fit file it can
// produce carries a sample with HasGPS false -- a command-level test of this
// refusal would be asserting something its own fixture cannot express, and one
// written that way passed for the wrong reason until the track above was built
// in memory instead.
func TestActivityBounds_RefusesAnActivityWithNoGPSRatherThanFetchingNullIsland(t *testing.T) {
	start := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	indoor := &fitactivity.Track{}
	for i := range 600 {
		indoor.Samples = append(indoor.Samples, fitactivity.Sample{
			Time:         start.Add(time.Duration(i) * time.Second),
			HasHeartRate: true, HeartRate: 140,
		})
	}
	oneFix := &fitactivity.Track{Samples: append([]fitactivity.Sample(nil), indoor.Samples...)}
	oneFix.Samples[42].HasGPS = true
	oneFix.Samples[42].Lat, oneFix.Samples[42].Lon = fetchLat, fetchLon

	for _, c := range []struct {
		name  string
		track *fitactivity.Track
	}{
		{"no fix at all", indoor},
		{"one fix", oneFix},
		{"no samples", &fitactivity.Track{}},
		{"no track", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := activityBounds(c.track, 2)
			if err == nil {
				t.Fatalf("accepted an activity with no route and returned %+v", b)
			}
			if !strings.Contains(err.Error(), "GPS") {
				t.Errorf("the refusal does not say what is missing: %v", err)
			}
		})
	}
}

// TestActivityBounds_CoversEveryFixWithThePadAroundIt checks the box against
// two properties rather than against coordinates this code produced.
//
// The first is containment: every fix has to be inside, because a fix outside
// the box is ground the map stops short of, which shows up in a render as a
// route leaving the map at one end.
//
// The second is the size of the pad, derived independently: padding by p
// kilometres on every side widens the area by 2p kilometres in each
// direction, and acquire.ExtentKM measures that in kilometres from the
// degrees. The tolerance is one per cent, which covers the conservative
// direction of the longitude conversion -- a degree of longitude is measured
// at the latitude furthest from the equator, so the pad is never short and is
// fractionally generous at the ends of a north-south route.
func TestActivityBounds_CoversEveryFixWithThePadAroundIt(t *testing.T) {
	const padKM = 1.5
	start := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	track := &fitactivity.Track{}
	for i := range 60 {
		track.Samples = append(track.Samples, fitactivity.Sample{
			Time:   start.Add(time.Duration(i) * time.Second),
			HasGPS: true,
			Lat:    fetchLat + float64(i)*0.0005,
			Lon:    fetchLon - float64(i)*0.0003,
		})
	}
	// One dropout in the middle, whose Lat and Lon are the zeros an absent
	// position decodes to. It must not widen the box.
	track.Samples[30].HasGPS, track.Samples[30].Lat, track.Samples[30].Lon = false, 0, 0

	raw := slice.Bounds{
		West:  fetchLon - 59*0.0003,
		South: fetchLat,
		East:  fetchLon,
		North: fetchLat + 59*0.0005,
	}
	b, err := activityBounds(track, padKM)
	if err != nil {
		t.Fatalf("activityBounds: %v", err)
	}
	for _, s := range track.Samples {
		if !s.HasGPS {
			continue
		}
		if s.Lat < b.South || s.Lat > b.North || s.Lon < b.West || s.Lon > b.East {
			t.Fatalf("fix %v,%v is outside the area %+v", s.Lat, s.Lon, b)
		}
	}
	// The dropout's zeros must not have widened the box toward Null Island.
	// The fixture sits near 12 N, 98 E, so a box that reached 0,0 would have
	// its south or west edge near zero.
	if b.South < 0.5 || b.West < 1 {
		t.Fatalf("the area %+v was dragged toward 0,0 by a sample with no fix", b)
	}

	rawW, rawH := acquire.ExtentKM(raw)
	gotW, gotH := acquire.ExtentKM(b)
	for _, c := range []struct {
		axis     string
		raw, got float64
	}{{"width", rawW, gotW}, {"height", rawH, gotH}} {
		want := c.raw + 2*padKM
		if math.Abs(c.got-want) > 0.01*want {
			t.Errorf("%s is %.3f km, want %.3f km (%.3f plus twice the %.1f km pad)",
				c.axis, c.got, want, c.raw, padKM)
		}
	}
}

// TestRunBasemapFetch_ADryRunWritesNothingAndReachesNoFurtherThanPlanning is
// the promise --dry-run makes, and both halves of it are checked because
// either alone would pass for the wrong reason.
//
// Nothing written means nothing AT ALL: not a tile, not a manifest, not the
// store directory. A dry run that created an empty store would leave a
// directory the user did not ask for, and -- worse -- one whose cell zoom is
// then fixed for every later fetch, so the plan printed for a fresh machine
// would stop being the plan that machine carries out.
//
// Reaching planning means the report is real. A dry run that refused early
// would also write nothing, and would say nothing useful either; the
// assertion below is that it costed a non-empty plan, which is only possible
// after reading the archive's directories.
func TestRunBasemapFetch_ADryRunWritesNothingAndReachesNoFurtherThanPlanning(t *testing.T) {
	dir := t.TempDir()
	archive := buildFetchArchive(t, dir)
	store := filepath.Join(dir, "store")
	fit := writeFetchActivity(t, dir)

	rep := runFetch(t, nil, "--dry-run", "--source", archive, "--store", store, "--max-zoom", "12", fit)

	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("a dry run created %s; it must write nothing at all", store)
	}
	if rep.Tiles <= 0 {
		t.Errorf("the plan says %d tiles to fetch, so this never reached the archive's directories", rep.Tiles)
	}
	if rep.Transfer <= 0 {
		t.Errorf("the plan says %d bytes to download, which is not a costed plan", rep.Transfer)
	}
	if rep.Fetched != nil {
		t.Errorf("a dry run reported a transfer: %+v", rep.Fetched)
	}
	if !strings.Contains(rep.Outcome, "dry run") {
		t.Errorf("outcome = %q, want it to say this was a dry run", rep.Outcome)
	}
}

// TestRunBasemapFetch_FillsAStoreTheRenderCanDrawFrom is the whole point of
// the command in one assertion: what it writes has to be what --basemap local
// opens. Fetching into a store the renderer then refuses is the failure this
// pairing exists to prevent, and it is invisible until somebody renders.
//
// The credit is checked here too, and deliberately end to end: the archive's
// metadata carries HTML, because archives are built for web maps, and what
// comes back out at the far end has to be the text a frame can carry.
func TestRunBasemapFetch_FillsAStoreTheRenderCanDrawFrom(t *testing.T) {
	dir := t.TempDir()
	archive := buildFetchArchive(t, dir)
	store := filepath.Join(dir, "store")
	fit := writeFetchActivity(t, dir)

	rep := runFetch(t, nil, "--source", archive, "--store", store, "--max-zoom", "12", fit)
	if rep.Fetched == nil {
		t.Fatalf("nothing was fetched: %+v", rep)
	}
	if rep.Fetched.Tiles == 0 {
		t.Error("the fetch wrote no tiles")
	}

	// Real inks, not a zero MapInks. The zero value has nil colours in every
	// field, which is not a palette this program can ever build -- and passing
	// it here tested the store round trip through a code path no render takes.
	p, err := tilemap.OpenLocal(store, mapInksFor(panel.DefaultTheme()), nil)
	if err != nil {
		t.Fatalf("the render cannot open the store this command just filled: %v", err)
	}
	if got, want := p.Attribution(), "© OpenStreetMap contributors"; got != want {
		t.Errorf("Attribution() = %q, want %q -- the archive's HTML credit unwrapped to text", got, want)
	}

	// Run again: the area is now held, so there is nothing to fetch and the
	// command says so instead of downloading it twice.
	again := runFetch(t, nil, "--source", archive, "--store", store, "--max-zoom", "12", fit)
	if again.Fetched != nil {
		t.Errorf("the second run fetched %+v; the store already holds this area", again.Fetched)
	}
	if !strings.Contains(again.Outcome, "already holds") {
		t.Errorf("outcome = %q, want it to say the store already holds the area", again.Outcome)
	}
}

// TestRunBasemapFetch_RefusesAnArchiveThatCreditsNobodyBeforeDownloading
// checks that the obligation is settled before any bytes move rather than
// after. A store with no attribution is one OpenLocal refuses to draw from,
// so fetching into it spends somebody's bandwidth on tiles fitdash will not
// use -- and the user finds out at render time, with no clue which of the two
// commands was wrong.
func TestRunBasemapFetch_RefusesAnArchiveThatCreditsNobodyBeforeDownloading(t *testing.T) {
	dir := t.TempDir()
	archive := buildArchiveFile(t, dir, "nocredit.pmtiles", "")
	store := filepath.Join(dir, "store")
	fit := writeFetchActivity(t, dir)

	err := runFetchErr(t, nil, "--source", archive, "--store", store, "--max-zoom", "12", fit)
	if err == nil {
		t.Fatal("an archive declaring no attribution was fetched from")
	}
	if !strings.Contains(err.Error(), "attribution") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("a refused fetch created %s", store)
	}
}

// TestConfirmFetch_TreatsSilenceAndAnythingButYesAsNo pins the answer to the
// one prompt in this program that sends data somewhere. Defaulting to yes on
// an empty line, or on a stdin that is closed because this is a pipeline,
// would be consent nobody gave.
func TestConfirmFetch_TreatsSilenceAndAnythingButYesAsNo(t *testing.T) {
	for _, c := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true}, {" yes \n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"no\n", false}, {"maybe\n", false},
	} {
		cmd := &cobra.Command{}
		var prompt strings.Builder
		cmd.SetErr(&prompt)
		cmd.SetIn(strings.NewReader(c.answer))
		got, err := confirmFetch(cmd, &acquire.Plan{}, "https://example.test/planet.pmtiles")
		if err != nil {
			t.Fatalf("confirmFetch(%q): %v", c.answer, err)
		}
		if got != c.want {
			t.Errorf("confirmFetch(%q) = %v, want %v", c.answer, got, c.want)
		}
		// The prompt has to name the host being contacted. It is the one
		// piece of information the answer depends on, and a prompt that asked
		// "Continue?" without saying with whom would be asking for consent to
		// something unstated.
		if !strings.Contains(prompt.String(), "example.test") {
			t.Errorf("the prompt does not name the host it would contact:\n%s", prompt.String())
		}
	}
}

// runFetch drives the command through cobra exactly as Execute does and
// returns the report it wrote to stdout, decoded from JSON so the assertions
// are about the data rather than about the text around it.
func runFetch(t *testing.T, in io.Reader, args ...string) fetchReport {
	t.Helper()
	out := &strings.Builder{}
	if err := execFetch(t, out, in, args...); err != nil {
		t.Fatalf("basemap fetch %v: %v", args, err)
	}
	var rep fetchReport
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("decoding the report %q: %v", out.String(), err)
	}
	return rep
}

func runFetchErr(t *testing.T, in io.Reader, args ...string) error {
	t.Helper()
	return execFetch(t, io.Discard, in, args...)
}

func execFetch(t *testing.T, out io.Writer, in io.Reader, args ...string) error {
	t.Helper()
	return execFetchTo(t, out, io.Discard, in, args...)
}

// execFetchTo is execFetch with the human-readable half kept as well. Most
// assertions here are about the JSON report on stdout; the few that are about
// what a person reads need the other stream.
func execFetchTo(t *testing.T, out, errw io.Writer, in io.Reader, args ...string) error {
	t.Helper()
	redirectUserCache(t)
	defer func(o basemapFetchOptions, f formatFlag) {
		basemapFetchOpts, format = o, f
	}(basemapFetchOpts, format)
	basemapFetchOpts = basemapFetchOptions{}
	format = formatFlag{Format: output.JSON}

	cmd := &cobra.Command{Use: "fetch", Args: cobra.MinimumNArgs(1), RunE: runBasemapFetch, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().AddFlagSet(basemapFetchCmd.Flags())
	// Put the defaults back, because zeroing the options struct above threw
	// them away. AddFlagSet shares the flag objects rather than copying them,
	// and each one holds a pointer into that struct; cobra writes a value
	// through it only when the flag is GIVEN, having applied the default once
	// at registration. So a zeroed struct silently runs every test with
	// --pad 0 and --max-zoom 0 -- which is how three tests here came to fail
	// against correct code, reporting that a route running due north was "at
	// the same place".
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("restoring the default for --%s: %v", f.Name, err)
		}
	})
	cmd.SetOut(out)
	cmd.SetErr(errw)
	if in != nil {
		cmd.SetIn(in)
	}
	cmd.SetArgs(args)
	return cmd.Execute()
}

// writeFetchActivity generates a short synthetic activity on the fixture
// ground: ten minutes at 3 m/s, which is under two kilometres and so lands in
// one or two cells.
func writeFetchActivity(t *testing.T, dir string) string {
	t.Helper()
	opts := fittest.DefaultOptions()
	opts.Count = 600
	opts.StartLat, opts.StartLon = fetchLat, fetchLon
	path := filepath.Join(dir, "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating the activity fixture: %v", err)
	}
	return path
}

// buildFetchArchive writes a synthetic PMTiles archive holding the tiles the
// fixture activity's area needs, with an HTML attribution in its metadata.
func buildFetchArchive(t *testing.T, dir string) string {
	t.Helper()
	return buildArchiveFile(t, dir, "map.pmtiles", fetchArchiveCredit)
}

func buildArchiveFile(t *testing.T, dir, name, credit string) string {
	t.Helper()
	tile, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "earth",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
				Exterior: mvt.Ring{{X: 0, Y: 0}, {X: 4096, Y: 0}, {X: 4096, Y: 4096}, {X: 0, Y: 4096}},
			}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}

	// Every tile covering the fixture ground from zoom 0 to the cell zoom,
	// which is the overview chain plus the cells themselves. Deeper zooms are
	// deliberately absent: the fetch is run with --max-zoom 12 so the plan
	// asks for exactly this much.
	var tiles []osmbasetest.ArchiveTile
	seen := map[uint64]bool{}
	for z := uint8(0); z <= slice.DefaultCellZoom; z++ {
		for _, corner := range [][2]float64{
			{fetchLat - 0.05, fetchLon - 0.05}, {fetchLat + 0.05, fetchLon + 0.05},
		} {
			x, y, err := mercator.TileAt(z, corner[1], corner[0])
			if err != nil {
				t.Fatalf("locating tile at zoom %d: %v", z, err)
			}
			id, err := pmtiles.ZxyToID(z, x, y)
			if err != nil {
				t.Fatalf("tile id for %d/%d/%d: %v", z, x, y, err)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			tiles = append(tiles, osmbasetest.ArchiveTile{ID: id, Data: tile})
		}
	}

	meta := []byte(`{}`)
	if credit != "" {
		b, err := json.Marshal(map[string]string{"attribution": credit})
		if err != nil {
			t.Fatalf("encoding the fixture metadata: %v", err)
		}
		meta = b
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles: tiles, Metadata: meta,
		TileType: pmtiles.TileTypeMVT,
		MinZoom:  0, MaxZoom: slice.DefaultCellZoom,
		MinLon: fetchLon - 0.1, MinLat: fetchLat - 0.1,
		MaxLon: fetchLon + 0.1, MaxLat: fetchLat + 0.1,
	})
	if err != nil {
		t.Fatalf("building the fixture archive: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatalf("writing the fixture archive: %v", err)
	}
	return path
}

// redirectUserCache points the user cache directory at a temporary one for the
// duration of a test.
//
// It is a guard against a whole class of accident rather than a tidy-up. The
// store directory falls back to slice.DefaultRoot when --store is empty, and
// DefaultRoot resolves through os.UserCacheDir -- so any test that reaches the
// fetch path without a store writes REAL tiles into the developer's own cache,
// under a source named after a temp archive that will not exist a second
// later. That happened: a run left a second archive in
// ~/Library/Caches/osmbase whose source was a t.TempDir path, and because a
// store holding two archives is refused, every subsequent "osmbase render"
// against the real cache failed with a message about a file in /var/folders.
//
// The test that caused it passed --store correctly; it was reached in a
// transient state while other edits were in flight. Which is the argument for
// doing this here rather than auditing call sites: an assertion that every
// invocation names a store can be true today and false after the next edit,
// while a redirected HOME cannot leak whatever the code under test decides to
// do.
//
// os.UserCacheDir reads XDG_CACHE_HOME on Unix and HOME on macOS, so both are
// set. t.Setenv restores them and refuses to run under t.Parallel, which is
// the behaviour wanted: a process-wide environment change must not overlap
// with another test.
func redirectUserCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
}

// TestActivityBounds_ReadsEveryFIXRatherThanTheThinnedOutline is the
// difference between the area a route occupies and the area its DRAWING
// occupies, and they are not the same area.
//
// route.FromTrack exists to thin a track down to what a few hundred pixels
// can show, and it thins by uniform stride: whether a fix survives depends on
// its index and nothing else. A detour that reaches two kilometres further
// east than anything around it is one fix, it sits at an arbitrary index, and
// the outline drops it without visible harm -- at 500 points across a screen
// it was never going to be its own pixel.
//
// A fetch reading that thinned list would fetch a box that stops short of
// where the activity actually went, and the failure surfaces much later and
// somewhere else: a render whose route runs off the edge of the map at one
// end, with a fetch that reported success. So this asks for every sample, and
// the test is built so the outline demonstrably does not contain the extreme
// it is checking for.
//
// The dropped index is computed from FromTrack itself rather than assumed.
// The stride formula is not this package's to know, and an index picked by
// hand would stop being a dropped one -- silently, leaving a passing test
// that proves nothing -- if it ever changed.
func TestActivityBounds_ReadsEveryFixRatherThanTheThinnedOutline(t *testing.T) {
	start := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	const count = 3000
	track := &fitactivity.Track{}
	for i := range count {
		track.Samples = append(track.Samples, fitactivity.Sample{
			Time:   start.Add(time.Duration(i) * time.Second),
			HasGPS: true,
			Lat:    fetchLat + float64(i)*0.00001,
			Lon:    fetchLon + float64(i)*0.00001,
		})
	}

	// Which sample indices the outline keeps, identified by timestamp because
	// each sample has its own and thinning carries it through.
	kept := map[time.Time]bool{}
	for _, p := range route.FromTrack(track, route.DefaultMaxPoints) {
		kept[p.Time] = true
	}
	detour := -1
	for i, s := range track.Samples {
		if !kept[s.Time] {
			detour = i
			break
		}
	}
	if detour < 0 {
		t.Fatalf("the outline kept all %d fixes, so nothing here is dropped and this test proves nothing", count)
	}

	// The one fix that reached furthest east, at an index the outline drops.
	east := fetchLon + 0.05
	track.Samples[detour].Lon = east

	if outline := route.FromTrack(track, route.DefaultMaxPoints); slices.ContainsFunc(outline, func(p route.Point) bool { return p.Lon == east }) {
		t.Fatal("precondition: the outline kept the detour after all, so a box drawn from it would not fall short")
	}

	b, err := activityBounds(track, 0)
	if err != nil {
		t.Fatalf("activityBounds: %v", err)
	}
	if b.East < east {
		t.Errorf("the area stops at %.5f, short of the fix at %.5f: the map would end before the route does", b.East, east)
	}
}

// TestActivityBounds_AnActivityThatNeverMovedIsRefusedOrPaddedIntoAnArea
// covers the activity with no width: every fix at the same point, which is a
// watch that locked once and then sat on a bench.
//
// Both halves are the same decision seen from either side of --pad, and
// neither is allowed to be an accident. Without a pad there is no area to
// fetch, and the refusal has to happen HERE, where the message can say what
// to type: the same box handed down to the planner comes back as an error
// about zoom or extent from three layers below, describing a shape rather
// than the activity that produced it. With a pad there is an area -- the
// ground around the point -- and refusing it would be wrong, since somebody
// fetching a map around a fixed position is doing something sensible.
//
// The padded size is measured rather than merely checked as non-empty, since
// "widened by something" and "widened by the amount asked for" are different
// claims and only the second is useful.
func TestActivityBounds_AnActivityThatNeverMovedIsRefusedOrPaddedIntoAnArea(t *testing.T) {
	start := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	track := &fitactivity.Track{}
	for i := range 10 {
		track.Samples = append(track.Samples, fitactivity.Sample{
			Time:   start.Add(time.Duration(i) * time.Second),
			HasGPS: true,
			Lat:    fetchLat,
			Lon:    fetchLon,
		})
	}

	t.Run("without a pad it is refused, saying what to type", func(t *testing.T) {
		_, err := activityBounds(track, 0)
		if err == nil {
			t.Fatal("an activity whose every fix is at one point produced an area")
		}
		if !strings.Contains(err.Error(), "--pad") {
			t.Errorf("the refusal does not name the flag that fixes it: %v", err)
		}
	})

	t.Run("with a pad it becomes the ground around the point", func(t *testing.T) {
		const padKM float64 = 2
		b, err := activityBounds(track, padKM)
		if err != nil {
			t.Fatalf("activityBounds: %v", err)
		}
		w, h := acquire.ExtentKM(b)
		for _, c := range []struct {
			axis string
			got  float64
		}{{"width", w}, {"height", h}} {
			if want := 2 * padKM; math.Abs(c.got-want) > 0.01*want {
				t.Errorf("%s is %.3f km, want %.3f km -- twice the %g km pad on either side of the point", c.axis, c.got, want, padKM)
			}
		}
	})
}

// TestRunBasemapFetch_NamesWhoTheMapDataIsOwedTo is the attribution
// obligation at the moment it is taken on.
//
// Everything OpenStreetMap ships is free to use on one condition: say where
// it came from. Until now this command read the archive's credit -- early,
// so an uncreditable archive is refused before the download rather than
// after -- wrote it into the store's manifest, and never showed it to the
// person who ran it. That is enough to keep a later render honest, and it
// leaves the user to discover whose data they downloaded by rendering a video
// and reading the corner of it.
//
// Two things are asserted, and the second is the one with teeth. The credit
// has to reach the output, AND it has to arrive as the text a person reads
// rather than the markup the archive wrote: Protomaps writes its attribution
// as HTML because in a browser the credit is a link, and an anchor tag
// printed into a terminal report discharges nothing. The fixture's credit is
// deliberately HTML for exactly that reason.
func TestRunBasemapFetch_NamesWhoTheMapDataIsOwedTo(t *testing.T) {
	dir := t.TempDir()
	fit := writeFetchActivity(t, dir)
	archive := buildFetchArchive(t, dir)
	store := filepath.Join(dir, "store")

	var out, errw strings.Builder
	if err := execFetchTo(t, &out, &errw, nil,
		"--source", archive, "--store", store, "--max-zoom", "12", fit); err != nil {
		t.Fatalf("basemap fetch: %v", err)
	}
	var rep fetchReport
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("decoding the report %q: %v", out.String(), err)
	}

	const want = "© OpenStreetMap contributors"
	if rep.Credit != want {
		t.Errorf("the report credits %q, want %q", rep.Credit, want)
	}
	if !strings.Contains(errw.String(), want) {
		t.Errorf("the fetch output never says whose data was downloaded; got:\n%s", errw.String())
	}
	for _, markup := range []string{"<a ", "&copy;", "href="} {
		if strings.Contains(rep.Credit, markup) || strings.Contains(errw.String(), markup) {
			t.Errorf("the credit reaches the user as markup (%q), which credits nobody a person can read", markup)
		}
	}
}

// TestOfferToFillTheStore_AsksBeforeAnythingIsReadAndFetchesOnYes is the
// end-to-end of the offer, over a LOCAL archive so the test reaches no
// network -- which is also the flag's own reason for existing.
//
// The three answers are separate cases rather than one loop because they are
// three different promises. No means nothing is written. Silence -- a
// pipeline, a closed stdin -- must behave as no rather than as consent, which
// is the same rule the fetch command's own prompt follows and the one that
// matters most here, since a render is a thing people run from scripts. Yes
// means the store ends up holding the area, which is the only one of the
// three that proves the offer is wired to a real fetch rather than to a
// message.
func TestOfferToFillTheStore_AsksBeforeAnythingIsReadAndFetchesOnYes(t *testing.T) {
	for _, c := range []struct {
		name    string
		answer  string
		yesFlag bool
		filled  bool
	}{
		{"no leaves the store alone", "n\n", false, false},
		{"silence is not consent", "", false, false},
		{"yes fills it", "y\n", false, true},
		{"--yes skips the question", "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func(o basemapFetchOptions) { basemapFetchOpts = o }(basemapFetchOpts)
			dir := t.TempDir()
			fit := writeFetchActivity(t, dir)
			store := filepath.Join(dir, "store")
			basemapFetchOpts = basemapFetchOptions{
				source: buildFetchArchive(t, dir),
				yes:    c.yesFlag,
				// The render never registers the fetch command's flags, so
				// this stands in for the max-zoom default a real run gets.
				maxZoom: acquire.AutoZoom,
			}

			track, err := fitactivity.Decode(fit)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			var errw strings.Builder
			cmd := &cobra.Command{}
			cmd.SetErr(&errw)
			cmd.SetIn(strings.NewReader(c.answer))

			offerToFillTheStore(cmd, store, track)

			out := errw.String()
			// Whatever the answer, the question has to have named the host
			// and the area before a socket could have been opened. A prompt
			// that said only "fetch?" would be asking for consent to an
			// unstated thing.
			for _, want := range []string{"km", "holds no map data"} {
				if !strings.Contains(out, want) {
					t.Errorf("the offer never says %q:\n%s", want, out)
				}
			}

			_, err = os.Stat(store)
			switch {
			case c.filled && err != nil:
				t.Fatalf("after %q the store was not created: %v\n%s", c.name, err, out)
			case !c.filled && err == nil:
				t.Fatalf("after %q the store exists; declining must write nothing\n%s", c.name, out)
			case !c.filled:
				return
			}

			// Filled means filled for THIS activity: a store that exists but
			// holds the wrong ground would pass a mere existence check and
			// still draw nothing.
			area, err := activityBounds(track, autoFetchPadKM)
			if err != nil {
				t.Fatalf("activityBounds: %v", err)
			}
			short, err := tilemap.StoreShortfall(store, area)
			if err != nil {
				t.Fatalf("StoreShortfall: %v", err)
			}
			if !short.Complete() {
				t.Errorf("after fetching, the store still holds %d of %d areas (empty=%v)\n%s",
					short.Held, short.Cells, short.Empty, out)
			}
		})
	}
}

// TestOfferToFillTheStore_SaysNothingWhenTheStoreAlreadyHasIt keeps the offer
// out of the way of the runs it has nothing to do with.
//
// A render whose store already covers the route must not print a privacy
// notice, ask a question, or open a socket. A prompt that appeared on every
// run would be one people learn to answer without reading, which is the exact
// failure mode the rest of this design is trying to avoid.
func TestOfferToFillTheStore_SaysNothingWhenTheStoreAlreadyHasIt(t *testing.T) {
	defer func(o basemapFetchOptions) { basemapFetchOpts = o }(basemapFetchOpts)
	dir := t.TempDir()
	fit := writeFetchActivity(t, dir)
	store := filepath.Join(dir, "store")
	basemapFetchOpts = basemapFetchOptions{source: buildFetchArchive(t, dir), yes: true, maxZoom: acquire.AutoZoom}

	track, err := fitactivity.Decode(fit)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	first := &cobra.Command{}
	first.SetErr(io.Discard)
	first.SetIn(strings.NewReader(""))
	offerToFillTheStore(first, store, track)

	// Second run over the now-filled store: the source is pointed at a path
	// that does not exist, so any attempt to read an archive fails loudly
	// rather than passing by luck.
	basemapFetchOpts.source = filepath.Join(dir, "not-there.pmtiles")
	var errw strings.Builder
	second := &cobra.Command{}
	second.SetErr(&errw)
	second.SetIn(strings.NewReader(""))
	offerToFillTheStore(second, store, track)

	if out := errw.String(); out != "" {
		t.Errorf("a render over a store that already covers the route said:\n%s", out)
	}
}

// TestOfferToFillTheStore_DoesNotBlockOnAPipeNobodyWillWriteTo is a bug this
// found by being run rather than by being reasoned about.
//
// The prompt read stdin and treated end-of-input as no, which is right for a
// terminal and for a closed stream. It is wrong for the case in between: a
// render started from a script or a job runner inherits a pipe that may stay
// open for the life of the parent and never deliver anything. The read blocks
// there forever, and the symptom is a render that produces no output and no
// error -- the hardest kind of failure to diagnose, because there is nothing
// to look at.
//
// So the question is only asked when something could answer it. The fixture
// is a real os.Pipe rather than a stub, because the distinction being tested
// is one only a real file handle has: a strings.Reader ends, and would pass
// this test without the fix.
func TestOfferToFillTheStore_DoesNotBlockOnAPipeNobodyWillWriteTo(t *testing.T) {
	defer func(o basemapFetchOptions) { basemapFetchOpts = o }(basemapFetchOpts)
	dir := t.TempDir()
	fit := writeFetchActivity(t, dir)
	store := filepath.Join(dir, "store")
	basemapFetchOpts = basemapFetchOptions{source: buildFetchArchive(t, dir), maxZoom: acquire.AutoZoom}

	track, err := fitactivity.Decode(fit)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	// Deliberately left open for the whole test: that IS the condition.
	defer w.Close()
	defer r.Close()

	var errw strings.Builder
	cmd := &cobra.Command{}
	cmd.SetErr(&errw)
	cmd.SetIn(r)

	done := make(chan struct{})
	go func() {
		defer close(done)
		offerToFillTheStore(cmd, store, track)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer is still waiting for an answer from a pipe nobody will write to; a render started from a script would never finish")
	}

	out := errw.String()
	if !strings.Contains(out, "--yes") {
		t.Errorf("declining for want of an answer does not say how to say yes in advance:\n%s", out)
	}
	if _, err := os.Stat(store); err == nil {
		t.Errorf("a store was written without anybody consenting to it\n%s", out)
	}
}

// TestOfferToFillTheStore_WarnsThatAStretchedMapStillReportsAsCovered closes
// a gap between two true statements that contradict each other on screen.
//
// A store that lacks an area at depth but holds any shallower tile over it
// does not fail. The renderer fills the view by stretching what it has and
// reports FULL coverage, because coverage counts tiles drawn and not the
// detail in them -- so the user reads "100% covered" under a coloured smear.
// Seen on a real store: zero of one areas held, one of twelve shallow tiles
// held, and a render that announced full coverage.
//
// The assertion is an either/or rather than a skip, so the test says
// something whichever way the fixture falls: the sentence appears exactly
// when there is a stretched render to warn about.
func TestOfferToFillTheStore_WarnsThatAStretchedMapStillReportsAsCovered(t *testing.T) {
	defer func(o basemapFetchOptions) { basemapFetchOpts = o }(basemapFetchOpts)
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	basemapFetchOpts = basemapFetchOptions{source: buildFetchArchive(t, dir), yes: true, maxZoom: acquire.AutoZoom}

	// Seed the store from one activity, then ask about ground a long way from
	// it: non-empty, incomplete for the area asked about, which is the only
	// arrangement in which the warning is either right or wrong.
	track, err := fitactivity.Decode(writeFetchActivity(t, dir))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	seed := &cobra.Command{}
	seed.SetErr(io.Discard)
	seed.SetIn(strings.NewReader(""))
	offerToFillTheStore(seed, store, track)

	far := shiftTrack(track, -30)
	area, err := activityBounds(far, autoFetchPadKM)
	if err != nil {
		t.Fatalf("activityBounds: %v", err)
	}
	short, err := tilemap.StoreShortfall(store, area)
	if err != nil {
		t.Fatalf("StoreShortfall: %v", err)
	}
	if short.Complete() || short.Empty {
		t.Fatalf("precondition: the far area reports %+v; this test needs a store that is neither empty nor complete for it", short)
	}

	basemapFetchOpts.yes = false
	var errw strings.Builder
	cmd := &cobra.Command{}
	cmd.SetErr(&errw)
	cmd.SetIn(strings.NewReader("n\n"))
	offerToFillTheStore(cmd, store, far)

	const warning = "stretching shallower tiles"
	said := strings.Contains(errw.String(), warning)
	if want := short.CanFallBack(); said != want {
		t.Errorf("the offer %s say %q, but the store %s a shallower tile to stretch (%+v):\n%s",
			map[bool]string{true: "does", false: "does not"}[said], warning,
			map[bool]string{true: "has", false: "has no"}[want], short, errw.String())
	}
}

// shiftTrack moves every fix west by degrees, for a fixture that needs ground
// a store was not filled for. Longitude only: a shift in latitude would cross
// into a different band of the projection and change the cell arithmetic
// being tested.
func shiftTrack(track *fitactivity.Track, degrees float64) *fitactivity.Track {
	out := &fitactivity.Track{Samples: append([]fitactivity.Sample(nil), track.Samples...)}
	for i := range out.Samples {
		if out.Samples[i].HasGPS {
			out.Samples[i].Lon += degrees
		}
	}
	return out
}
