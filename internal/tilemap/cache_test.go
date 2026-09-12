package tilemap

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingProvider records how often it was actually asked for imagery, which
// is the only thing a cache test really wants to know.
type countingProvider struct {
	calls int
	err   error
	name  string
}

func (p *countingProvider) Image(context.Context, View) (image.Image, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 9, G: 8, B: 7, A: 255})
	return img, nil
}
func (p *countingProvider) Attribution() string { return "© somebody" }
func (p *countingProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "fake/style"
}

// TestCached_SecondViewIsServedFromDisk is the workflow this exists for.
// Tuning a --highlight means rendering the same activity repeatedly, and
// without a cache every one of those runs re-fetches identical imagery.
func TestCached_SecondViewIsServedFromDisk(t *testing.T) {
	inner := &countingProvider{}
	c := &Cached{Provider: inner, Dir: t.TempDir()}

	first, err := c.Image(context.Background(), aView())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if !c.Fetched() {
		t.Error("Fetched() is false after a real fetch")
	}

	// A second Cached over the same directory is the next RUN of the program,
	// which is the case that matters -- a cache that only worked within one
	// process would do nothing for the workflow it is for.
	next := &Cached{Provider: &countingProvider{}, Dir: c.Dir}
	second, err := next.Image(context.Background(), aView())
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if next.Provider.(*countingProvider).calls != 0 {
		t.Error("the second run went to the service anyway")
	}
	if next.Fetched() {
		t.Error("Fetched() is true on a run that sent nothing -- the privacy line would say the route was sent when it was not")
	}
	if first.Bounds() != second.Bounds() {
		t.Errorf("cached image is %v, want %v", second.Bounds(), first.Bounds())
	}
}

// TestCached_KeysOnTheViewAndTheStyle keeps two different pictures apart.
// A name without the style would serve a Landscape render out of an Outdoors
// cache, which looks like a render that ignored the flag.
func TestCached_KeysOnTheViewAndTheStyle(t *testing.T) {
	dir := t.TempDir()
	outdoors := &Cached{Provider: &countingProvider{name: "fake/outdoors"}, Dir: dir}
	landscape := &Cached{Provider: &countingProvider{name: "fake/landscape"}, Dir: dir}

	if _, err := outdoors.Image(context.Background(), aView()); err != nil {
		t.Fatal(err)
	}
	if _, err := landscape.Image(context.Background(), aView()); err != nil {
		t.Fatal(err)
	}
	if got := landscape.Provider.(*countingProvider).calls; got != 1 {
		t.Errorf("a second style made %d requests, want 1 -- it is sharing the first style's cache entry", got)
	}

	// A different view under the same style is also a different entry.
	other := aView()
	other.North += 0.05
	if _, err := outdoors.Image(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if got := outdoors.Provider.(*countingProvider).calls; got != 2 {
		t.Errorf("a different view made %d requests in total, want 2", got)
	}
}

// TestCached_ExpiredEntryIsRefetched pins the TTL. Map data changes on a
// cartographic timescale, so this is slow rather than off.
func TestCached_ExpiredEntryIsRefetched(t *testing.T) {
	inner := &countingProvider{}
	c := &Cached{Provider: inner, Dir: t.TempDir(), TTL: time.Hour}
	if _, err := c.Image(context.Background(), aView()); err != nil {
		t.Fatal(err)
	}

	// Age the entry past the TTL.
	entry := filepath.Join(c.Dir, c.entry(aView()))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Image(context.Background(), aView()); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Errorf("made %d requests, want 2 -- the expired entry was served anyway", inner.calls)
	}
}

