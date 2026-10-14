package tilemap

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"math"
	"sync"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// The zooms a flight's pyramid draws tiles at. A tile is perspective.TileSize
// pixels, twice a slippy-map tile, so a zoom 17 tile carries zoom 18's
// detail: a house and its garden from a camera a few hundred metres off.
// The coarsest is a continent's: a flight's overview takes in thousands of
// kilometres, and a pyramid stopping at 11, as the first one did, drew it
// from millions of zoom-11 tiles; a run never sees ground that far off.
const (
	flightMinZoom = 3
	flightMaxZoom = 17
)

// flightCell is the mesh's cell for a camera flightCellDistance off, in
// metres. A camera further off lays its mesh on a lattice of twice, four
// times ... the cell, so the overview many kilometres up does not build
// millions of cells, and the lattices nest, so ground shared by two of them
// is read at the same points.
const (
	flightCell         = 15.0
	flightCellDistance = 800.0
)

// FlightMap is a map laid out for a camera flying over it: what a flyover
// draws its pictures from. *Flight is one.
type FlightMap interface {
	// PlanNames works out, before any frame is drawn, how strongly each
	// name is shown in each frame of the flight whose cameras are cams --
	// each drawn at o -- smoothed over smooth frames either side; see
	// perspective.NamePlan. Pictures drawn after it stand their names as
	// planned; before it, or without it, each picture works out its own.
	PlanNames(ctx context.Context, cams []perspective.Camera, o perspective.Options, smooth int, progress func(done, total int)) error
	// Picture draws frame i of the flight from cam, at o's size and in its
	// colours of sky and haze, with its names stood on it.
	Picture(ctx context.Context, i int, cam perspective.Camera, o perspective.Options) (*perspective.Picture, error)
	// Ground is the height of the ground at c, in metres, as the pictures
	// shape it; false where it is not known.
	Ground(c osm.Coord) (float64, bool)
	// Leveled is true when there is no elevation and the map is laid over
	// level ground.
	Leveled() bool
	// Attribution is the credit every picture owes.
	Attribution() string
	// NameCountries names the countries along route where the route
	// crosses into and out of each, and draws their states' borders where
	// those are a major country's -- before any tile is drawn, which is to
	// say before PlanNames. It reports how many places it named the
	// countries at, and false when the store holds no country outlines to
	// do either from.
	NameCountries(route []osm.Coord) (named int, ok bool)
}

// Flight is the local map laid out for a camera flying over it: a pyramid
// of fixed tiles over the ground the flight sees, drawn when first seen, and
// the elevation the ground is shaped from. See perspective.Tiles for why a
// moving camera needs a pyramid rather than one map.
//
// Every tile is drawn as part of one map: the areas a tile stacks are ranked
// by their size over the whole flight, measured once, and contours are drawn
// at one interval for each zoom, so the tiles meet without seams. Names are
// lifted off the tiles and stood on each picture by Picture.
type Flight struct {
	l            *Local
	tiles        *perspective.Tiles
	heights      osm.HeightSource
	leveled      bool
	exaggeration float64
	lat          float64 // the middle of the flight, which fixes the lattices
	credit       string

	mu       sync.Mutex
	lattices map[int]perspective.Lattice

	// plan is the flight's names, planned; nil before PlanNames.
	plan *perspective.NamePlan

	// pol is the store's country outlines, nil where it holds none, and
	// borders the places NameCountries names the countries at; see
	// political.
	pol     *political
	borders []osm.ExtraPlace
}

