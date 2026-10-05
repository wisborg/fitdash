package cmd

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/tilemap"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// terrainSourceDir is a directory of terrain archives -- one zoom-0
// Terrarium tile of ground rising to the east -- which a fetch copies from
// without contacting anybody.
func terrainSourceDir(t *testing.T) string {
	t.Helper()
	const n = 64
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			h := 32768 + 400*x
			img.SetNRGBA(x, y, color.NRGBA{uint8(h >> 8), uint8(h), 0, 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:    []osmbasetest.ArchiveTile{{ID: 0, Data: b.Bytes()}},
		TileType: pmtiles.TileTypePNG, TileCompression: pmtiles.CompressionNone,
		MinZoom: 0, MaxZoom: 12, MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planet.pmtiles"), built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func terrainCommand(t *testing.T, flags map[string]string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{}
	bindRenderFlags(c)
	for name, value := range flags {
		if err := c.Flags().Set(name, value); err != nil {
			t.Fatalf("setting --%s: %v", name, err)
		}
	}
	var buf bytes.Buffer
	c.SetErr(&buf)
	c.SetIn(strings.NewReader(""))
	return c, &buf
}

// --terrain with the local basemap offers to fetch the elevation this
// activity's map lacks; with --yes it copies it into the store beside the
// map's, and the map drawn from it is shaded, credited, and has its notice
// for the summary to hand over.
func TestResolveBasemap_LocalTerrainIsOfferedFetchedAndDrawn(t *testing.T) {
	defer func(o renderOptions, f basemapFetchOptions) { renderOpts, basemapFetchOpts = o, f }(renderOpts, basemapFetchOpts)
	store := localBasemapFixtureStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.fit")
	opts := fittest.DefaultOptions()
	opts.Count = 600
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, _, err := decodeActivities([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	source := terrainSourceDir(t)

	basemapFetchOpts.yes = true
	c, stderr := terrainCommand(t, map[string]string{
		"basemap": tilemap.LocalProvider, "basemap-store": store, "terrain": "true", "terrain-source": source,
	})
	p, err := resolveBasemap(c, panel.DarkTheme(), track, nil)
	if err != nil {
		t.Fatalf("resolveBasemap: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr.String(), "copied from "+source) || !strings.Contains(stderr.String(), "elevation tiles") {
		t.Errorf("the offer and the fetch:\n%s", stderr)
	}
	if _, err := os.Stat(store + "-terrain"); err != nil {
		t.Errorf("no terrain store beside the map's: %v", err)
	}
	local := p.(*tilemap.Local)
	b, err := activityBounds(track, autoFetchPadKM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Image(context.Background(), tilemap.View{West: b.West, South: b.South, East: b.East, North: b.North, Width: 480, Height: 320}); err != nil {
		t.Fatal(err)
	}
	var summary bytes.Buffer
	writeTerrainSummary(&summary, local)
	if !strings.Contains(summary.String(), "terrain: 100% of the map shaded") || !strings.Contains(summary.String(), "give this notice with it:\n  Elevation: ") {
		t.Errorf("the summary:\n%s", summary.String())
	}
	if !strings.Contains(local.Attribution(), "Elevation: ") {
		t.Errorf("the frame's credit does not name the elevation: %q", local.Attribution())
	}
}

// With nobody to answer, nothing is fetched and the map is drawn unshaded,
// said; and terrain flags given to a render that cannot shade are said to
// be unused rather than ignored.
func TestResolveBasemap_TerrainDeclinedOrUnusedIsSaid(t *testing.T) {
	defer func(o renderOptions, f basemapFetchOptions) { renderOpts, basemapFetchOpts = o, f }(renderOpts, basemapFetchOpts)
	store := localBasemapFixtureStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.fit")
	opts := fittest.DefaultOptions()
	opts.Count = 600
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, _, err := decodeActivities([]string{path})
	if err != nil {
		t.Fatal(err)
	}

	basemapFetchOpts.yes = false
	c, stderr := terrainCommand(t, map[string]string{
		"basemap": tilemap.LocalProvider, "basemap-store": store, "terrain": "true", "terrain-source": terrainSourceDir(t),
	})
	c.SetIn(os.Stdin) // not a terminal under go test: nobody to answer
	p, err := resolveBasemap(c, panel.DarkTheme(), track, nil)
	if err != nil {
		t.Fatalf("resolveBasemap: %v", err)
	}
	if _, ok := p.(*tilemap.Local); !ok {
		t.Fatalf("a declined terrain fetch cost the map: %T", p)
	}
	if !strings.Contains(stderr.String(), "no elevation was fetched") || !strings.Contains(stderr.String(), "drawn unshaded") {
		t.Errorf("declined:\n%s", stderr)
	}
	if _, err := os.Stat(store + "-terrain"); !os.IsNotExist(err) {
		t.Errorf("a terrain store was created with nobody's consent: %v", err)
	}

	c, stderr = terrainCommand(t, map[string]string{"terrain": "true"})
	if p, err := resolveBasemap(c, panel.DarkTheme(), track, nil); err != nil || p != nil {
		t.Fatalf("no basemap: %v, %v", p, err)
	}
	if !strings.Contains(stderr.String(), "--terrain is only read by --basemap local") {
		t.Errorf("--terrain without the local basemap:\n%s", stderr)
	}
}