// TestCached_IsNeverLoadBearing covers every way the cache itself can fail.
// It is an optimisation, and a render that died because a cache directory
// was unwritable would have turned a nicety into a dependency.
func TestCached_IsNeverLoadBearing(t *testing.T) {
	t.Run("a corrupt entry is a miss, not an error", func(t *testing.T) {
		inner := &countingProvider{}
		c := &Cached{Provider: inner, Dir: t.TempDir()}
		if _, err := c.Image(context.Background(), aView()); err != nil {
			t.Fatal(err)
		}
		// A run killed mid-write leaves a truncated PNG.
		entry := filepath.Join(c.Dir, c.entry(aView()))
		if err := os.WriteFile(entry, []byte("not a png"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Image(context.Background(), aView()); err != nil {
			t.Errorf("a corrupt entry became an error: %v", err)
		}
		if inner.calls != 2 {
			t.Errorf("made %d requests, want 2 -- the corrupt entry was decoded anyway", inner.calls)
		}
	})

	t.Run("an unwritable directory still renders", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "readonly")
		if err := os.Mkdir(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		c := &Cached{Provider: &countingProvider{}, Dir: dir}
		if _, err := c.Image(context.Background(), aView()); err != nil {
			t.Errorf("an unwritable cache directory failed the fetch: %v", err)
		}
	})

	t.Run("no directory means no caching, not a failure", func(t *testing.T) {
		inner := &countingProvider{}
		c := &Cached{Provider: inner, Dir: ""}
		for i := 0; i < 3; i++ {
			if _, err := c.Image(context.Background(), aView()); err != nil {
				t.Fatal(err)
			}
		}
		if inner.calls != 3 {
			t.Errorf("made %d requests with caching disabled, want 3", inner.calls)
		}
	})
}

// TestCached_ProviderFailureIsNotCached keeps a transient outage from being
// remembered. Caching a failure would mean one flaky moment disabled the
// basemap for as long as the TTL.
func TestCached_ProviderFailureIsNotCached(t *testing.T) {
	inner := &countingProvider{err: errors.New("service down")}
	c := &Cached{Provider: inner, Dir: t.TempDir()}

	if _, err := c.Image(context.Background(), aView()); err == nil {
		t.Fatal("no error from a failing provider")
	}
	inner.err = nil
	if _, err := c.Image(context.Background(), aView()); err != nil {
		t.Errorf("the recovered service was not retried: %v", err)
	}
	if inner.calls != 2 {
		t.Errorf("made %d requests, want 2", inner.calls)
	}
}

// planSpan values are chosen from measured behaviour, not picked freely.
//
// planStatic resolves a view to a centre, an integer zoom and a canvas, and
// the requested pixel size reaches that answer only through the choice of
// zoom. At both ends of the range it does not reach it at all: a wide view is
// capped by the endpoint's 2560-pixel ceiling, and a tight one by the
// service's maximum zoom of 22, and in both cases every resolution from 720p
// to 4K resolves to one identical request.
//
// Those two cases are exactly what fitdash asks for -- a whole-course view is
// the wide one and a highlight zoom is the tight one -- so a cache keyed on
// the requested pixel size misses hardest precisely where this program lives.
const (
	// spanBothZoomAndCeiling: 720p resolves to zoom 18, while 1080p and 4K
	// both resolve to zoom 19. One fixture exercises both directions.
	spanSplitsAtThisSize = 1.0 / 360.0 / 200
	// spanCappedByTheCeiling: every size resolves to zoom 14, because the
	// canvas a deeper zoom needs is past what the endpoint will return.
	spanCappedByTheCeiling = 1.0 / 360.0 / 8
)

func servedPNG(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 8, 8))
	}
}

// TestCached_OneRequestForEveryViewThatResolvesToTheSameRequest is the bug.
//
// The cache used to key on View.Width and View.Height, which is not what
// determines the response: a static-map endpoint takes a centre and an
// integer zoom, so a whole range of requested sizes resolves to one request
// and one byte-identical image. Every one of those was a miss -- a second
// fetch, against the user's own quota, sending the area of their activity
// again to be told the same thing, and a second copy of the same PNG on disk.
//
// Asserted as request COUNT rather than as a key comparison, because the
// count is what the user pays and a key is an implementation detail.
func TestCached_OneRequestForEveryViewThatResolvesToTheSameRequest(t *testing.T) {
	for _, c := range []struct {
		name   string
		span   float64
		sizes  [][2]int
		wantEq bool
	}{
		{"the endpoint's pixel ceiling caps them all", spanCappedByTheCeiling,
			[][2]int{{912, 224}, {1368, 336}, {2736, 672}}, true},
		{"a deeper zoom fits, so these two still agree", spanSplitsAtThisSize,
			[][2]int{{1368, 336}, {2736, 672}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tf, reqs := fakeService(t, servedPNG(t))
			cached := &Cached{Provider: tf, Dir: t.TempDir()}

			base := viewFor(-33.82, 151.19, c.span, 1368, 336)
			// The precondition, stated rather than assumed: if planStatic
			// ever stops resolving these to one request, this test proves
			// nothing and should say so instead of passing.
			first, ok := tf.CacheKey(sizedAs(base, c.sizes[0]))
			if !ok {
				t.Fatal("the fixture view cannot be planned at all")
			}
			for _, s := range c.sizes[1:] {
				if got, _ := tf.CacheKey(sizedAs(base, s)); got != first {
					t.Fatalf("fixture: %dx%d resolves to %q, not %q; these sizes no longer share a request",
						s[0], s[1], got, first)
				}
			}

			for _, s := range c.sizes {
				if _, err := cached.Image(context.Background(), sizedAs(base, s)); err != nil {
					t.Fatalf("%dx%d: %v", s[0], s[1], err)
				}
			}
			if got := len(*reqs); got != 1 {
				t.Errorf("%d requests for %d views that resolve to one identical request, want 1",
					got, len(c.sizes))
			}
		})
	}
}

