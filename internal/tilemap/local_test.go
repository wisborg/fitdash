package tilemap

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// The synthetic ground these tests draw. Nowhere in particular and
// deliberately so: nothing derived from a real recording -- not a
// coordinate, not a date -- belongs in a committed test.
const (
	fixtureLat = 12.345678
	fixtureLon = 98.765432
)

// fixtureCredit is what the fixture store's manifest records, and every
// attribution assertion below reads it from there rather than from a
// constant in the code under test. That is the property: the credit is data.
const fixtureCredit = "Map data © OpenStreetMap contributors (synthetic fixture)"

// oneTileArchive answers every tile coordinate with the same vector tile, so
// a fill writes a real, decodable tile wherever it is pointed.
type oneTileArchive struct{ data []byte }

func (a oneTileArchive) RawTile(uint8, uint32, uint32) ([]byte, bool, error) {
	return a.data, true, nil
}

// localFixtureStore builds a store on disk holding map data for the cells
// around lat,lon, with credit in its manifest.
//
// Only the cell zoom is filled, not the pyramid beneath it: a store whose
// deepest tile is shallower than the view asks for is the ordinary case --
// public builds stop at zoom 15 and a route panel asks for more -- and the
// renderer answers it by drawing the ancestor overzoomed. Filling one zoom
// keeps the fixture to a handful of files and exercises that path rather
// than avoiding it.
func localFixtureStore(t *testing.T, lat, lon float64, credit string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")

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

	store, err := slice.Create(root, slice.Config{})
	if err != nil {
		t.Fatalf("creating the fixture store: %v", err)
	}
	src, err := store.AddSource(slice.SourceDesc{
		Source:          "synthetic.pmtiles",
		Build:           "fixture",
		Schema:          "protomaps/basemap v4",
		Attribution:     credit,
		TileType:        "mvt",
		TileCompression: slice.CompressionNone,
		SourceZoom:      slice.ZoomRange{Min: 0, Max: 15},
	})
	if err != nil {
		t.Fatalf("adding the fixture source: %v", err)
	}
	cellZoom := store.CellZoom()
	cells, err := store.CellsFor(slice.Bounds{
		West: lon - 0.01, South: lat - 0.01, East: lon + 0.01, North: lat + 0.01,
	})
	if err != nil {
		t.Fatalf("finding the fixture cells: %v", err)
	}
	for _, c := range cells {
		if _, err := src.Fill(context.Background(), oneTileArchive{tile}, c,
			slice.ZoomRange{Min: cellZoom, Max: cellZoom}); err != nil {
			t.Fatalf("filling cell %s: %v", c, err)
		}
	}
	return root
}

// darkInks is a consumer palette to derive a map from: fitdash's own dark
// theme, spelled out here so this package's tests do not import the panel
// layer that imports it.
func darkInks() MapInks {
	return MapInks{
		Background: color.RGBA{R: 0x0E, G: 0x0E, B: 0x10, A: 0xFF},
		Foreground: color.RGBA{R: 0xF2, G: 0xF2, B: 0xF4, A: 0xFF},
		Dim:        color.RGBA{R: 0x6E, G: 0x6E, B: 0x78, A: 0xFF},
		Accent:     color.RGBA{R: 0xFF, G: 0x5A, B: 0x36, A: 0xFF},
		Highlight:  color.RGBA{R: 0x9B, G: 0x6B, B: 0xFF, A: 0xFF},
	}
}

// fixtureView is a view of the fixture ground, at the size and shape a route
// panel would ask for.
func fixtureView(lat, lon float64) View {
	return View{
		North: lat + 0.004, South: lat - 0.004,
		West: lon - 0.006, East: lon + 0.006,
		Width: 480, Height: 320,
	}
}