// Flight lays the map out over region, the ground every camera of a flight
// sees, with hills exaggeration times their height.
func (l *Local) Flight(ctx context.Context, region slice.Bounds, exaggeration float64) (FlightMap, error) {
	b := osm.Bounds{West: region.West, South: region.South, East: region.East, North: region.North}
	o := l.opts
	o.LiftLabels = true
	// Measured as deep as a few thousand tiles of the flight's ground
	// allow: a run's whole ground at street detail, a flight's at the detail
	// of a country. Measured at 15 over a flight from Copenhagen to Bangkok
	// it was millions of tiles and ran out of time, and the flight was drawn
	// flat. An area the measure misses is ranked by what each tile holds of
	// it, as any one map ranks it.
	areas, err := osm.MeasureAreas(ctx, l.source, o, b, affordableZoom(region, 15))
	if err != nil {
		return nil, fmt.Errorf("tilemap: measuring the map's areas over the flight: %w", err)
	}
	if exaggeration == 0 {
		exaggeration = 1
	}
	f := &Flight{
		l: l, exaggeration: exaggeration, lat: (region.South + region.North) / 2,
		heights: perspective.Level, leveled: true,
		lattices: map[int]perspective.Lattice{},
		pol:      loadPolitical(l.root),
	}
	f.credit = l.credit
	if l.terrain != nil {
		f.heights, f.leveled = l.terrain.Heights(), false
		// What the pictures owe the elevation, known before any is drawn:
		// the credit is fitted to the panel once, before the first frame,
		// and a tile only says what it owes once it has been drawn.
		// At as deep a zoom as a few thousand tiles of the ground allow, as
		// the areas are measured: asked at the flight's finest, 18, over a
		// flight across a continent, the credit enumerated billions of
		// tiles and never came back.
		short, _, err := l.terrain.Credit(region, affordableZoom(region, flightMaxZoom+1))
		if err != nil {
			return nil, fmt.Errorf("tilemap: working out what the flight owes the elevation in %s: %w", l.terrain.Root(), err)
		}
		if short != "" {
			f.credit = l.credit + " | " + short
		}
	}
	tiles, err := perspective.NewTiles(func(ctx context.Context, z uint8, x, y uint32) (*perspective.Tile, error) {
		return l.flightTile(ctx, z, x, y, areas, f.political())
	}, flightMinZoom, flightMaxZoom, 0)
	if err != nil {
		return nil, err
	}
	f.tiles = tiles
	return f, nil
}

// flightTile draws one tile of a flight's pyramid: as part of the one map,
// with its names lifted, in faces of its own -- the pictures stand a tile's
// names afterwards, one at a time, while other tiles are being drawn, and a
// font face drawn with by two at once corrupts both.
func (l *Local) flightTile(ctx context.Context, z uint8, x, y uint32, areas *osm.Areas, pol flightPolitics) (*perspective.Tile, error) {
	n := math.Exp2(float64(z))
	b := slice.Bounds{
		West: float64(x)/n*360 - 180, East: float64(x+1)/n*360 - 180,
		North: math.Atan(math.Sinh(math.Pi*(1-2*float64(y)/n))) * 180 / math.Pi,
		South: math.Atan(math.Sinh(math.Pi*(1-2*float64(y+1)/n))) * 180 / math.Pi,
	}
	// Held only where the tile reads cells at all: one drawn from a zoom
	// above the store's cell zoom reads the overview, and a flight's coarse
	// tiles, each over thousands of cells, held -- and moved the eviction
	// clock of -- every one of them for files they never opened.
	if z+1 >= l.store.CellZoom() {
		if cells, err := l.store.CellsFor(b); err == nil {
			defer l.source.Hold(cells).Release()
		}
	}
	v := View{North: b.North, West: b.West, South: b.South, East: b.East, Width: perspective.TileSize, Height: perspective.TileSize}
	rend, ids, release, err := l.flightRenderer(v, z, areas, pol)
	if err != nil {
		return nil, err
	}
	defer release()
	res, err := rend.Render(ctx, osm.View{Bounds: osm.Bounds{West: b.West, South: b.South, East: b.East, North: b.North}, Width: perspective.TileSize, Height: perspective.TileSize})
	if err != nil {
		return nil, fmt.Errorf("tilemap: drawing tile %d/%d/%d from the local map store %s: %w", z, x, y, l.root, err)
	}
	l.record(res, ids)
	img := res.Image
	if img.Rect.Min != (image.Point{}) {
		moved := image.NewRGBA(image.Rect(0, 0, img.Rect.Dx(), img.Rect.Dy()))
		draw.Draw(moved, moved.Rect, img, img.Rect.Min, draw.Src)
		img = moved
	}
	return &perspective.Tile{Image: img, Places: res.PointLabels, Lines: res.LineLabels}, nil
}

