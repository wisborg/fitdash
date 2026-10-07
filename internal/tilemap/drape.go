package tilemap

import (
	"context"
	"fmt"
	"math"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// Drape draws the map in perspective, from cam, at o's size and in its
// colours of sky and haze:
// the ground the camera sees out into the haze, drawn from the store as
// Image draws a view, shaded and shaped by the elevation, with the names of
// places stood upright on the picture rather than lying on the slopes.
//
// The Picture comes back rather than only its image, because what is drawn
// over it -- a route, a moving dot -- has to be placed through the same
// camera, and hidden where the ground hides it; Picture.Locate is how.
//
// Without elevation the map is draped over level ground: the map tilted,
// which is honest about what is known, rather than nothing at all. Leveled
// says which the caller got, so the summary can say the ground is level for
// want of heights rather than letting it pass for flat country.
//
// Like Image it reaches no network, and it records the drawn view's
// coverage and what it owes the elevation, so Coverage, Overzoomed,
// Attribution and the terrain notices account for it.
func (l *Local) Drape(ctx context.Context, cam perspective.Camera, o perspective.Options, exaggeration float64) (pic *perspective.Picture, leveled bool, err error) {
	if o.Width <= 0 || o.Height <= 0 {
		return nil, false, fmt.Errorf("tilemap: a %d by %d picture has no pixels", o.Width, o.Height)
	}
	v := cam.MapView(cam.MapBounds(float64(o.Width)/float64(o.Height)), o.Height, 0)
	b := slice.Bounds{West: v.Bounds.West, South: v.Bounds.South, East: v.Bounds.East, North: v.Bounds.North}
	if cells, err := l.store.CellsFor(b); err == nil {
		defer l.source.Hold(cells).Release()
	}
	rend, err := l.rendererFor(View{North: b.North, West: b.West, South: b.South, East: b.East, Width: v.Width, Height: v.Height}, &cam.Heading)
	if err != nil {
		return nil, false, err
	}
	res, err := rend.Render(ctx, v)
	if err != nil {
		return nil, false, fmt.Errorf("tilemap: drawing a %dx%d view from the local map store %s: %w", v.Width, v.Height, l.root, err)
	}
	l.record(res)

	var heights osm.HeightSource = perspective.Level
	leveled = true
	if l.terrain != nil && res.TerrainCovered > 0 {
		heights, leveled = l.terrain.Heights(), false
	}
	pic, err = perspective.Render(
		perspective.Scene{Map: res.Image, View: v, Heights: heights, Exaggeration: exaggeration, Step: perspective.StepFor(v)},
		cam, o,
	)
	if err != nil {
		return nil, false, fmt.Errorf("tilemap: drawing the map in perspective: %w", err)
	}
	pic.DrawPlaceNames(res.PointLabels, l.opts.Palette)
	return pic, leveled, nil
}

// DrapeArea is the ground Drape draws for cam in a picture of the given
// aspect: what a fetch for a perspective view has to cover, which is far
// more than the route -- the camera sees out to the horizon's haze.
func DrapeArea(cam perspective.Camera, aspect float64) slice.Bounds {
	b := cam.MapBounds(aspect)
	return slice.Bounds{
		West: math.Max(-180, b.West), South: math.Max(-85, b.South),
		East: math.Min(180, b.East), North: math.Min(85, b.North),
	}
}
