package panel

import (
	"image/color"
	"testing"
)

// A map's own inks are used over that map; over no map, the theme's,
// whatever is set -- the map's were chosen against the map and may vanish
// into the dashboard.
func TestRouteInks_TheMapsOverItTheThemesElsewhere(t *testing.T) {
	theme := DefaultTheme()
	navy := color.RGBA{R: 0x14, G: 0x1a, B: 0x33, A: 0xff}
	own := &RouteInks{Foreground: navy, Dim: navy, Accent: navy, Highlight: navy}
	if got := routeInks(own, true, theme); got.Foreground != navy {
		t.Errorf("over its map the route is drawn in %v, not the map's own ink", got.Foreground)
	}
	if got := routeInks(own, false, theme); got.Foreground != theme.Foreground || got.Accent != theme.Accent {
		t.Errorf("over no map the route is drawn in %v, not the theme's", got.Foreground)
	}
	if got := routeInks(nil, true, theme); got.Dim != theme.Dim {
		t.Errorf("with no inks of its own the map's route is drawn in %v, not the theme's", got.Dim)
	}
}
