package tilemap

import (
	"context"
	"fmt"
	"image"
	"sync"

	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// LocalProvider is the name this backend goes by on the command line and in
// the render summary.
const LocalProvider = "local"

// DefaultStoreDir is where map data lives when the caller names no
// directory, and it is deliberately NOT under fitdash's own cache the way
// DefaultCacheDir is.
//
// A slice is data the user acquired once and can render from forever; two
// programs on one machine should share it rather than download a city each.
// That sharing is osmbase's own decision and this is its answer, asked for
// rather than spelled out again here -- a second spelling would be a second
// directory the moment either moved.
//
// An error rather than DefaultCacheDir's empty string, and the difference is
// what each absence means: imagery with nowhere to cache is simply not
// cached, while map data with nowhere to look for it is a basemap that cannot
// be drawn at all. The caller reports it rather than guessing a path, because
// a default store in the wrong place is a download the user makes twice.
func DefaultStoreDir() (string, error) {
	dir, err := slice.DefaultRoot()
	if err != nil {
		return "", fmt.Errorf("tilemap: finding the default map store: %w", err)
	}
	return dir, nil
}

// Local draws the basemap from a local osmbase store instead of fetching it.
//
// It is a Provider like any other -- degrees and pixels in, an image out --
// and that is the entire point: nothing above this interface knows or needs
// to know that these pixels were drawn here from vector tiles on the user's
// own disk rather than requested from a service. What it does NOT do is
// reach the network; there is nothing in this type's vocabulary to reach it
// with, because osmbase's store cannot fetch and its renderer cannot either.
//
// It must never be wrapped in Cached. See OpenLocal.
type Local struct {
	rend   *osm.Renderer
	store  *slice.Store
	source *slice.Source
	root   string
	id     string
	credit string

	mu sync.Mutex
	// covered is the smallest covered fraction any view came back with, and
	// 1 when nothing has been drawn yet. A render asks for several views and
	// the honest thing to report is the worst of them, not the last.
	covered float64
	// overzoomed is the largest overzoomed fraction any view came back with,
	// and 0 when nothing has been drawn yet. The opposite direction to
	// covered above: this one is bad when it is high.
	overzoomed float64
}

// OpenLocal opens the store at root and returns a Provider that draws from
// it, in colours derived from inks.
//
// # Never wrap this in Cached
//
// Cached exists because a third-party service is slow, rate-limited and
// costs somebody's quota. None of that is true here, and wrapping would do
// three separate kinds of harm: it would add a cache keyed on the view AND
// its pixel size on top of a tile store deliberately keyed on neither, so
// changing the output resolution would miss; it would store megabytes of PNG
// for a picture regenerable in milliseconds from bytes already on disk; and
// it would apply a thirty-day expiry to a slice the user downloaded on
// purpose, after which the entry expires into a fetch this backend cannot
// perform. The tile store is the cache, at the layer that can be shared
// between programs and between resolutions.
//
// # Why a store with no credit is refused
//
// A rendered map is a Produced Work under the ODbL and the credit has to
// appear wherever it is shown. The string is data -- it is written into the
// manifest at acquisition, from whatever source the bytes came from -- so a
// store that records none leaves fitdash with an obligation it cannot state.
// Inventing one here would be the exact failure the manifest field exists to
// prevent: a credit spelled into the program goes on being printed after
// somebody changes archives.
//
// # Why the credit is converted here
//
// The manifest's string is written as HTML, because the archives it is copied
// from were authored for web maps -- and the panel that draws it has nowhere
// to put a link, so it would burn the markup into every frame. Converting it
// at the point the manifest is READ, rather than at the point it is drawn,
// keeps one plain string in the field, in Attribution(), in the summary and
// in the frame: a conversion at the drawing end would leave the same string
// meaning two different things depending on who asked for it. See
// render.PlainCredit in osmbase, which both this and osmbase's own render
// command call so the obligation is discharged one way.
func OpenLocal(root string, inks MapInks) (*Local, error) {
	store, err := slice.Open(root)
	if err != nil {
		// Opening, never creating. A render cannot fill a store -- filling is
		// a fetch and this backend has no way to make one -- so bringing an
		// empty one into being here would leave the user with a directory
		// that explains nothing and a map that never arrives.
		return nil, fmt.Errorf("tilemap: opening the local map store: %w; --basemap %s draws only from map data already downloaded into a store, and none is there", err, LocalProvider)
	}
	m, err := newestSource(store, root)
	if err != nil {
		return nil, err
	}
	// The refusal is tested against the CONVERTED string rather than the raw
	// one, because what the frame can carry is what matters: a manifest whose
	// attribution is whitespace credits nobody however it was spelled.
	// PlainCredit keeps anything it cannot make sense of, so this is empty
	// only when there was nothing there.
	credit := osm.PlainCredit(m.Attribution)
	if credit == "" {
		return nil, fmt.Errorf("tilemap: the map data in %s records no attribution, so a render from it could not credit anyone; a rendered map is a Produced Work under its data licence, and fitdash will not draw one it cannot credit", root)
	}
	src, err := store.Source(m.ID)
	if err != nil {
		return nil, fmt.Errorf("tilemap: opening source %s of the local map store: %w", m.ID, err)
	}
	// A theme whose inks leave no room for a map is refused here rather than
	// drawn. There is no degraded picture to fall back to: the collapse puts
	// every map role on the same colour, which renders as a solid rectangle
	// indistinguishable from no basemap at all -- while the summary would
	// still report one as drawn. See MapInks.band.
	palette, err := inks.localPalette()
	if err != nil {
		return nil, fmt.Errorf("tilemap: the theme cannot carry a local basemap: %w", err)
	}
	rend, err := osm.New(src, osm.Options{
		Style:       osm.BasemapStyle(),
		Palette:     palette,
		Attribution: credit,
	})
	if err != nil {
		return nil, fmt.Errorf("tilemap: preparing to draw from the local map store %s: %w", root, err)
	}
	return &Local{
		rend: rend, store: store, source: src,
		root: root, id: m.ID, credit: credit, covered: 1,
	}, nil
}

// newestSource picks which of the store's sources to draw from.
//
// A store holds one source per archive build, because a tile from two builds
// is two different pictures of the same ground -- so with more than one
// present the newest is the right answer rather than an arbitrary one, and
// the tie is broken on the ID so that two sources written in the same second
// still choose the same way every run. Updated is a timestamp recorded in the
// manifest, not a wall clock read during the render: the same store picks the
// same source forever.
func newestSource(store *slice.Store, root string) (slice.Manifest, error) {
	sources, err := store.Sources()
	if err != nil {
		return slice.Manifest{}, fmt.Errorf("tilemap: reading the local map store %s: %w", root, err)
	}
	if len(sources) == 0 {
		return slice.Manifest{}, fmt.Errorf("tilemap: the local map store %s holds no map data yet; fetch the area of the activity into it before rendering with --basemap %s", root, LocalProvider)
	}
	best := sources[0]
	for _, m := range sources[1:] {
		if m.Updated.After(best.Updated) {
			best = m
		}
	}
	return best, nil
}

// Image draws v from the store.
//
// The whole of the adapter: fitdash's view is osmbase's view, in the same
// degrees and the same pixels, because the two interfaces were shaped to the
// same question. An area the store holds nothing for comes back as an error
// and no image, which the caller already treats the way it treats a service
// that would not answer -- the route is drawn on plain background and the
// summary says so. An area it holds only PART of comes back as an image with
// the gaps hatched, which is a placeholder rather than a blank, and Coverage
// reports how much of it was real.
func (l *Local) Image(ctx context.Context, v View) (image.Image, error) {
	// The cells this view reads are held for the duration of the draw, which
	// also moves their eviction clock. Both halves matter: a cell evicted
	// mid-render would leave a hole in some frames and not others, and a
	// store whose cells were never marked as used would evict the ones the
	// user renders most. A failure to work out the cells is not a reason to
	// refuse to draw -- the hold is protection, not a precondition -- so it
	// falls through to the render.
	if cells, err := l.store.CellsFor(slice.Bounds{
		West: v.West, South: v.South, East: v.East, North: v.North,
	}); err == nil {
		defer l.source.Hold(cells).Release()
	}

	res, err := l.rend.Render(ctx, osm.View{
		Bounds: osm.Bounds{West: v.West, South: v.South, East: v.East, North: v.North},
		Width:  v.Width, Height: v.Height,
	})
	if err != nil {
		return nil, fmt.Errorf("tilemap: drawing a %dx%d view from the local map store %s: %w", v.Width, v.Height, l.root, err)
	}
	l.mu.Lock()
	if res.Covered < l.covered {
		l.covered = res.Covered
	}
	// The WORST of each, and they run in opposite directions: coverage is a
	// fraction that should be high, overzoom a fraction that should be low.
	// Keeping the extreme of each is what stops one good view in the middle
	// of a route reporting away a bad one at its start.
	if res.Overzoomed > l.overzoomed {
		l.overzoomed = res.Overzoomed
	}
	l.mu.Unlock()
	return res.Image, nil
}

// Coverage is the smallest fraction of any drawn view the store actually held
// tiles for, and 1 before anything has been drawn.
//
// It is a question about which tiles exist rather than a measurement of the
// image: a view over open water draws almost nothing and is not missing.
func (l *Local) Coverage() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.covered
}

