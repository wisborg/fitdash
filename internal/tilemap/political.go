package tilemap

import (
	"math"
	"slices"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	osm "github.com/wisborg/osmbase/render"
)

// political is what a flyover knows of countries beyond what its tiles say,
// from the Natural Earth outlines a store keeps once "osmbase boundaries"
// has fetched them: where each country is, and which countries' states are
// worth drawing.
//
// The tiles name a country at one point, and a flight that does not pass
// over that point never sees the name; their borders carry an admin level
// and nothing else, so the line between two American states and the one
// between two Danish regions are the same line. The outlines answer both.
// Nothing here reaches the network: a store without them flies over its
// countries as the tiles alone draw them.
type political struct {
	countries *boundary.Set
	// major are the countries whose states are major divisions; see
	// majorDivisionRank.
	major map[string]bool
}

// majorDivisionRank is the rank Natural Earth gives a country's states, at
// or below which they are major divisions: drawn strongly and named. Two
// takes the federations whose states are names a viewer knows -- the
// United States, Canada, Australia, Brazil, India, Russia, China,
// Indonesia, South Africa, Bangladesh -- and leaves Germany's, ranked 3,
// France's and Spain's with them, and Denmark's regions at 7 and England's
// counties at 8, as the faint lines a map draws them.
const majorDivisionRank = 2

// loadPolitical reads the outlines from the store at root, or nil where it
// holds none.
func loadPolitical(root string) *political {
	src := boundary.Open(root, boundary.DefaultDetail)
	countries := src.Outlines(boundary.Countries)
	if countries == nil {
		return nil
	}
	p := &political{countries: countries, major: map[string]bool{}}
	if states := src.Outlines(boundary.Regions); states != nil {
		// A country's states are ranked as most of them are: Natural
		// Earth ranks a few of a country's states apart from the rest --
		// an outlying territory -- and one of those is not the country's
		// rank.
		ranks := map[string]map[int]int{}
		for _, a := range states.Areas() {
			if a.Country == "" || a.Rank == 0 {
				continue
			}
			if ranks[a.Country] == nil {
				ranks[a.Country] = map[int]int{}
			}
			ranks[a.Country][a.Rank]++
		}
		for country, byRank := range ranks {
			best, n := 0, -1
			for rank, count := range byRank {
				if count > n || count == n && rank < best {
					best, n = rank, count
				}
			}
			p.major[country] = best <= majorDivisionRank
		}
	}
	return p
}

// countryAt is the country holding c, if one does.
func (p *political) countryAt(c osm.Coord) (boundary.Area, bool) {
	return p.countries.At(c.Lat, c.Lon)
}

// majorDivision is osm.Options.MajorDivision: whether a state's border at c
// is in a country whose states are major divisions.
func (p *political) majorDivision(c osm.Coord) bool {
	a, ok := p.countryAt(c)
	return ok && p.major[a.Country]
}

// borderNameStep is how far apart, along the route, the country under it is
// asked: about two kilometres, a border crossed between two asks put off
// by at most that.
const borderNameStep = 2000.0

// borderNameInset is how far into a country, along the route, its name is
// put from each border the route crosses into or out of it, and
// borderNameAsides how far to the side of the route, the farthest that
// keeps it in the country: in from the line, where the camera sees it come
// up from the horizon and pass, and off the route, which is drawn over
// the map. A country's name at a cruise is a hundred kilometres across, and
// put a dozen to the side the route ran through its letters. A country the
// route is in for less than borderNameShort is named once, half way.
//
// A country left for less than borderNameGap -- a lagoon its outline
// leaves out, a bay the route cuts across -- is the same stretch of it:
// taken as two, Poland's coast was named twice a few kilometres apart,
// and two of one name that near each half hide the other.
const (
	borderNameGap   = 30_000.0
	borderNameInset = 50_000.0
	borderNameShort = 3 * borderNameInset
)

var borderNameAsides = []float64{45_000, 25_000, 12_000}

