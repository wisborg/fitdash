package tilemap

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
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
