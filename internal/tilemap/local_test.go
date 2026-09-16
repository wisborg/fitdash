package tilemap

import (
	"context"
	"errors"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

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
	p, err := OpenLocal(root, darkInks())
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
	p, err := OpenLocal(root, darkInks())
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
	p, err := OpenLocal(root, darkInks())
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
	_, err := OpenLocal(root, darkInks())
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
	if _, err := OpenLocal(missing, darkInks()); err == nil {
		t.Error("a store directory that does not exist was accepted")
	} else if !strings.Contains(err.Error(), missing) {
		t.Errorf("error does not name the directory: %v", err)
	}

	empty := filepath.Join(t.TempDir(), "store")
	if _, err := slice.Create(empty, slice.Config{}); err != nil {
		t.Fatalf("creating an empty store: %v", err)
	}
	_, err := OpenLocal(empty, darkInks())
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
	p, err := OpenLocal(localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit), darkInks())
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
// Asserted through OpenLocal rather than over plainCredit alone because the
// conversion has to happen at the point the manifest is read: that is what
// makes the string in the frame, the string in the summary and the string in
// this accessor one string.
func TestLocal_AnHTMLAttributionBecomesTextTheFrameCanCarry(t *testing.T) {
	const markup = `<a href="https://example.test/copyright" target="_blank">&copy; OpenStreetMap contributors</a>`
	const want = "© OpenStreetMap contributors"

	p, err := OpenLocal(localFixtureStore(t, fixtureLat, fixtureLon, markup), darkInks())
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