// flightRenderer is rendererFor for a tile of a flight: the areas measured
// over the flight, the contour interval of the tile's zoom, every name
// lifted, and faces of its own.
//
// The tile's elevation sources come back with it; see rendererFor. So does
// what to call once the tile is drawn, done with its faces; see
// FreshLabels.
func (l *Local) flightRenderer(v View, z uint8, areas *osm.Areas, pol flightPolitics) (*osm.Renderer, []string, func(), error) {
	o := l.opts
	o.Areas, o.LiftLabels = areas, true
	// A flight's picture stands the names of a dozen tiles at once, the
	// nearest drawn at the zoom where every village is named: places are
	// named as on a map flightPlaceShift zooms further out. Countries and
	// states are not shifted (osm.Style.ShiftPlaceLabels).
	//
	// Its borders and its countries' names are drawn to be seen first:
	// flown over, a border is the one line that says where the flight is.
	// See osm.Style.EmphasisePolitical, and flightPolitics for the major
	// countries' states and the countries named at the borders.
	o.Style = o.Style.ShiftPlaceLabels(flightPlaceShift).EmphasisePolitical()
	o.MajorDivision, o.ExtraPlaces = pol.major, pol.places
	o.ContourInterval = flightContourInterval(z + 1)
	release := func() {}
	if l.freshLabels != nil {
		var faces LabelFaces
		faces, release = l.freshLabels()
		o.LabelFace, o.LabelFaceFor = faces.at(1), faces.at
	} else {
		o.LabelFace, o.LabelFaceFor = nil, nil // no faces of its own: no names
	}
	var ids []string
	if l.terrain != nil {
		bb := slice.Bounds{West: v.West, South: v.South, East: v.East, North: v.North}
		var err error
		if ids, err = l.terrain.SourcesIn(bb, z+1); err != nil {
			release()
			return nil, nil, nil, fmt.Errorf("tilemap: working out what this tile owes the elevation in %s: %w", l.terrain.Root(), err)
		}
		o.Terrain, o.TerrainAttribution = l.terrain.Heights(), l.terrain.ShortCredit(ids)
	}
	rend, err := osm.New(l.source, o)
	if err != nil {
		release()
		return nil, nil, nil, err
	}
	return rend, ids, release, nil
}

// flightPolitics is what a flight's tiles are told of its countries: which
// states' borders are a major country's (nil, none) and the places the
// countries are named at along the route. See political.
type flightPolitics struct {
	major  func(osm.Coord) bool
	places []osm.ExtraPlace
}

// political is what the flight's tiles are told of its countries, as
// NameCountries left it.
func (f *Flight) political() flightPolitics {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pol == nil {
		return flightPolitics{}
	}
	return flightPolitics{major: f.pol.majorDivision, places: f.borders}
}

// NameCountries names the flight's countries along route; see FlightMap.
func (f *Flight) NameCountries(route []osm.Coord) (int, bool) {
	if f.pol == nil {
		return 0, false
	}
	names := map[string]map[string]mvt.Value{}
	places := f.pol.borderPlaces(route, func(a boundary.Area) map[string]mvt.Value {
		key := a.Country + "|" + a.Name
		if tags, ok := names[key]; ok {
			return tags
		}
		tags := countryTags(f.l.source, a)
		names[key] = tags
		return tags
	})
	f.mu.Lock()
	f.borders = places
	f.mu.Unlock()
	return len(places), true
}

