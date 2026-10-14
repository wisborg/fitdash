package tilemap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	osm "github.com/wisborg/osmbase/render"
)

// Every outline here is made up: square countries side by side on the
// equator, a degree -- 111 km -- wide each, named for what they are.

// square is a Natural Earth feature: a square from west to east, south to
// north, with the given properties.
func square(west, south, east, north float64, props string) string {
	return fmt.Sprintf(`{"type":"Feature","properties":{%s},"geometry":{"type":"Polygon","coordinates":[[[%g,%g],[%g,%g],[%g,%g],[%g,%g],[%g,%g]]]}}`,
		props, west, south, east, south, east, north, west, north, west, south)
}

func collection(features ...string) string {
	return `{"type":"FeatureCollection","features":[` + strings.Join(features, ",") + `]}`
}

// testStore is a store root holding the outlines given, as "osmbase
// boundaries" leaves them.
func testStore(t *testing.T, countries, states string) string {
	t.Helper()
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for layer, doc := range map[boundary.Layer]string{boundary.Countries: countries, boundary.Regions: states} {
		if doc == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, boundary.File(boundary.DefaultDetail, layer)), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Three countries in a row, the middle one narrow: Wideland from 0° to 3°
// east, Narrowland to 3.5°, Farland to 6°.
func threeCountries() string {
	return collection(
		square(0, -1, 3, 1, `"NAME":"Wideland","ADM0_A3":"WID","WIKIDATAID":"Q1"`),
		square(3, -1, 3.5, 1, `"NAME":"Narrowland","ADM0_A3":"NAR","WIKIDATAID":"Q2"`),
		square(3.5, -1, 6, 1, `"NAME":"Farland","ADM0_A3":"FAR","WIKIDATAID":"Q3"`),
	)
}

func byName(a boundary.Area) map[string]mvt.Value {
	return map[string]mvt.Value{"kind": mvt.StringValue("country"), "name": mvt.StringValue(a.Name)}
}

// A route flown east across three countries names each a little way in
// from each border it crosses -- once, half way, in the one it crosses in
// less than a stretch's length -- beside the route and inside the country
// it names.
func TestCountriesAreNamedInFromTheBordersTheRouteCrosses(t *testing.T) {
	p := loadPolitical(testStore(t, threeCountries(), ""))
	if p == nil {
		t.Fatal("no outlines read")
	}
	route := []osm.Coord{{Lat: 0, Lon: 0.2}, {Lat: 0, Lon: 5.8}}
	places := p.borderPlaces(route, byName)
	got := map[string]int{}
	for _, pl := range places {
		name, _ := pl.Tags["name"].Text()
		got[name]++
		if pl.Layer != "places" {
			t.Errorf("%s is named as a feature of %q, want places", name, pl.Layer)
		}
		a, ok := p.countryAt(pl.At)
		if !ok || a.Name != name {
			t.Errorf("%s is named at %v, which is in %q", name, pl.At, a.Name)
		}
		if aside := metresBetween(pl.At, osm.Coord{Lat: 0, Lon: pl.At.Lon}); aside < borderNameAsides[0]*0.9 || aside > borderNameAsides[0]*1.1 {
			t.Errorf("%s is named %.0f m from the route, want %.0f", name, aside, borderNameAsides[0])
		}
	}
	want := map[string]int{"Wideland": 2, "Narrowland": 1, "Farland": 2}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s named %d times, want %d (all: %v)", name, got[name], n, got)
		}
	}
}

// A country the route leaves for a short way -- across a lagoon its
// outline leaves out -- and comes back to is one stretch, named at its two
// ends and not again either side of the gap.
func TestACountryLeftForAMomentIsOneStretch(t *testing.T) {
	countries := collection(
		square(0, -1, 1.5, 1, `"NAME":"Lagoonland","ADM0_A3":"LAG"`),
		square(1.6, -1, 3, 1, `"NAME":"Lagoonland","ADM0_A3":"LAG"`),
	)
	p := loadPolitical(testStore(t, countries, ""))
	places := p.borderPlaces([]osm.Coord{{Lat: 0, Lon: 0.1}, {Lat: 0, Lon: 2.9}}, byName)
	if len(places) != 2 {
		t.Errorf("named %d times across a 10 km gap, want twice, at the two ends", len(places))
	}
}

