package cmd

import (
	"fmt"
	"image/color"
	"math"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"
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
		for _, name := range []string{"route-heading", "route-pitch", "route-exaggeration"} {
			if cmd.Flags().Changed(name) {
				fmt.Fprintf(cmd.ErrOrStderr(), "--%s is only read by --route-view 3d; the route is drawn flat\n", name)
				break
			}
		}
		return nil, nil
	case "3d":
	default:
		return nil, fmt.Errorf("render: --route-view %q is invalid; use flat or 3d", renderOpts.routeView)
	}
	if renderOpts.basemap != tilemap.LocalProvider {
		return nil, fmt.Errorf("render: --route-view 3d draws the map here, shaped by the elevation beside your map store, so it needs --basemap %s", tilemap.LocalProvider)
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
	if !cmd.Flags().Changed("terrain") {
		renderOpts.terrain = true
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
	if !r.AutoHeading {
		h, err := strconv.ParseFloat(renderOpts.routeHeading, 64)
		if err != nil || math.IsNaN(h) || math.IsInf(h, 0) {
			return r, fmt.Errorf("%q is not a bearing", renderOpts.routeHeading)
		}
		r.Heading = math.Mod(math.Mod(h, 360)+360, 360)
	}
	return r, nil
}

// rgbaOf is a theme colour as the opaque RGBA a picture's sky is drawn in.
func rgbaOf(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
}
