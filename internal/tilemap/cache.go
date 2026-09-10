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
// wrongly because a hedge is a month out of date. It also sits inside the
// caching allowance map services grant -- the strictest read for this
// project's purposes is thirty days on the device that made the request.
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

func (c *Cached) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTTL
}

// entry names the file for a view.
//
// The name is a hash of the provider (style included -- see Thunderforest.Name)
// and the view, at a precision well past what any zoom can distinguish. The
// coordinates are formatted rather than hashed as float bits so that two runs
// computing the same view by slightly different arithmetic still agree, which
// is the difference between a cache that works across a rebuild and one that
// quietly never hits.
func (c *Cached) entry(v View) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%.9f|%.9f|%.9f|%.9f|%d|%d",
		c.Provider.Name(), v.North, v.West, v.South, v.East, v.Width, v.Height)
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