// Overzoomed is the largest fraction of any drawn view that had to be
// stretched from a tile shallower than the view asked for, and 0 before
// anything has been drawn.
//
// It is reported separately from Coverage because it is the failure Coverage
// cannot see. A view with no tile at all is hatched and counts as uncovered;
// a view whose tiles exist only further up the pyramid is drawn completely,
// counts as fully covered, and comes out as a coloured shape with none of the
// detail the ground has. Without this, the summary's only honest-looking
// number says 100% over a smear.
func (l *Local) Overzoomed() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.overzoomed
}

// Attribution is the credit the store's own manifest records.
//
// Read from the data rather than written down here, which is what stops a
// change of source leaving the old credit on screen. See OpenLocal.
func (l *Local) Attribution() string { return l.credit }

// Name identifies this provider AND the source it draws from, in the shape
// Cached would key on if anyone ever wrapped it. Nothing should -- see
// OpenLocal -- but a Name that could not tell two sources apart would be a
// quiet way for that mistake to serve one build's tiles out of another's
// entry.
func (l *Local) Name() string { return LocalProvider + "/" + l.id }

// Root is the store this draws from, for the render summary.
func (l *Local) Root() string { return l.root }

// Fetched is false, always, and it is not a stub.
//
// A caller decides from this interface whether to tell the user that the area
// of their activity was sent to a third party. This backend reads tiles off
// the user's own disk and has no way to do anything else, so a provider that
// simply did not implement Reporter -- the shape a plain provider has -- would
// be read as having sent it, which is a false statement in the one line
// somebody reads to find out whether that happened.
func (l *Local) Fetched() bool { return false }