// TestLocal_FetchedIsFalseSoNoRunIsReportedAsHavingSentAnything is the
// privacy half of the adapter, and it is checked through the interface the
// summary actually branches on rather than by calling the method directly: a
// provider that did not implement Reporter at all is read as having gone to
// the network, so "does not implement it" and "implements it returning true"
// are the same defect here and both have to fail.
func TestLocal_FetchedIsFalseSoNoRunIsReportedAsHavingSentAnything(t *testing.T) {
	root := localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit)
	p, err := OpenLocal(root, darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}

	var provider Provider = p
	r, ok := provider.(Reporter)
	if !ok {
		t.Fatal("*Local does not implement Reporter, so a summary would report a local render as having sent the route to a third party")
	}
	if r.Fetched() {
		t.Error("Fetched() is true for a provider that reads tiles off the user's own disk")
	}

	// Still false after drawing, which is the case that matters: Fetched is
	// a question about what a run DID, and a provider that answered it from
	// its type rather than its behaviour would be right for the wrong reason.
	if _, err := p.Image(context.Background(), fixtureView(fixtureLat, fixtureLon)); err != nil {
		t.Fatalf("Image: %v", err)
	}
	if r.Fetched() {
		t.Error("Fetched() became true after a draw; nothing was fetched")
	}
}

// TestLocal_DrawsTheGroundItWasAskedAboutAndRefusesGroundItHasNone is the
// test of the view conversion, and it is aimed at the transposition it is
// there to prevent: latitude and longitude, or north and south, swapped on
// the way into osmbase. Both mistakes leave a provider that draws a
// perfectly good image of the wrong place, which no assertion about image
// size would catch -- so the assertion is about COVERAGE, which is a
// question about which tiles exist. The fixture holds tiles around
// 12.345678,98.765432 and nowhere else, so the transposed view
// (98.765432,12.345678) lands on ground the store has nothing for.
func TestLocal_DrawsTheGroundItWasAskedAboutAndRefusesGroundItHasNone(t *testing.T) {
	root := localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit)
	p, err := OpenLocal(root, darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}

	img, err := p.Image(context.Background(), fixtureView(fixtureLat, fixtureLon))
	if err != nil {
		t.Fatalf("Image over the fixture's own ground: %v", err)
	}
	if got := img.Bounds().Dx(); got != 480 {
		t.Errorf("image is %d wide, want the 480 the view asked for", got)
	}
	if got := img.Bounds().Dy(); got != 320 {
		t.Errorf("image is %d high, want the 320 the view asked for", got)
	}
	if got := p.Coverage(); got != 1 {
		t.Errorf("Coverage() = %v over ground the store holds, want 1", got)
	}

	// Latitude and longitude swapped: a real place, and not this one.
	_, err = p.Image(context.Background(), fixtureView(fixtureLon-85, fixtureLat))
	if err == nil {
		t.Fatal("Image drew a view of ground the store holds no tiles for")
	}
	if !errors.Is(err, osm.ErrNoCoverage) {
		t.Errorf("error is %v, want one wrapping ErrNoCoverage so the caller can tell it from a damaged store", err)
	}
}

