package tilemap

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/osmbase/terrain"
)

// terrainFixtureStore builds a terrain store beside a map store at mapRoot,
// whose elevation answers every tile around lat,lon with the same synthetic
// Terrarium tile: ground rising steeply to the east, so every view is a
// slope to shade and to draw contours on.
func terrainFixtureStore(t *testing.T, mapRoot string, lat, lon float64) *terrain.Store {
	t.Helper()
	const n = 64
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			h := 32768 + 200*x
			img.SetNRGBA(x, y, color.NRGBA{uint8(h >> 8), uint8(h), 0, 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	root := terrain.Root(mapRoot)
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	src, err := st.AddSource(slice.SourceDesc{
		Source: "synthetic-terrain", TileType: "png", TileCompression: slice.CompressionNone,
		SourceZoom: slice.ZoomRange{Min: 0, Max: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	cells, err := st.CellsFor(slice.Bounds{West: lon - 0.01, South: lat - 0.01, East: lon + 0.01, North: lat + 0.01})
	if err != nil {
		t.Fatal(err)
	}
	z := st.CellZoom()
	for _, c := range cells {
		if _, err := src.Fill(context.Background(), oneTileArchive{b.Bytes()}, c, slice.ZoomRange{Min: z, Max: z}); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := terrain.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// With terrain, the map is shaded and has contours, and once a view has
// been drawn the credit names the elevation after the map's and the full
// notice is there to hand over. Without terrain nothing of the kind is
// claimed, and the pixels are not the same.
func TestLocal_TerrainShadesCreditsAndHandsOverTheNotice(t *testing.T) {
	root := localFixtureStore(t, fixtureLat, fixtureLon, fixtureCredit)
	ts := terrainFixtureStore(t, root, fixtureLat, fixtureLon)
	v := fixtureView(fixtureLat, fixtureLon)

	plain, err := OpenLocal(root, darkInks(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := plain.Image(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Attribution() != fixtureCredit || len(plain.TerrainNotices()) != 0 {
		t.Errorf("without terrain: credit %q, notices %q", plain.Attribution(), plain.TerrainNotices())
	}

	shaded, err := OpenLocal(root, darkInks(), nil, &Terrain{Store: ts, Contours: true})
	if err != nil {
		t.Fatal(err)
	}
	if shaded.Attribution() != fixtureCredit {
		t.Errorf("before any view was drawn, the credit already names the elevation: %q", shaded.Attribution())
	}
	hilly, err := shaded.Image(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	covered, interval := shaded.TerrainCoverage()
	if covered != 1 || interval == 0 {
		t.Errorf("terrain coverage %v, contours every %v m; want all of it, with contours", covered, interval)
	}
	if got := shaded.Attribution(); !strings.HasPrefix(got, fixtureCredit) || !strings.Contains(got, "Elevation: synthetic-terrain") {
		t.Errorf("credit %q, want the map's and then the elevation's", got)
	}
	if ns := shaded.TerrainNotices(); len(ns) != 1 || !strings.Contains(ns[0], "synthetic-terrain") {
		t.Errorf("notices %q", ns)
	}
	if samePixels(flat, hilly) {
		t.Error("the shaded map is the same picture as the plain one")
	}

	lines, err := OpenLocal(root, darkInks(), nil, &Terrain{Store: ts, Contours: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lines.Image(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if covered, interval := lines.TerrainCoverage(); covered != 1 || interval != 0 {
		t.Errorf("Contours false: coverage %v, contours every %v m; want the shading alone", covered, interval)
	}
}

func samePixels(a, b image.Image) bool {
	ra, rb := image.NewRGBA(a.Bounds()), image.NewRGBA(b.Bounds())
	draw.Draw(ra, ra.Bounds(), a, a.Bounds().Min, draw.Src)
	draw.Draw(rb, rb.Bounds(), b, b.Bounds().Min, draw.Src)
	return bytes.Equal(ra.Pix, rb.Pix)
}

// The derived palette's shade and highlight are greys at the band's two
// ends, and the map they shade still carries the theme's inks: osmbase's
// check measures every shaded surface at its extremes.
func TestMapInks_ReliefInksAreTheBandsEnds(t *testing.T) {
	for name, inks := range map[string]MapInks{"dark": darkInks(), "light": lightInks(), "high-contrast dark": highContrastDarkInks(), "high-contrast light": highContrastLightInks()} {
		p, err := inks.localPalette()
		if err != nil {
			t.Fatal(err)
		}
		if p.Shade == (color.RGBA{}) || p.Highlight == (color.RGBA{}) || p.Contour == (color.RGBA{}) {
			t.Fatalf("%s: shade %v, highlight %v, contour %v", name, p.Shade, p.Highlight, p.Contour)
		}
		if luminance(p.Shade) >= luminance(p.Highlight) {
			t.Errorf("%s: the shade %v is no darker than the highlight %v", name, p.Shade, p.Highlight)
		}
		if err := inks.CheckContrast(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