// flightPlaceShift is how many zooms later a flight's tiles name their
// places than a map at their zoom would. Measured on a climb out of
// Bangkok, where one tile at the zoom of the nearest ground names a hundred
// and sixty villages: two still left the plain a wall of names; three keep
// the towns, about fifteen to a picture west of the city, and leave the
// villages to the suburbs right under the camera.
const flightPlaceShift = -3

// flightContourInterval is the contour interval for a tile drawn at map zoom
// z: the interval a map at that zoom starts from, fixed rather than widened
// on steep ground as a single map's is, because a tile widening its own
// would draw lines twice as dense on one side of a seam as the other.
func flightContourInterval(z uint8) float64 {
	switch {
	case z < 12:
		return 100
	case z < 13:
		return 50
	case z < 14:
		return 20
	case z < 16:
		return 10
	}
	return 5
}

// scene is what frame i is drawn from with cam, at o's size.
func (f *Flight) scene(cam perspective.Camera, o perspective.Options) (perspective.Scene, error) {
	lat, err := f.lattice(cam.Distance)
	if err != nil {
		return perspective.Scene{}, err
	}
	v, err := lat.View(cam.MapBounds(float64(o.Width)/float64(o.Height)), perspective.DefaultStep)
	if err != nil {
		return perspective.Scene{}, err
	}
	return perspective.Scene{Tiles: f.tiles, View: v, Heights: f.heights, Exaggeration: f.exaggeration, Step: perspective.DefaultStep}, nil
}

// PlanNames plans the flight's names; see FlightMap.
func (f *Flight) PlanNames(ctx context.Context, cams []perspective.Camera, o perspective.Options, smooth int, progress func(done, total int)) error {
	plan, err := perspective.PlanNames(ctx, len(cams), func(i int) (perspective.Scene, perspective.Camera, perspective.Options, error) {
		s, err := f.scene(cams[i], o)
		return s, cams[i], o, err
	}, smooth, progress)
	if err != nil {
		return fmt.Errorf("tilemap: planning the flight's names: %w", err)
	}
	f.plan = plan
	return nil
}

// Picture draws frame i of the flight from cam, at o's size and in its
// colours of sky and haze, with its names stood on it: as planned, once
// they are.
func (f *Flight) Picture(ctx context.Context, i int, cam perspective.Camera, o perspective.Options) (*perspective.Picture, error) {
	s, err := f.scene(cam, o)
	if err != nil {
		return nil, err
	}
	pic, err := perspective.RenderContext(ctx, s, cam, o)
	if err != nil {
		return nil, fmt.Errorf("tilemap: drawing the flight in perspective: %w", err)
	}
	if f.plan != nil {
		pic.DrawNamesPlanned(f.l.opts.Palette, f.plan, i)
	} else {
		pic.DrawNames(f.l.opts.Palette)
	}
	return pic, nil
}

// Steadiness is how much the planned names would have flickered and how
// much they will; zeros before PlanNames. See perspective.NamePlan.
func (f *Flight) Steadiness() (before, after float64) {
	if f.plan == nil {
		return 0, 0
	}
	return f.plan.Steadiness()
}

// lattice is the lattice for a camera distance metres off; see flightCell.
func (f *Flight) lattice(distance float64) (perspective.Lattice, error) {
	k := max(0, int(math.Round(math.Log2(distance/flightCellDistance))))
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.lattices[k]; ok {
		return l, nil
	}
	l, err := perspective.NewLattice(f.lat, flightCell*math.Exp2(float64(k)))
	if err != nil {
		return perspective.Lattice{}, err
	}
	f.lattices[k] = l
	return l, nil
}

// Ground is the height of the ground at c in metres, as the pictures shape
// it; false without elevation, or where it has none.
func (f *Flight) Ground(c osm.Coord) (float64, bool) {
	if f.leveled {
		return 0, false
	}
	const d = 0.00001
	hs, err := osm.Heights(f.heights, osm.View{Bounds: osm.Bounds{West: c.Lon - d, East: c.Lon + d, South: c.Lat - d, North: c.Lat + d}, Width: 1, Height: 1})
	if err != nil || len(hs) == 0 || math.IsNaN(float64(hs[0])) {
		return 0, false
	}
	return float64(hs[0]), true
}