// TestCached_StillFetchesWhenTheRequestGenuinelyDiffers is the other half,
// and without it the fix above could be satisfied by keying on nothing.
//
// 720p resolves to a shallower zoom than 1080p for this view, which is a
// different request and a genuinely different image. Serving the first from
// the cache would put a map of the wrong detail under the route.
func TestCached_StillFetchesWhenTheRequestGenuinelyDiffers(t *testing.T) {
	tf, reqs := fakeService(t, servedPNG(t))
	cached := &Cached{Provider: tf, Dir: t.TempDir()}

	base := viewFor(-33.82, 151.19, spanSplitsAtThisSize, 1368, 336)
	small, large := sizedAs(base, [2]int{912, 224}), sizedAs(base, [2]int{1368, 336})

	ks, _ := tf.CacheKey(small)
	kl, _ := tf.CacheKey(large)
	if ks == kl {
		t.Fatalf("fixture: both sizes resolve to %q, so this test cannot tell the two apart", ks)
	}

	for _, v := range []View{small, large} {
		if _, err := cached.Image(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(*reqs); got != 2 {
		t.Errorf("%d requests for two views that resolve to different zooms, want 2 -- one of them was served imagery drawn at the wrong detail", got)
	}
}

// TestCached_KeysOnTheViewWhenTheProviderCannotSay keeps the fallback honest.
//
// CacheKeyer is optional, so a provider that cannot reduce a view to a
// request must still get one entry per distinct view. Collapsing those would
// be the opposite failure to the one fixed here, and a worse one: a cache
// returning the wrong image rather than too few hits.
func TestCached_KeysOnTheViewWhenTheProviderCannotSay(t *testing.T) {
	inner := &countingProvider{}
	if _, ok := interface{}(inner).(CacheKeyer); ok {
		t.Fatal("this fixture is meant to be a provider that does NOT implement CacheKeyer")
	}
	cached := &Cached{Provider: inner, Dir: t.TempDir()}

	base := aView()
	for _, wh := range [][2]int{{400, 300}, {800, 600}} {
		if _, err := cached.Image(context.Background(), sizedAs(base, wh)); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 2 {
		t.Errorf("made %d requests for two differently sized views of a provider that cannot canonicalise them, want 2", inner.calls)
	}
}

// TestThunderforest_CacheKeyCarriesNoCredential is a small rule with a large
// blast radius: the key is a query parameter and the cache key is built from
// the PATH, so the two never meet.
//
// It is hashed before it reaches a filename, so a leak here would not be
// visible on disk -- which is exactly why it is worth asserting. A secret
// that reaches a string it did not need to reach is a secret one refactor
// away from reaching a log line.
func TestThunderforest_CacheKeyCarriesNoCredential(t *testing.T) {
	const secret = "s3cr3t-key-value"
	tf := &Thunderforest{Key: Key(secret), Style: "outdoors"}
	key, ok := tf.CacheKey(viewFor(-33.82, 151.19, spanSplitsAtThisSize, 1368, 336))
	if !ok {
		t.Fatal("a plannable view produced no cache key")
	}
	if strings.Contains(key, secret) {
		t.Error("the cache key contains the API key")
	}
	if strings.Contains(key, "apikey") {
		t.Errorf("the cache key contains the query parameter the credential travels in: %q", key)
	}
	if !strings.Contains(key, "outdoors") {
		t.Errorf("the cache key does not name the style: %q -- two styles would share an entry", key)
	}
}

// TestCached_NeverServesOneViewsImageryForAnother is the failure the key has
// to rule out, and it is worse than the one this change fixes.
//
// A canvas size does not identify a request. Halve a view's span and the
// resolved zoom goes up by one, and the canvas -- which is the span times the
// world size at that zoom -- comes out IDENTICAL. Measured: the same centre
// at two spans an octave apart both plan a 1865x458 canvas, at zoom 19 and
// zoom 18, covering ground that differs by a factor of two on each axis.
//
// A key built from the centre and the canvas alone would give those two one
// entry, and the second render would be handed the first's imagery: a map at
// the wrong scale, drawn under a route that no longer sits on its own roads.
// Nothing would error and nothing would look broken except the map.
func TestCached_NeverServesOneViewsImageryForAnother(t *testing.T) {
	const span = 1.0 / 360.0 / 200
	tf, reqs := fakeService(t, servedPNG(t))
	cached := &Cached{Provider: tf, Dir: t.TempDir()}

	tight := viewFor(-33.82, 151.19, span, 1368, 336)
	wide := viewFor(-33.82, 151.19, 2*span, 1368, 336)

	pt, _ := planStatic(tight, thunderforestMaxPixels, thunderforestMaxZoom)
	pw, _ := planStatic(wide, thunderforestMaxPixels, thunderforestMaxZoom)
	if pt.Width != pw.Width || pt.Height != pw.Height {
		t.Fatalf("fixture: canvases are %dx%d and %dx%d; these views no longer collide on size and prove nothing",
			pt.Width, pt.Height, pw.Width, pw.Height)
	}
	if pt.Zoom == pw.Zoom {
		t.Fatalf("fixture: both plan zoom %d, so there is no distinction left to lose", pt.Zoom)
	}

	for _, v := range []View{tight, wide} {
		if _, err := cached.Image(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(*reqs); got != 2 {
		t.Errorf("%d requests for two views covering different ground at the same canvas size, want 2 -- the second was served the first's map, at half the scale it asked for", got)
	}
}

// TestThunderforest_TheCacheKeyIsTheRequest pins the property that makes one
// shared path function worth having, and it is the property neither caller
// can check on its own.
//
// The cache key and the request URL are the same string by construction. If
// they were formatted separately they could drift -- a different precision, a
// different field order -- and the failure would be silent in both
// directions: two identical requests filed under two keys, or two different
// requests filed under one.
//
// The coordinate order is asserted alongside it because this fixture is the
// only place it can be. Longitude comes first in the path, and swapping the
// two sends a request for a point in the Indian Ocean while the key, the
// plan and every test that looks only at sizes go on agreeing with
// themselves. The fixture's own coordinates make the swap unmistakable: a
// longitude of 151 is not a latitude anywhere.
func TestThunderforest_TheCacheKeyIsTheRequest(t *testing.T) {
	tf, reqs := fakeService(t, servedPNG(t))
	tf.Style = "outdoors"
	v := viewFor(-33.82, 151.19, spanSplitsAtThisSize, 1368, 336)

	key, ok := tf.CacheKey(v)
	if !ok {
		t.Fatal("a plannable view produced no cache key")
	}
	if _, err := tf.Image(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("made %d requests, want 1", len(*reqs))
	}
	if got := (*reqs)[0].URL.Path; got != key {
		t.Errorf("the request path is %q and the cache key is %q; they are formatted in two places and have drifted", got, key)
	}
	if !strings.Contains(key, "/151.") {
		t.Errorf("the path does not carry the longitude first: %q -- swapped, this asks the service for a point in the ocean", key)
	}
}

func sizedAs(v View, wh [2]int) View {
	v.Width, v.Height = wh[0], wh[1]
	return v
}

// TestCached_SurvivesItsOwnSweepOnTheNextRun is the bug a single-process test
// cannot see, and it is the one that would have destroyed the feature.
//
// The sweep runs once per Cached, on first use, which in one run means it
// happens BEFORE anything is written. So an entry whose name the sweep does
// not recognise still survives the run that wrote it, and every test inside
// one process passes. The next run sweeps first, deletes the lot, and the
// cache becomes a slower way of always missing -- with no error, and with the
// summary cheerfully reporting a fetch every time.
//
// A second Cached over the same directory is what a second run is.
func TestCached_SurvivesItsOwnSweepOnTheNextRun(t *testing.T) {
	dir := t.TempDir()
	v := viewFor(-33.82, 151.19, spanSplitsAtThisSize, 1368, 336)

	first, reqs := fakeService(t, servedPNG(t))
	if _, err := (&Cached{Provider: first, Dir: dir}).Image(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("the first run made %d requests, want 1", len(*reqs))
	}

	second, reqs2 := fakeService(t, servedPNG(t))
	second.Key, second.Style = first.Key, first.Style
	if _, err := (&Cached{Provider: second, Dir: dir}).Image(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if got := len(*reqs2); got != 0 {
		t.Errorf("the second run made %d requests, want 0 -- it swept away the entry the first run had just written", got)
	}
}

// TestCached_SweepsWhenItIsUsed wires the two halves together.
//
// Everything else here calls sweep directly, which proves what it does and
// not that anything calls it. Without this, deleting the one line that runs
// it leaves a cache that grows forever and a suite that passes.
//
// It also pins WHEN: on use, not on construction. A render configured with a
// cache directory it never reaches -- no --basemap, or a route with no GPS --
// must not go reading and deleting in a directory it was never asked to open.
func TestCached_SweepsWhenItIsUsed(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, strings.Repeat("b", 64)+".png")
	if err := os.WriteFile(stale, []byte("an entry from the previous key scheme"), 0o600); err != nil {
		t.Fatal(err)
	}

	tf, _ := fakeService(t, servedPNG(t))
	cached := &Cached{Provider: tf, Dir: dir}

	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("constructing a Cached already touched the directory: %v", err)
	}

	v := viewFor(-33.82, 151.19, spanSplitsAtThisSize, 1368, 336)
	if _, err := cached.Image(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("an entry from the previous key scheme survived a fetch; nothing is calling the sweep")
	}
}

// TestCached_SweepRemovesOnlyWhatItWrote is the safety half, and the reason
// the sweep matches names strictly instead of emptying the directory.
//
// --basemap-cache takes a path, so the directory belongs to the user. A sweep
// that deleted whatever it found would be one typo away from eating a folder
// of documents, and it would do it silently, on a run whose purpose was to
// draw a map.
func TestCached_SweepRemovesOnlyWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	// Written long enough ago to be past any TTL, so age is never the reason
	// one of these survives.
	old := time.Now().Add(-90 * 24 * time.Hour)
	write := func(name string, stale bool) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if stale {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	condemned := map[string]string{
		"an entry from the previous key scheme": write(digest+".png", false),
		"an entry past the TTL":                 write("2-"+digest+".png", true),
		"a temporary left by a killed run":      write(".tmp-1234567890", true),
	}
	spared := map[string]string{
		"a current entry within the TTL": write("2-"+strings.Repeat("a", 64)+".png", false),
		"somebody's photograph":          write("holiday.png", true),
		"a README":                       write("README.md", true),
		"a digest with the wrong length": write("abc123.png", true),
		"a digest that is not hex":       write(strings.Repeat("z", 64)+".png", true),
		"a version that is not a number": write("v2-"+digest+".png", true),
	}
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	(&Cached{Provider: &countingProvider{}, Dir: dir}).sweep()

	for what, p := range condemned {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was kept; the cache can never read it again", what)
		}
	}
	for what, p := range spared {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", what, err)
		}
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("a subdirectory was removed: %v", err)
	}
}

// TestParseEntryName covers the rule the sweep's safety rests on directly,
// since a name that is wrongly accepted is a file that is wrongly deleted.
func TestParseEntryName(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name    string
		version int
		ours    bool
	}{
		{digest + ".png", 1, true},
		{"2-" + digest + ".png", 2, true},
		{"17-" + digest + ".png", 17, true},
		{".tmp-9876", entryVersion, true},
		{digest, 0, false},
		{digest + ".jpg", 0, false},
		{"0-" + digest + ".png", 0, false},
		{"-" + digest + ".png", 0, false},
		{digest[:63] + ".png", 0, false},
		{digest + "0.png", 0, false},
		{strings.Repeat("g", 64) + ".png", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		version, ours := parseEntryName(c.name)
		if ours != c.ours || (ours && version != c.version) {
			t.Errorf("parseEntryName(%q) = %d, %v; want %d, %v", c.name, version, ours, c.version, c.ours)
		}
	}
}