// borderPlaces are the places a flight along route names its countries
// at: for each stretch of the route over one country, a point a little way
// in from each end -- the country it has just entered, and the one it is
// about to leave -- or one half way along a short stretch, each a little to
// the side of the route. names gives each country's tags; one it gives
// none for is not named.
func (p *political) borderPlaces(route []osm.Coord, names func(boundary.Area) map[string]mvt.Value) []osm.ExtraPlace {
	type stretch struct {
		area       boundary.Area
		start, end float64 // metres along the route
	}
	var stretches []stretch
	pts := resample(route, borderNameStep)
	along := 0.0
	for i, c := range pts {
		if i > 0 {
			along += metresBetween(pts[i-1], c)
		}
		a, ok := p.countryAt(c)
		switch {
		case !ok:
			continue
		case len(stretches) > 0 && sameCountry(stretches[len(stretches)-1].area, a) && stretches[len(stretches)-1].end >= along-borderNameGap:
			stretches[len(stretches)-1].end = along
		default:
			stretches = append(stretches, stretch{area: a, start: along, end: along})
		}
	}
	var out []osm.ExtraPlace
	type namedAt struct {
		area boundary.Area
		at   osm.Coord
	}
	var named []namedAt
	for _, s := range stretches {
		tags := names(s.area)
		if tags == nil {
			continue
		}
		at := []float64{(s.start + s.end) / 2}
		if s.end-s.start >= borderNameShort {
			at = []float64{s.start + borderNameInset, s.end - borderNameInset}
		}
		for _, d := range at {
			c, ok := p.aside(pts, d, s.area)
			// Never nearer than twice the inset to where the country is
			// named already: two of one name that near each half hide the
			// other.
			if !ok || slices.ContainsFunc(named, func(n namedAt) bool {
				return sameCountry(n.area, s.area) && metresBetween(n.at, c) < 2*borderNameInset
			}) {
				continue
			}
			named = append(named, namedAt{s.area, c})
			out = append(out, osm.ExtraPlace{Layer: "places", At: c, Tags: tags})
		}
	}
	return out
}

// aside is the point d metres along pts, moved to the left of the way the
// route runs there by the first of borderNameAsides that keeps it in area
// -- or to the right by it -- or onto the route if none does.
func (p *political) aside(pts []osm.Coord, d float64, area boundary.Area) (osm.Coord, bool) {
	on, bearing, ok := pointAlong(pts, d)
	if !ok {
		return osm.Coord{}, false
	}
	for _, by := range borderNameAsides {
		for _, side := range []float64{-90, 90} {
			c := ahead(on, bearing+side, by)
			if a, ok := p.countryAt(c); ok && sameCountry(a, area) {
				return c, true
			}
		}
	}
	return on, true
}

// sameCountry says whether two of the outlines are one country.
func sameCountry(a, b boundary.Area) bool {
	if a.Country != "" || b.Country != "" {
		return a.Country == b.Country
	}
	return a.Name == b.Name
}

// countryTags are the tags the map's own tiles give country a, read from
// the tile where Natural Earth names it, or the tiles round it -- so the name is spelled as the rest of the map spells
// it, in every language it carries, and --lang picks among them as it
// does everywhere else -- with its min_zoom taken off, so it is named at
// whatever zoom the tile it falls in is drawn. Where no tile names it,
// Natural Earth's own name, which is English. nil for a country with no
// name at all.
func countryTags(src osm.TileSource, a boundary.Area) map[string]mvt.Value {
	if a.Wikidata != "" && a.HasLabel {
		if tags := tileCountryTags(src, a); tags != nil {
			return tags
		}
	}
	if a.Name == "" {
		return nil
	}
	return map[string]mvt.Value{
		"kind": mvt.StringValue("country"),
		"name": mvt.StringValue(a.Name),
	}
}

// countryNameZooms are the zooms a country's name is looked for at, in
// turn: every country the data names is named by zoom 6, but a store
// holds the coarser zooms over more of the world than the finer -- one
// filled for a flight from Copenhagen holds zoom 5 over the north of
// Sweden, where Natural Earth names it, and not zoom 6, and Sweden was
// named in English.
var countryNameZooms = []uint8{6, 5, 4, 3}

func tileCountryTags(src osm.TileSource, a boundary.Area) map[string]mvt.Value {
	for _, z := range countryNameZooms {
		if tags := tileCountryTagsAt(src, a, z); tags != nil {
			return tags
		}
	}
	return nil
}