// Attribution is the credit every picture of the flight owes: the map's,
// and the elevation's when the ground is shaped from it.
func (f *Flight) Attribution() string { return f.credit }

// Leveled is true when there is no elevation and the map is laid over level
// ground.
func (f *Flight) Leveled() bool { return f.leveled }

// TilesDrawn is how many tiles the flight has drawn so far.
func (f *Flight) TilesDrawn() int { return f.tiles.Drawn() }

// maxMeasuredTiles is the most tiles of a flight's ground Local.Flight reads
// to measure the map's areas over it, or to work out what it owes the
// elevation.
const maxMeasuredTiles = 4096

// affordableZoom is the deepest zoom, no deeper than z, at which region is
// at most maxMeasuredTiles tiles: a run's whole ground at street detail, a
// flight's at the detail of a country.
func affordableZoom(region slice.Bounds, z uint8) uint8 {
	for z > 0 {
		if n, err := slice.CellCount(region, z); err == nil && n <= maxMeasuredTiles {
			break
		}
		z--
	}
	return z
}

// flightOfferFrames is the most frames FlightAreas looks through: the
// cameras of neighbouring frames see nearly the same ground, and one in so
// many of a long flight is enough to know which.
const flightOfferFrames = 300

// FlightAreas is the ground a flight's cameras see and the detail they see
// it at, as the areas a fetch of the map has to cover: what the frames of
// the flight would be drawn from, worked out as their pictures would draw
// them, over level ground -- the elevation may be fetched only after this
// is asked -- in pictures of o's size. lat is the middle of the flight, which
// fixes the lattices the ground is laid on, as Local.Flight's does.
//
// Each tile of the pyramid a frame is drawn from is an area: its ground,
// with an eighth of a tile more each side for the strokes and the shading
// that reach into it from beyond, at the map zoom it is drawn from -- one
// deeper than its own, a pyramid tile being 512 pixels.
func FlightAreas(ctx context.Context, cams []perspective.Camera, o perspective.Options, lat float64, progress func(done, total int)) ([]acquire.Area, error) {
	if len(cams) == 0 {
		return nil, nil
	}
	f := &Flight{lat: lat, heights: perspective.Level, leveled: true, lattices: map[int]perspective.Lattice{}}
	step := max(1, len(cams)/flightOfferFrames)
	var frames []int
	for i := 0; i < len(cams); i += step {
		frames = append(frames, i)
	}
	if frames[len(frames)-1] != len(cams)-1 {
		frames = append(frames, len(cams)-1)
	}
	seen, err := perspective.TilesSeen(ctx, frames, func(i int) (perspective.Scene, perspective.Camera, perspective.Options, error) {
		s, err := f.scene(cams[i], o)
		return s, cams[i], o, err
	}, flightMinZoom, flightMaxZoom, progress)
	if err != nil {
		return nil, fmt.Errorf("tilemap: working out the ground a flight sees: %w", err)
	}
	areas := make([]acquire.Area, len(seen))
	for i, t := range seen {
		n := math.Exp2(float64(t.Z))
		west, east := float64(t.X)/n*360-180, float64(t.X+1)/n*360-180
		north := math.Atan(math.Sinh(math.Pi*(1-2*float64(t.Y)/n))) * 180 / math.Pi
		south := math.Atan(math.Sinh(math.Pi*(1-2*float64(t.Y+1)/n))) * 180 / math.Pi
		dx, dy := (east-west)/8, (north-south)/8
		areas[i] = acquire.Area{
			Bounds:  slice.Bounds{West: math.Max(-180, west-dx), East: math.Min(180, east+dx), South: math.Max(-85, south-dy), North: math.Min(85, north+dy)},
			MaxZoom: t.Z + 1,
		}
	}
	return areas, nil
}