// TestLocal_AttributionIsReadFromTheStoreRatherThanWrittenDownHere pins the
// obligation to the DATA. A credit compiled into fitdash would go on being
// drawn into every frame after somebody filled their store from a different
// source, which is the one way this can be wrong and still look right.
func TestLocal_AttributionIsReadFromTheStoreRatherThanWrittenDownHere(t *testing.T) {
	const other = "Map data © Somebody Else, under some other licence"
	root := localFixtureStore(t, fixtureLat, fixtureLon, other)
	p, err := OpenLocal(root, darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if got := p.Attribution(); got != other {
		t.Errorf("Attribution() = %q, want the store's own %q", got, other)
	}
}

// TestOpenLocal_RefusesAStoreThatNamesNobodyToCredit is the case that must
// not degrade quietly: a rendered map is a Produced Work under the ODbL, so
// a store recording no credit leaves fitdash with an obligation it cannot
// discharge. Drawing anyway with a blank credit, or with an invented one,
// are the two wrong answers.
func TestOpenLocal_RefusesAStoreThatNamesNobodyToCredit(t *testing.T) {
	root := localFixtureStore(t, fixtureLat, fixtureLon, "")
	_, err := OpenLocal(root, darkInks(), nil)
	if err == nil {
		t.Fatal("a store with no attribution was accepted")
	}
	if !strings.Contains(err.Error(), "attribution") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestOpenLocal_SaysSoWhenThereIsNoStoreOrNoDataInIt pins the two ways a
// user arrives here with nothing to draw from. Both are refusals at resolve
// time rather than a silent fall back to no basemap, because both are a
// typed path or an un-run fetch -- things the user can fix, and will not
// think to if the render simply comes out plain.
func TestOpenLocal_SaysSoWhenThereIsNoStoreOrNoDataInIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")
	if _, err := OpenLocal(missing, darkInks(), nil); err == nil {
		t.Error("a store directory that does not exist was accepted")
	} else if !strings.Contains(err.Error(), missing) {
		t.Errorf("error does not name the directory: %v", err)
	}

	empty := filepath.Join(t.TempDir(), "store")
	if _, err := slice.Create(empty, slice.Config{}); err != nil {
		t.Fatalf("creating an empty store: %v", err)
	}
	_, err := OpenLocal(empty, darkInks(), nil)
	if err == nil {
		t.Fatal("a store holding no map data was accepted")
	}
	if !strings.Contains(err.Error(), "holds no map data") {
		t.Errorf("error does not say the store is empty: %v", err)
	}
}

// TestLocal_NameIdentifiesTheSourceItDrawsFrom checks the identity a cache
// would key on if anyone ever wrapped this -- nothing should, and the point
// of the assertion is that the name distinguishes two stores anyway rather
// than collapsing every local render onto one key.
func TestLocal_NameIdentifiesTheSourceItDrawsFrom(t *testing.T) {
	p, err := OpenLocal(localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit), darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if !strings.HasPrefix(p.Name(), LocalProvider+"/") {
		t.Errorf("Name() = %q, want it to start with %q", p.Name(), LocalProvider+"/")
	}
	if p.Name() == LocalProvider+"/" {
		t.Error("Name() carries no source identity, so two different slices would share one name")
	}
}

// TestLocal_AnHTMLAttributionBecomesTextTheFrameCanCarry is the same
// obligation as the test above, at the point it is actually discharged.
// Protomaps writes the credit as HTML because its archives were authored for
// web maps, and the panel that draws Attribution() into a frame has no
// browser, no link and no way to say so -- so a raw manifest string is burnt
// into every frame as markup, and the ODbL credit for the rendered video
// reads as an anchor tag.
//
// Asserted through OpenLocal rather than over render.PlainCredit alone because the
// conversion has to happen at the point the manifest is read: that is what
// makes the string in the frame, the string in the summary and the string in
// this accessor one string.
func TestLocal_AnHTMLAttributionBecomesTextTheFrameCanCarry(t *testing.T) {
	const markup = `<a href="https://example.test/copyright" target="_blank">&copy; OpenStreetMap contributors</a>`
	const want = "© OpenStreetMap contributors"

	p, err := OpenLocal(localFixtureStore(t, fixtureLat, fixtureLon, markup), darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if got := p.Attribution(); got != want {
		t.Errorf("Attribution() = %q, want %q", got, want)
	}
	if strings.ContainsAny(p.Attribution(), "<>") {
		t.Errorf("Attribution() still carries markup: %q", p.Attribution())
	}
}

// oneCellFixtureStore builds a store holding map data for exactly ONE cell,
// and returns the root together with that cell's own bounds.
//
// Exactly one, rather than the handful localFixtureStore writes, because the
// property under test is what happens at the EDGE of what a store holds, and
// an edge is only reachable if the test knows where it is. The cell's bounds
// come back from the store's own geometry rather than being written down
// here: a cell is a tile at the store's cell zoom, so its extent depends on a
// zoom this test does not choose and should not assume.
func oneCellFixtureStore(t *testing.T, lat, lon float64) (root string, cell slice.Bounds) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "store")

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
	store, err := slice.Create(root, slice.Config{})
	if err != nil {
		t.Fatalf("creating the fixture store: %v", err)
	}
	src, err := store.AddSource(slice.SourceDesc{
		Source:          "synthetic.pmtiles",
		Build:           "fixture",
		Schema:          "protomaps/basemap v4",
		Attribution:     fixtureCredit,
		TileType:        "mvt",
		TileCompression: slice.CompressionNone,
		SourceZoom:      slice.ZoomRange{Min: 0, Max: 15},
	})
	if err != nil {
		t.Fatalf("adding the fixture source: %v", err)
	}
	cellZoom := store.CellZoom()
	cells, err := store.CellsFor(slice.Bounds{West: lon, South: lat, East: lon, North: lat})
	if err != nil {
		t.Fatalf("finding the fixture cell: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("a degenerate bounds covers %d cells, want exactly 1", len(cells))
	}
	if _, err := src.Fill(context.Background(), oneTileArchive{tile}, cells[0],
		slice.ZoomRange{Min: cellZoom, Max: cellZoom}); err != nil {
		t.Fatalf("filling cell %s: %v", cells[0], err)
	}
	w, s, e, n, err := mercator.TileBounds(cellZoom, cells[0].X, cells[0].Y)
	if err != nil {
		t.Fatalf("TileBounds for cell %s: %v", cells[0], err)
	}
	return root, slice.Bounds{West: w, South: s, East: e, North: n}
}

// TestLocal_CoverageKeepsTheWORSTViewDrawnRatherThanTheLatestOrTheBest is
// about a number the user reads and cannot check.
//
// The summary prints one coverage figure for a whole render, and the render
// draws hundreds of views: the route panel asks for a new one as the map
// pans. Those views differ -- the middle of a route can be fully held while
// its first kilometre runs off the edge of what was fetched -- so a single
// figure has to choose, and the only honest choice is the worst. "94% of the
// map area is in the store" printed after a render whose opening frames were
// three-quarters hatched is a sentence that tells a user their store was
// nearly complete while they are looking at the hole.
//
// Both orders are asserted, and that is the point rather than thoroughness
// for its own sake. A single-order test cannot tell "keeps the minimum" from
// "keeps the first" or "keeps the last": drawing worst-then-best passes under
// min AND under first, drawing best-then-worst passes under min AND under
// last. Only the pair excludes everything but the minimum.
func TestLocal_CoverageKeepsTheWORSTViewDrawnRatherThanTheLatestOrTheBest(t *testing.T) {
	root, cell := oneCellFixtureStore(t, fixtureLat, fixtureLon)

	// Wholly inside the one cell the store holds: every tile this needs is on
	// disk, so it is the BEST view available from this fixture.
	inset := (cell.East - cell.West) / 4
	held := View{
		West: cell.West + inset, East: cell.East - inset,
		South: cell.South + inset, North: cell.North - inset,
		Width: 480, Height: 320,
	}
	// Shifted east so that half of it lies over the cell the store holds and
	// half over the neighbour it does not. Partly held, so it draws -- with
	// the missing half hatched -- rather than failing.
	straddling := View{
		West: cell.West + 2*inset, East: cell.East + 2*inset,
		South: cell.South + inset, North: cell.North - inset,
		Width: 480, Height: 320,
	}

	for _, c := range []struct {
		name  string
		views []View
	}{
		{"the worst view drawn first", []View{straddling, held}},
		{"the worst view drawn last", []View{held, straddling}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := OpenLocal(root, darkInks(), nil)
			if err != nil {
				t.Fatalf("OpenLocal: %v", err)
			}
			var worst float64 = 1
			for i, v := range c.views {
				if _, err := p.Image(context.Background(), v); err != nil {
					t.Fatalf("Image %d: %v", i, err)
				}
				if got := p.Coverage(); got < worst {
					worst = got
				}
			}
			if worst >= 1 {
				t.Fatal("precondition: neither view was partially covered, so this test proves nothing about which one is kept")
			}
			if got := p.Coverage(); got != worst {
				t.Errorf("Coverage() = %.3f after drawing both views, want the worst of them, %.3f", got, worst)
			}
		})
	}
}

