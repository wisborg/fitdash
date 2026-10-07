package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// --route-view is flat unless asked, and the camera flags given to a flat
// render are said to be unused; 3d needs the local basemap, turns terrain on
// unless it was turned off by name, and refuses a camera that could not see
// the route.
func TestResolveRoute3D(t *testing.T) {
	defer func(o renderOptions) { renderOpts = o }(renderOpts)

	c, stderr := terrainCommand(t, map[string]string{"route-pitch": "50"})
	if r, err := resolveRoute3D(c); err != nil || r != nil {
		t.Errorf("flat: %+v, %v", r, err)
	}
	if !strings.Contains(stderr.String(), "--route-pitch is only read by --route-view 3d") {
		t.Errorf("a camera flag on a flat render was not said to be unused:\n%s", stderr)
	}

	c, _ = terrainCommand(t, map[string]string{"route-view": "3d", "basemap": "outdoors"})
	if _, err := resolveRoute3D(c); err == nil || !strings.Contains(err.Error(), "--basemap "+tilemap.LocalProvider) {
		t.Errorf("3d over a tile service: %v", err)
	}

	c, _ = terrainCommand(t, map[string]string{"route-view": "3d", "basemap": tilemap.LocalProvider, "route-heading": "-90"})
	r, err := resolveRoute3D(c)
	if err != nil || r == nil || r.AutoHeading || r.Heading != 270 || r.Pitch != 35 || !renderOpts.terrain {
		t.Errorf("3d, heading -90: %+v, %v, terrain %v", r, err, renderOpts.terrain)
	}

	c, _ = terrainCommand(t, map[string]string{"route-view": "3d", "basemap": tilemap.LocalProvider, "terrain": "false"})
	if r, err := resolveRoute3D(c); err != nil || r == nil || renderOpts.terrain {
		t.Errorf("3d with --terrain=false: %+v, %v, terrain %v", r, err, renderOpts.terrain)
	}

	for flag, value := range map[string]string{"route-pitch": "5", "route-heading": "north", "route-exaggeration": "0", "route-view": "tilted"} {
		c, _ := terrainCommand(t, map[string]string{"route-view": "3d", "basemap": tilemap.LocalProvider, flag: value})
		if _, err := resolveRoute3D(c); err == nil || !strings.Contains(err.Error(), "--"+flag) {
			t.Errorf("--%s %s: %v", flag, value, err)
		}
	}
}

// The 3d offer is for the ground the camera sees, which takes in the route
// and everything the panel will drape for a box of any shape; and it is
// what the panel drapes: the map
// drawn in 3d from a store filled for that ground, with its terrain, is
// shaded, not level.
func TestRoute3D_OffersAndDrapesTheGroundTheCameraSees(t *testing.T) {
	defer func(o renderOptions, f basemapFetchOptions) { renderOpts, basemapFetchOpts = o, f }(renderOpts, basemapFetchOpts)
	store := localBasemapFixtureStore(t)
	path := filepath.Join(t.TempDir(), "activity.fit")
	opts := fittest.DefaultOptions()
	opts.Count = 600
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, _, err := decodeActivities([]string{path})
	if err != nil {
		t.Fatal(err)
	}

	basemapFetchOpts.yes = true
	c, stderr := terrainCommand(t, map[string]string{
		"basemap": tilemap.LocalProvider, "basemap-store": store, "terrain-source": terrainSourceDir(t), "route-view": "3d",
	})
	r, err := resolveRoute3D(c)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := activityBounds(track, autoFetchPadKM)
	if err != nil {
		t.Fatal(err)
	}
	seen, err := offerBounds(track, autoFetchPadKM)
	if err != nil {
		t.Fatal(err)
	}
	if seen.West > flat.West || seen.East < flat.East || seen.South > flat.South || seen.North < flat.North {
		t.Errorf("the 3d area %+v does not take in the route's %+v", seen, flat)
	}
	// Whatever shape of box the layout gives the panel, the ground it drapes
	// is inside what was offered -- including shapes between the ones the
	// offer is worked out at.
	pts := route.FromTrack(track, 2000)
	for _, aspect := range []float64{0.4, 0.75, 16.0 / 9, 2.5, 4} {
		cam, err := r.Camera(pts, aspect)
		if err != nil {
			t.Fatal(err)
		}
		d := tilemap.DrapeArea(cam, aspect)
		const slack = 1e-3 // degrees; the offer is of cells, far coarser
		if d.West < seen.West-slack || d.East > seen.East+slack || d.South < seen.South-slack || d.North > seen.North+slack {
			t.Errorf("a box of aspect %.2f drapes %+v, outside the %+v offered", aspect, d, seen)
		}
	}

	p, err := resolveBasemap(c, panel.DarkTheme(), track, nil)
	if err != nil {
		t.Fatalf("resolveBasemap: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr.String(), "elevation tiles") {
		t.Errorf("3d did not offer the elevation:\n%s", stderr)
	}
	ctx := &panel.Context{Track: track, Basemap: p, Route3D: r}
	painter := panel.RoutePanel{}.Prepare(ctx, panel.Box{W: 480, H: 270})
	note := painter.(panel.BasemapReporter).BasemapNote()
	if !strings.Contains(note, "drawn in 3d") || strings.Contains(note, "level") {
		t.Errorf("the 3d route's note: %q", note)
	}
}