var (
	_ Provider = (*Local)(nil)
	_ Reporter = (*Local)(nil)
)

// Shortfall is what a store lacks for an area.
type Shortfall struct {
	// Empty is a store that is not there at all, or holds no map data yet.
	// Distinguished from a partial one because the sentence a user needs is
	// different: one of them is "you have not fetched anything", the other is
	// "you fetched somewhere else".
	Empty bool

	// Cells is how many of the store's fetch units the area touches and Held
	// how many of those hold a finished fetch. Both are zero for an empty
	// store, which has no cell zoom to count in.
	Cells, Held int

	// Overview is how many tiles ABOVE the cell zoom the area calls for, and
	// OverviewHeld how many are there.
	//
	// They are the difference between "there is nothing to draw" and "there
	// is something to draw, blurrily". A store holding the overview but not
	// the cells renders every pixel -- by overzooming a shallower tile, which
	// is sharp-looking and short on detail -- and reports full coverage while
	// doing it. Without this, a shortfall of "0 of 1 areas" would be told to
	// somebody whose map is about to come out fine, which is the way to teach
	// them to ignore the message.
	Overview, OverviewHeld int
}

// CanFallBack reports whether the store holds any tile the renderer could
// overzoom to fill the gaps.
//
// It is not a claim about quality. One tile at zoom 1 satisfies this, and a
// view drawn from it is a coloured shape rather than a map -- which is
// exactly why the distinction is worth reporting: the renderer will report
// full coverage either way, because coverage counts tiles drawn and not the
// detail in them. A user seeing "100% covered" over a smear needs the offer
// to have told them which it was going to be.
func (s Shortfall) CanFallBack() bool {
	return !s.Empty && (s.Held > 0 || s.OverviewHeld > 0)
}

// Complete reports whether the store already holds the whole area.
func (s Shortfall) Complete() bool { return !s.Empty && s.Cells > 0 && s.Held == s.Cells }

// StoreShortfall reports what the store at root lacks for b, WITHOUT touching
// the network.
//
// That constraint is the whole design of this function, not an optimisation.
// The question it answers -- should the user be offered a download? -- has to
// be settled before anything reaches a host, because planning a fetch is
// itself a request that tells that host which part of the map was asked
// about. A check that had to plan in order to decide whether to ask
// permission would have already done the thing it was asking permission for.
//
// Everything needed is on disk: the store records which cells hold a finished
// fetch, and the area is arithmetic. An unreadable store is reported as empty
// rather than as an error, because the remedy is the same -- fetch into it --
// and refusing a render over it would be worse than offering to fill it.
func StoreShortfall(root string, b slice.Bounds) (Shortfall, error) {
	store, err := slice.Open(root)
	if err != nil {
		return Shortfall{Empty: true}, nil
	}
	sources, err := store.Sources()
	if err != nil {
		return Shortfall{}, fmt.Errorf("tilemap: reading the local map store %s: %w", root, err)
	}
	if len(sources) == 0 {
		return Shortfall{Empty: true}, nil
	}
	m, err := newestSource(store, root)
	if err != nil {
		return Shortfall{}, err
	}
	src, err := store.Source(m.ID)
	if err != nil {
		return Shortfall{}, fmt.Errorf("tilemap: opening source %s of the local map store: %w", m.ID, err)
	}
	cov, err := src.Coverage(b)
	if err != nil {
		return Shortfall{}, fmt.Errorf("tilemap: measuring what %s holds for this activity: %w", root, err)
	}
	// Partial cells count as not held. A partial cell is one an interrupted
	// fetch left tiles in without finishing, so it draws with holes -- which
	// is the state this offer exists to get the user out of, not a state to
	// report as covered.
	return Shortfall{
		Cells: cov.Cells, Held: cov.Complete,
		Overview: cov.OverviewWanted, OverviewHeld: cov.OverviewHeld,
	}, nil
}