// TestOpenLocal_RefusesAStoreWhoseCreditIsNothingButWhitespace is the ODbL
// refusal at the only input that can tell the two implementations of it
// apart.
//
// The refusal must be judged on the credit a FRAME can carry, not on the
// bytes the manifest happens to hold, and the existing empty-string test
// cannot distinguish those: "" is empty either way. A manifest whose
// attribution is a tab and a newline is not empty as a string, converts to
// nothing a viewer could read, and is exactly what a store built by a script
// that filled the field from an absent value looks like. Checking the raw
// string accepts it and draws a map crediting nobody -- an obligation
// breached in the one place nothing in the output would show it.
//
// Confirmed by mutation: refusing on m.Attribution == "" rather than on the
// converted credit passed the whole suite before this existed.
func TestOpenLocal_RefusesAStoreWhoseCreditIsNothingButWhitespace(t *testing.T) {
	const blank = " \t\n  "
	root := localFixtureStore(t, fixtureLat, fixtureLon, blank)
	_, err := OpenLocal(root, darkInks(), nil)
	if err == nil {
		t.Fatal("a store whose attribution is only whitespace was accepted; the render would credit nobody")
	}
	if !strings.Contains(err.Error(), "attribution") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// twoSourceStore builds a store holding map data from TWO sources and returns
// its root along with the ID of the one written most recently.
//
// The timestamps are rewritten on disk rather than taken from the clock,
// because what is under test is a comparison and a clock cannot be asked for
// two readings a known distance apart. Rewriting the manifest is fair game
// here: it is a documented JSON file that this package already reads back
// through the same accessor.
//
// newestSortsFirst chooses WHICH of the two is made the newer one. A source's
// ID is a hash of its name, so the caller cannot pick the order directly; the
// arrangement is made by looking at the IDs after the fact and assigning the
// later timestamp to the end the caller asked for.
func twoSourceStore(t *testing.T, newestSortsFirst bool) (root, newest string) {
	t.Helper()
	root = localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit)

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
	store, err := slice.Open(root)
	if err != nil {
		t.Fatalf("opening the fixture store: %v", err)
	}
	second, err := store.AddSource(slice.SourceDesc{
		Source:          "second.pmtiles",
		Build:           "fixture",
		Schema:          "protomaps/basemap v4",
		Attribution:     fixtureCredit,
		TileType:        "mvt",
		TileCompression: slice.CompressionNone,
		SourceZoom:      slice.ZoomRange{Min: 0, Max: 15},
	})
	if err != nil {
		t.Fatalf("adding the second source: %v", err)
	}
	cells, err := store.CellsFor(slice.Bounds{
		West: fixtureLon - 0.01, South: fixtureLat - 0.01,
		East: fixtureLon + 0.01, North: fixtureLat + 0.01,
	})
	if err != nil {
		t.Fatalf("finding the fixture cells: %v", err)
	}
	for _, c := range cells {
		z := slice.ZoomRange{Min: store.CellZoom(), Max: store.CellZoom()}
		if _, err := second.Fill(context.Background(), oneTileArchive{tile}, c, z); err != nil {
			t.Fatalf("filling cell %s of the second source: %v", c, err)
		}
	}

	sources, err := store.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("store holds %d sources, want 2", len(sources))
	}
	// Sources comes back sorted by ID, so sources[0] is the end that sorts
	// first and sources[1] the end that sorts last.
	older, newer := sources[1], sources[0]
	if !newestSortsFirst {
		older, newer = sources[0], sources[1]
	}
	base := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	setUpdated(t, root, older.ID, base)
	setUpdated(t, root, newer.ID, base.Add(time.Hour))
	return root, newer.ID
}

