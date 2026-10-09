package cmd

import (
	"context"
	"fmt"
	"image/color"
	"math"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/perspective"
	"github.com/wisborg/osmbase/slice"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// resolveRoute3D reads --route-view and the camera flags: nil for the flat
// route, the default. 3d needs the local basemap -- the map is drawn here,
// from the ground's heights, which a tile service's pictures do not carry --
// and turns --terrain on unless it was turned off by name, in which case the
// map is draped over level ground and the summary says so.
//
// Resolved before the basemap, because it changes what the basemap offers to
// fetch: see offerBounds.
func resolveRoute3D(cmd *cobra.Command) (*panel.Route3D, error) {
	switch renderOpts.routeView {
	case "flat":
		for _, name := range []string{"route-heading", "route-pitch", "route-exaggeration", "route-distance", "route-camera-smoothing"} {
			if cmd.Flags().Changed(name) {
				fmt.Fprintf(cmd.ErrOrStderr(), "--%s is only read by --route-view 3d or flyover; the route is drawn flat\n", name)
				break
			}
		}
		return nil, nil
	case "3d":
		for _, name := range []string{"route-distance", "route-camera-smoothing"} {
			if cmd.Flags().Changed(name) {
				fmt.Fprintf(cmd.ErrOrStderr(), "--%s is only read by --route-view flyover; a 3d route is one picture\n", name)
				break
			}
		}
	case "flyover":
	default:
		return nil, fmt.Errorf("render: --route-view %q is invalid; use flat, 3d or flyover", renderOpts.routeView)
	}
	if renderOpts.basemap != tilemap.LocalProvider {
		return nil, fmt.Errorf("render: --route-view %s draws the map here, shaped by the elevation beside your map store, so it needs --basemap %s", renderOpts.routeView, tilemap.LocalProvider)
	}
	cfg, err := route3DConfig()
	if err != nil {
		return nil, fmt.Errorf("render: --route-heading %q is invalid; give a compass bearing in degrees, or auto", renderOpts.routeHeading)
	}
	r := &cfg
	// Level with the ground, the camera sees the route edge on; past
	// straight down, it is looking back over its own shoulder.
	if !(r.Pitch >= 10 && r.Pitch <= 90) {
		return nil, fmt.Errorf("render: --route-pitch %g is invalid; give degrees below the horizontal, from 10 to 90", r.Pitch)
	}
	if !(r.Exaggeration > 0 && r.Exaggeration <= 10) {
		return nil, fmt.Errorf("render: --route-exaggeration %g is invalid; give how many times their height hills are drawn, more than 0 and at most 10", r.Exaggeration)
	}
	if r.Flyover {
		if r.Distance != 0 && !(r.Distance >= 100 && r.Distance <= 500_000) {
			return nil, fmt.Errorf("render: --route-distance %q is invalid; give how far behind the activity the camera follows, in metres, from 100 to 500000, auto or steady", renderOpts.routeDistance)
		}
		if r.Rise < 0 {
			return nil, fmt.Errorf("render: --route-camera-smoothing %s is invalid; give 0 or a positive duration", r.Rise)
		}
	}
	if !cmd.Flags().Changed("terrain") {
		renderOpts.terrain = true
	}
	// A flyover leaves contour lines out unless asked for by name: seen
	// from a few hundred metres up, the lines of one hillside are drawn
	// from a different zoom's tiles a few seconds later, at a different
	// interval, and the ground's shading already says where the hills are.
	if r.Flyover && !cmd.Flags().Changed("contours") {
		renderOpts.contours = false
	}
	return r, nil
}

// route3DAspects are the shapes of route panel the 3d offer covers: the
// layout gives the panel its box only after the offer is made, so the
// ground asked for is what the camera sees at any of these, from a tall
// column to a wide band, together.
var route3DAspects = []float64{0.25, 0.5, 1, 2, 4}

// offerBounds is the ground the map and elevation offers ask about: the
// activity's own, padded by padKM, for a flat route; for a 3d one, that and
// every piece of ground its camera can see, which reaches far beyond the
// route into the haze.
//
// The camera is the panel's own, from panel.Route3D.Camera, so the ground
// offered is the ground drawn.
func offerBounds(track *fitactivity.Track, padKM float64) (slice.Bounds, error) {
	if renderOpts.routeView == "flyover" {
		b, err := activityBounds(track, padKM)
		if err != nil {
			return b, err
		}
		if flyoverGround == nil {
			return b, nil
		}
		return union(b, *flyoverGround), nil
	}
	if renderOpts.routeView != "3d" {
		return activityBounds(track, padKM)
	}
	b, err := activityBounds(track, padKM)
	if err != nil {
		return b, err
	}
	cfg, err := route3DConfig()
	if err != nil {
		return b, err
	}
	pts := route.FromTrack(track, 400)
	for _, aspect := range route3DAspects {
		cam, err := cfg.Camera(pts, aspect)
		if err != nil {
			return b, err
		}
		a := tilemap.DrapeArea(cam, aspect)
		b.West, b.South = math.Min(b.West, a.West), math.Min(b.South, a.South)
		b.East, b.North = math.Max(b.East, a.East), math.Max(b.North, a.North)
	}
	return b, nil
}

// route3DConfig is the camera the flags describe, for resolveRoute3D to
// check and for an offer to frame with -- one reading of the flags, so the
// ground offered and the ground drawn come from the same camera.
func route3DConfig() (panel.Route3D, error) {
	r := panel.Route3D{Pitch: renderOpts.routePitch, Exaggeration: renderOpts.routeExaggeration, AutoHeading: renderOpts.routeHeading == "auto"}
	if renderOpts.routeView == "flyover" {
		r.Flyover, r.Rise = true, renderOpts.routeSmoothing
		// auto is 0, chosen by the flight; anything unreadable is NaN, for
		// resolveRoute3D to refuse in the distance's own words.
		switch renderOpts.routeDistance {
		case "auto":
		case "steady":
			r.SteadyDistance = true
		default:
			d, err := strconv.ParseFloat(renderOpts.routeDistance, 64)
			if err != nil {
				d = math.NaN()
			}
			r.Distance = d
		}
	}
	if !r.AutoHeading {
		h, err := strconv.ParseFloat(renderOpts.routeHeading, 64)
		if err != nil || math.IsNaN(h) || math.IsInf(h, 0) {
			return r, fmt.Errorf("%q is not a bearing", renderOpts.routeHeading)
		}
		r.Heading = math.Mod(math.Mod(h, 360)+360, 360)
	}
	return r, nil
}

// flyoverGround is the ground a flyover sees, worked out by flightOffer once
// the render's timeline exists, for offerBounds: the offers are made while
// the basemap is opened, and they ask about all of it. flyoverAreas is the
// same ground as the areas a fetch covers, each at the detail it is seen
// at; the offers under --route-view flyover ask about those.
var (
	flyoverGround *slice.Bounds
	flyoverAreas  []acquire.Area
)

// flightOffer is the ground the flight r describes sees, over every shape
// of panel the layout might give it -- planned by the panel's own
// Route3D.Flight, so the ground offered is the ground drawn -- as the box
// round it and as the areas a fetch of it covers, each at the detail it is
// seen at. A panel of each shape is taken as large as the frame of w by h
// pixels holds, the detail a camera needs growing with its picture.
func flightOffer(ctx context.Context, r panel.Route3D, track *fitactivity.Track, tl panel.Timeline, w, h int) (slice.Bounds, []acquire.Area, error) {
	fixes := route.FromTrack(track, len(track.Samples)+1)
	drawn := route.FromTrack(track, 400)
	var b slice.Bounds
	var areas []acquire.Area
	for i, aspect := range route3DAspects {
		cams, err := r.Flight(fixes, drawn, tl, aspect, nil)
		if err != nil {
			return b, nil, err
		}
		g := panel.FlightGround(cams, aspect)
		if i == 0 {
			b = g
		} else {
			b = union(b, g)
		}
		pw, ph := float64(w), float64(w)/aspect
		if ph > float64(h) {
			pw, ph = float64(h)*aspect, float64(h)
		}
		o := perspective.Options{Width: max(1, int(pw)), Height: max(1, int(ph))}
		a, err := tilemap.FlightAreas(ctx, cams, o, (g.South+g.North)/2)
		if err != nil {
			return b, nil, err
		}
		areas = append(areas, a...)
	}
	return b, areas, nil
}

// union is the box round a and b.
func union(a, b slice.Bounds) slice.Bounds {
	return slice.Bounds{West: math.Min(a.West, b.West), South: math.Min(a.South, b.South), East: math.Max(a.East, b.East), North: math.Max(a.North, b.North)}
}

// resolveBasemapPalette reads --basemap-palette and, for outdoors, turns the
// local map to it and returns the inks the route is to be drawn in over it;
// nil keeps the theme's. auto is outdoors under 3d and flyover, where the
// map is the picture, and theme for the flat map, whose quiet derived
// colours are what let a small route over it stand out.
func resolveBasemapPalette(cmd *cobra.Command, basemap tilemap.Provider, route3D *panel.Route3D) (*panel.RouteInks, error) {
	choice := renderOpts.basemapPalette
	switch choice {
	case "auto":
		choice = "theme"
		if route3D != nil {
			choice = "outdoors"
		}
	case "theme", "outdoors":
	default:
		return nil, fmt.Errorf("render: --basemap-palette %q is invalid; use auto, theme or outdoors", renderOpts.basemapPalette)
	}
	local, ok := basemap.(*tilemap.Local)
	if !ok {
		if cmd.Flags().Changed("basemap-palette") {
			fmt.Fprintf(cmd.ErrOrStderr(), "--basemap-palette is only read by --basemap %s, which draws the map here\n", tilemap.LocalProvider)
		}
		return nil, nil
	}
	if choice == "theme" {
		return nil, nil
	}
	if err := local.UseOutdoors(); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	fg, dim, accent, hl := tilemap.OutdoorsInks()
	return &panel.RouteInks{Foreground: fg, Dim: dim, Accent: accent, Highlight: hl}, nil
}

// rgbaOf is a theme colour as the opaque RGBA a picture's sky is drawn in.
func rgbaOf(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
}
