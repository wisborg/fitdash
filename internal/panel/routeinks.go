package panel

import "image/color"

// RouteInks are the colours the route is drawn in over its map: the line
// still ahead, the line covered, a highlighted stretch, and the dot.
//
// By default they are the theme's, and the local map's colours are derived
// from the theme to suit them. A map that brings colours of its own -- the
// outdoors palette, under the 3d and flyover views -- was drawn against inks
// of its own, chosen and checked for contrast with it, and the route is drawn
// in those: a theme's white line on its pale greens, or its black one on its
// shaded hills, is not guaranteed to read. Over no map at all the theme's
// inks are used whatever is set, because the map's are chosen against the
// map and may vanish into the dashboard's background.
type RouteInks struct {
	Foreground, Dim, Accent, Highlight color.Color
}

// routeInks are the inks to draw a route in: o's when set and the route is
// over its map, the theme's otherwise.
func routeInks(o *RouteInks, overMap bool, t Theme) RouteInks {
	if o != nil && overMap {
		return *o
	}
	return RouteInks{Foreground: t.Foreground, Dim: t.Dim, Accent: t.Accent, Highlight: t.Highlight}
}