func tileCountryTagsAt(src osm.TileSource, a boundary.Area, z uint8) map[string]mvt.Value {
	x0, y0, err := mercator.TileAt(z, a.Label.Lon, a.Label.Lat)
	if err != nil {
		return nil
	}
	last := uint32(1)<<z - 1
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			x, y := int64(x0)+int64(dx), int64(y0)+int64(dy)
			if x < 0 || y < 0 || x > int64(last) || y > int64(last) {
				continue
			}
			data, ok, err := src.Tile(z, uint32(x), uint32(y))
			if err != nil || !ok {
				continue
			}
			tile, err := mvt.Decode(data)
			if err != nil {
				continue
			}
			layer, ok := tile.Layer("places")
			if !ok {
				continue
			}
			for _, f := range layer.Features {
				kind, _ := f.Tag("kind")
				id, _ := f.Tag("wikidata")
				if k, _ := kind.Text(); k != "country" {
					continue
				}
				if w, _ := id.Text(); w != a.Wikidata {
					continue
				}
				tags := make(map[string]mvt.Value, len(f.Tags))
				for k, v := range f.Tags {
					tags[k] = v
				}
				delete(tags, "min_zoom")
				return tags
			}
		}
	}
	return nil
}

// The few sums on the sphere the countries are placed with; a spherical
// earth is metres off over the tens of kilometres they are used across.

const earthRadius = 6371008.8

func metresBetween(a, b osm.Coord) float64 {
	la1, la2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dla, dlo := la2-la1, (b.Lon-a.Lon)*math.Pi/180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * earthRadius * math.Asin(math.Min(1, math.Sqrt(h)))
}

// bearing is the direction from a to b, in degrees clockwise from north.
func bearing(a, b osm.Coord) float64 {
	la1, la2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dlo := (b.Lon - a.Lon) * math.Pi / 180
	y := math.Sin(dlo) * math.Cos(la2)
	x := math.Cos(la1)*math.Sin(la2) - math.Sin(la1)*math.Cos(la2)*math.Cos(dlo)
	return math.Atan2(y, x) * 180 / math.Pi
}

// ahead is the point d metres from c in direction deg, clockwise from north.
func ahead(c osm.Coord, deg, d float64) osm.Coord {
	la, lo, b, r := c.Lat*math.Pi/180, c.Lon*math.Pi/180, deg*math.Pi/180, d/earthRadius
	la2 := math.Asin(math.Sin(la)*math.Cos(r) + math.Cos(la)*math.Sin(r)*math.Cos(b))
	lo2 := lo + math.Atan2(math.Sin(b)*math.Sin(r)*math.Cos(la), math.Cos(r)-math.Sin(la)*math.Sin(la2))
	return osm.Coord{Lat: la2 * 180 / math.Pi, Lon: math.Remainder(lo2*180/math.Pi, 360)}
}

// resample is route with a point every step metres or closer along each
// leg, so a border crossed between two of its fixes -- an ocean apart, on
// a flight -- is found within a step.
func resample(route []osm.Coord, step float64) []osm.Coord {
	var out []osm.Coord
	for i, c := range route {
		if i > 0 {
			prev := route[i-1]
			n := int(metresBetween(prev, c) / step)
			for k := 1; k <= n; k++ {
				f := float64(k) / float64(n+1)
				out = append(out, osm.Coord{Lat: prev.Lat + (c.Lat-prev.Lat)*f, Lon: prev.Lon + (c.Lon-prev.Lon)*f})
			}
		}
		out = append(out, c)
	}
	return slices.Clip(out)
}

// pointAlong is the point d metres along pts, and the way they run there.
func pointAlong(pts []osm.Coord, d float64) (osm.Coord, float64, bool) {
	along := 0.0
	for i := 1; i < len(pts); i++ {
		leg := metresBetween(pts[i-1], pts[i])
		if leg > 0 && along+leg >= d {
			f := (d - along) / leg
			a, b := pts[i-1], pts[i]
			return osm.Coord{Lat: a.Lat + (b.Lat-a.Lat)*f, Lon: a.Lon + (b.Lon-a.Lon)*f}, bearing(a, b), true
		}
		along += leg
	}
	return osm.Coord{}, 0, false
}
