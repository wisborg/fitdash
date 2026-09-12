package tilemap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// DefaultTTL is how long a cached image is served before it is fetched again.
//
// Thirty days, which is a cartographic timescale rather than a technical one:
// a road that moved is worth picking up eventually, and no route is redrawn
// wrongly because a hedge is a month out of date.
//
// It is this package's own choice rather than a limit read off anyone's
// terms. Thunderforest's permit keeping tiles "beyond the HTTP Expiry date,
// until fresh tiles are downloaded", which is more generous than this; other
// services are stricter, and thirty days is inside every allowance surveyed
// before this provider was chosen. A caller who needs to match a particular
// service's rule sets Cached.TTL.
const DefaultTTL = 30 * 24 * time.Hour

// Cached wraps a Provider with an on-disk cache.
//
// This is worth having for one workflow above all: tuning. Choosing where a
// --highlight starts means rendering the same activity again and again, and
// without a cache every one of those runs re-fetches identical imagery --
// slower for the user, and spending someone's quota to be told the same thing.
// Map services grant on-device caching in their terms precisely for this.
//
// Caching also makes a basemap survive going offline, which matters more than
// it sounds: the second render of an activity works on a train.
type Cached struct {
	Provider
	// Dir is where images are kept. An empty Dir disables caching entirely
	// rather than guessing a location.
	Dir string
	// TTL is how long an entry is served for. Zero means DefaultTTL.
	TTL time.Duration

	// fetched counts requests that actually reached the service, so a caller
	// can tell a run that sent something from one that did not. That
	// distinction is the whole point: a summary line telling a user their
	// route was sent to a third party is a false statement when every image
	// came off their own disk, and a privacy notice that cries wolf is worse
	// than none.
	fetched atomic.Int64
}

// Image returns the cached image for v, fetching and storing it on a miss.
//
// A cache that cannot be read or written is never an error. It is an
// optimisation, and a render that failed because a cache directory was
// read-only would have turned a nicety into a dependency -- so every failure
// here falls through to the underlying provider, and a write that fails is
// simply a future miss.
func (c *Cached) Image(ctx context.Context, v View) (image.Image, error) {
	if c.Dir == "" {
		return c.Provider.Image(ctx, v)
	}
	path := filepath.Join(c.Dir, c.entry(v))

	if img, ok := readEntry(path, c.ttl()); ok {
		return img, nil
	}
	c.fetched.Add(1)
	img, err := c.Provider.Image(ctx, v)
	if err != nil {
		return nil, err
	}
	writeEntry(path, img)
	return img, nil
}

// Fetched reports whether any image was actually requested from the service,
// as opposed to served from disk.
func (c *Cached) Fetched() bool { return c.fetched.Load() > 0 }

// Reporter is implemented by a Provider that can say whether it actually went
// to the network.
//
// It exists so a caller can tell a run that sent something from one that did
// not, without knowing which Provider it is holding -- a plain provider does
// not implement it and always sent. Named rather than written inline at the
// call site because that is what the panel layer does for the same shape of
// question, and an anonymous interface in one of two matching places is an
// inconsistency a reader has to notice twice.
type Reporter interface {
	Fetched() bool
}

var _ Reporter = (*Cached)(nil)

func (c *Cached) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTTL
}

// CacheKeyer is implemented by a Provider that can reduce a View to the
// distinct request it will actually make.
//
// It exists because a View is not what determines the response. A static-map
// endpoint takes a centre and an integer zoom, so a whole RANGE of requested
// pixel sizes resolves to one request and one byte-identical image -- and a
// cache keyed on the view asked for, rather than on the request made, misses
// every one of them.
//
// That was not hypothetical. Keying on View.Width and View.Height meant every
// render at a new resolution re-fetched imagery already on disk, at the user's
// own quota, sending the area of their activity again to be told the same
// thing. A layout change or a theme whose credit strip is a fraction taller
// did it too, because both move the panel's box by a pixel.
//
// Optional rather than part of Provider, and named rather than written inline,
// for the same reasons Reporter is: a provider with nothing to canonicalise
// simply does not implement it, and this package already answers one
// question of this shape that way.
type CacheKeyer interface {
	// CacheKey returns a string identifying the request v resolves to, and
	// whether v resolves to one at all. It must not contain a credential --
	// it is hashed, but a key that reaches a hash function is a key that was
	// held in a string somewhere it did not need to be.
	CacheKey(v View) (string, bool)
}

var _ CacheKeyer = (*Thunderforest)(nil)

// entry names the file for a view.
//
// The name is a hash of the provider (style included -- see Thunderforest.Name)
// and, where the provider can say, the request that view resolves to. See
// CacheKeyer for why the view alone is the wrong thing to key on.
//
// The fallback is the view itself, at a precision well past what any zoom can
// distinguish. The coordinates are formatted rather than hashed as float bits
// so that two runs computing the same view by slightly different arithmetic
// still agree, which is the difference between a cache that works across a
// rebuild and one that quietly never hits.
func (c *Cached) entry(v View) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|", c.Provider.Name())
	if k, ok := c.Provider.(CacheKeyer); ok {
		if key, resolved := k.CacheKey(v); resolved {
			fmt.Fprintf(h, "req|%s", key)
			return hex.EncodeToString(h.Sum(nil)) + ".png"
		}
	}
	fmt.Fprintf(h, "view|%.9f|%.9f|%.9f|%.9f|%d|%d",
		v.North, v.West, v.South, v.East, v.Width, v.Height)
	return hex.EncodeToString(h.Sum(nil)) + ".png"
}

func readEntry(path string, ttl time.Duration) (image.Image, bool) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > ttl {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		// A truncated or corrupt entry -- from a run killed mid-write, say.
		// Treated as a miss rather than an error, and overwritten by the
		// fetch that follows.
		return nil, false
	}
	return img, true
}

// writeEntry stores an image, via a temporary file so a killed render leaves
// no half-written entry for the next one to decode.
func writeEntry(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if err := png.Encode(tmp, img); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
	}
}

// DefaultCacheDir is where imagery is kept when the caller does not choose.
func DefaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "fitdash", "basemap")
}
