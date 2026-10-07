package tilemap

import (
	"testing"

	osm "github.com/wisborg/osmbase/render"
)

// Turned to the outdoors palette, the map draws in it, says so, keeps the
// render's choice to leave contours out -- and drops the theme's own
// omissions, which under night are every filled surface but water: carried
// over, they drew the land as bare paper.
func TestUseOutdoors_DrawsInItKeepingOnlyTheContourChoice(t *testing.T) {
	root := localFixtureStore(t, 55.68, 12.57, "© OpenStreetMap contributors")
	l, err := OpenLocal(root, darkInks(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Outdoors() {
		t.Fatal("a map opened from a theme says it is outdoors")
	}
	l.opts.Palette.Omitted = osm.Roles(osm.RoleGreen, osm.RoleBuilt, osm.RoleContour)
	if err := l.UseOutdoors(); err != nil {
		t.Fatal(err)
	}
	if !l.Outdoors() {
		t.Error("turned to outdoors, the map does not say so")
	}
	want := osm.OutdoorsPalette()
	if got := l.opts.Palette; got.Land != want.Land || got.Green != want.Green || got.Water != want.Water {
		t.Errorf("the palette is %+v, not outdoors", got)
	}
	if o := l.opts.Palette.Omitted; !o.Has(osm.RoleContour) || o.Has(osm.RoleGreen) || o.Has(osm.RoleBuilt) {
		t.Errorf("omitted roles %b: want contours left out, as the render chose, and the theme's own omissions dropped", o)
	}
}