// setUpdated rewrites one source manifest's updated timestamp in place,
// leaving every other field as it was written.
func setUpdated(t *testing.T, root, id string, at time.Time) {
	t.Helper()
	path := filepath.Join(root, id, "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	m["updated"] = at.Format(time.RFC3339Nano)
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatalf("encoding %s: %v", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestNewestSource_PicksTheNewestWhicheverWayTheIDsSort is about drawing
// last week's map over this week's route.
//
// A store can hold more than one source -- a second fetch from a different
// build writes a second one beside the first -- and the render has to choose.
// Choosing the oldest is not a crash and not a blank frame: it is a complete,
// convincing map of the right place, built from data that has since been
// replaced, and nothing on screen says so.
//
// Both arrangements are asserted because a source's ID is a hash and the
// comparison is not the only thing that could decide the answer. With the
// newest sorting first, "picks the oldest" and "picks whichever sorts last"
// both fail; with it sorting last, "picks the oldest" and "picks whichever
// sorts first" both fail. One arrangement alone leaves one of those alive.
//
// Confirmed by mutation: reversing the comparison passed the whole suite.
func TestNewestSource_PicksTheNewestWhicheverWayTheIDsSort(t *testing.T) {
	for _, c := range []struct {
		name  string
		first bool
	}{
		{"the newest source sorts first by ID", true},
		{"the newest source sorts last by ID", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, want := twoSourceStore(t, c.first)
			store, err := slice.Open(root)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			m, err := newestSource(store, root)
			if err != nil {
				t.Fatalf("newestSource: %v", err)
			}
			if m.ID != want {
				t.Errorf("newestSource chose %s, want the most recently written %s", m.ID, want)
			}
		})
	}
}

// TestNewestSource_ChoosesTheSameWayEveryRunWhenTheTimestampsTie pins the
// half of the choice the timestamps cannot make.
//
// Two sources written in the same second are a real outcome of one scripted
// fetch, and at that point the comparison decides nothing. What must NOT
// happen is that the answer depends on the order the filesystem handed the
// directories back: a render that picks one source today and the other
// tomorrow, from an unchanged store, is a bug nobody can reproduce on
// purpose. The tie falls to the lower ID, which is stable because Sources
// returns them sorted -- an upstream guarantee this asserts rather than
// assumes, since it is load-bearing here and nothing local would catch its
// removal.
func TestNewestSource_ChoosesTheSameWayEveryRunWhenTheTimestampsTie(t *testing.T) {
	root, _ := twoSourceStore(t, true)
	store, err := slice.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sources, err := store.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if !(sources[0].ID < sources[1].ID) {
		t.Fatalf("Sources returned %s before %s: the tie-break below rests on this order", sources[0].ID, sources[1].ID)
	}
	tied := time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)
	for _, m := range sources {
		setUpdated(t, root, m.ID, tied)
	}
	for i := range 5 {
		fresh, err := slice.Open(root)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		m, err := newestSource(fresh, root)
		if err != nil {
			t.Fatalf("newestSource: %v", err)
		}
		if m.ID != sources[0].ID {
			t.Fatalf("call %d chose %s, want the lower ID %s: a tie has to resolve the same way every run", i, m.ID, sources[0].ID)
		}
	}
}

// TestLocal_OverzoomedKeepsTheWORSTViewAndCoverageCannotSeeIt is the number
// that stops the summary telling a flattering lie.
//
// The two views below are drawn from the SAME one-cell store and both come
// back fully covered. One is drawn at the zoom its tiles were stored at and
// is honest pixel for pixel; the other asks for four times the detail and
// gets it by stretching the same tiles. Coverage cannot tell them apart -- it
// counts tiles drawn, not the detail in them -- so a render reporting only
// coverage says 100% over both, and the second is a coloured shape with none
// of the ground's features in it.
//
// That is not hypothetical. Measured against a real store: covered 1.000,
// overzoomed 1.000, and a summary line claiming a clean draw.
//
// Both orders are asserted for the same reason Coverage's test asserts both:
// one order alone cannot separate "keeps the worst" from "keeps the first" or
// "keeps the last". Note the direction is opposite to Coverage -- this
// fraction is bad when HIGH -- which is exactly the kind of thing that gets
// copied wrong from the line above it.
func TestLocal_OverzoomedKeepsTheWorstViewAndCoverageCannotSeeIt(t *testing.T) {
	root, cell := oneCellFixtureStore(t, fixtureLat, fixtureLon)
	whole := func(px int) View {
		return View{
			West: cell.West, East: cell.East,
			South: cell.South, North: cell.North,
			Width: px, Height: px,
		}
	}
	// At the stored tiles' own scale nothing is stretched; asking for four
	// times the pixels over the same ground stretches all of it.
	sharp, stretched := whole(256), whole(1024)

	for _, c := range []struct {
		name  string
		views []View
	}{
		{"the stretched view drawn first", []View{stretched, sharp}},
		{"the stretched view drawn last", []View{sharp, stretched}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := OpenLocal(root, darkInks(), nil)
			if err != nil {
				t.Fatalf("OpenLocal: %v", err)
			}
			for i, v := range c.views {
				if _, err := p.Image(context.Background(), v); err != nil {
					t.Fatalf("Image %d: %v", i, err)
				}
			}
			if got := p.Coverage(); got != 1 {
				t.Fatalf("precondition: coverage is %v, so this pair does not isolate overzoom from coverage", got)
			}
			if got := p.Overzoomed(); got != 1 {
				t.Errorf("Overzoomed() = %.3f after drawing a fully stretched view and a sharp one, want the worst of them, 1", got)
			}
		})
	}

	// And the sharp view alone must report nothing, or the figure would be
	// noise on every render rather than a signal on the ones that need it.
	p, err := OpenLocal(root, darkInks(), nil)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if _, err := p.Image(context.Background(), sharp); err != nil {
		t.Fatalf("Image: %v", err)
	}
	if got := p.Overzoomed(); got != 0 {
		t.Errorf("Overzoomed() = %.3f over tiles drawn at their own zoom, want 0", got)
	}
}