// A name that would be put off the country's edge is put nearer the route,
// and on it where nothing to either side is in the country.
func TestANameStaysInItsCountry(t *testing.T) {
	// A strip 0.5° -- 55 km -- tall, the route along its middle: 45 km to
	// either side is out of it, 25 km is in.
	strip := collection(square(0, -0.25, 3, 0.25, `"NAME":"Stripland","ADM0_A3":"STR"`))
	p := loadPolitical(testStore(t, strip, ""))
	for _, pl := range p.borderPlaces([]osm.Coord{{Lat: 0, Lon: 0.1}, {Lat: 0, Lon: 2.9}}, byName) {
		aside := metresBetween(pl.At, osm.Coord{Lat: 0, Lon: pl.At.Lon})
		if aside < 24_000 || aside > 26_000 {
			t.Errorf("named %.0f m from the route, want 25 km, the farthest still in the country", aside)
		}
	}
}

// A store without outlines names nothing and draws no state apart.
func TestNoOutlinesNoPolitics(t *testing.T) {
	if p := loadPolitical(t.TempDir()); p != nil {
		t.Error("outlines read from a store that holds none")
	}
}

// A border is a major division's in a country whose states Natural Earth
// ranks 2 or below, by the rank most of them have: not in one ranked 3, and
// not in one whose single outlying territory alone is ranked 2.
func TestMajorDivisionsAreTheStatesOfCountriesRankedHigh(t *testing.T) {
	countries := collection(
		square(0, -1, 1, 1, `"NAME":"Federation","ADM0_A3":"FED"`),
		square(1, -1, 2, 1, `"NAME":"Kingdom","ADM0_A3":"KIN"`),
		square(2, -1, 3, 1, `"NAME":"Republic","ADM0_A3":"REP"`),
	)
	states := collection(
		square(0, -1, 0.5, 1, `"name":"East","adm0_a3":"FED","scalerank":2`),
		square(0.5, -1, 1, 1, `"name":"West","adm0_a3":"FED","scalerank":2`),
		square(1, -1, 2, 1, `"name":"Shire","adm0_a3":"KIN","scalerank":3`),
		square(2, -1, 2.4, 1, `"name":"Isle","adm0_a3":"REP","scalerank":2`),
		square(2.4, -1, 2.7, 1, `"name":"North","adm0_a3":"REP","scalerank":7`),
		square(2.7, -1, 3, 1, `"name":"South","adm0_a3":"REP","scalerank":7`),
	)
	p := loadPolitical(testStore(t, countries, states))
	for _, c := range []struct {
		lon  float64
		want bool
	}{{0.5, true}, {1.5, false}, {2.5, false}, {4, false}} {
		if got := p.majorDivision(osm.Coord{Lat: 0, Lon: c.lon}); got != c.want {
			t.Errorf("a state's border at %g° east is major: %v, want %v", c.lon, got, c.want)
		}
	}
}

// tileSource is a TileSource over a few prepared tiles.
type tileSource map[[3]uint32][]byte

func (s tileSource) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	data, ok := s[[3]uint32{uint32(z), x, y}]
	return data, ok, nil
}

// A country is named as the map's own tiles name it -- the place with its
// Wikidata id, every tag but its min_zoom -- from the tile where Natural
// Earth names it, or a coarser one the store holds there; and in Natural
// Earth's name where no tile names it.
func TestACountryIsNamedAsTheMapNamesIt(t *testing.T) {
	label := boundary.Coord{Lat: 10, Lon: 10}
	x, y, err := mercator.TileAt(5, label.Lon, label.Lat)
	if err != nil {
		t.Fatal(err)
	}
	tile, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "places",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 2048, Y: 2048}}},
			Tags: []osmbasetest.Tag{
				{Key: "kind", Value: mvt.StringValue("country")},
				{Key: "wikidata", Value: mvt.StringValue("Q7")},
				{Key: "name", Value: mvt.StringValue("Lokalland")},
				{Key: "name:en", Value: mvt.StringValue("Localland")},
				{Key: "min_zoom", Value: mvt.DoubleValue(3)},
			},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	src := tileSource{{5, x, y}: tile}
	known := boundary.NewArea("Localland", "country", nil)
	known.Wikidata, known.Label, known.HasLabel = "Q7", label, true
	tags := countryTags(src, known)
	if name, _ := tags["name"].Text(); name != "Lokalland" {
		t.Errorf("named %q, want the map's own Lokalland", name)
	}
	if en, _ := tags["name:en"].Text(); en != "Localland" {
		t.Errorf("its English name is %q, want Localland", en)
	}
	if _, ok := tags["min_zoom"]; ok {
		t.Error("the name keeps the min_zoom that would hide it at a coarse zoom")
	}

	unknown := known
	unknown.Name, unknown.Wikidata = "Elsewhere", "Q8"
	if name, _ := countryTags(src, unknown)["name"].Text(); name != "Elsewhere" {
		t.Errorf("a country no tile names is named %q, want Natural Earth's Elsewhere", name)
	}
}
