package tilemap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// DefaultArchive is the map archive a fetch reads when the user names none.
//
// # Why there is a default at all
//
// osmbase's LIBRARY ships none, deliberately: a library that defaulted to
// somebody's bucket would put its consumer's traffic on a host the consumer
// never chose. An application is the other case, and this is one. A person
// who has just installed fitdash has no archive, and "first obtain a 125 GiB
// planet file" is not a first step -- so the default is named here, said out
// loud on stderr before anything is read, restated in the confirmation
// prompt, and replaceable with a path to a local file.
//
// # Why this URL rather than the daily build
//
// Protomaps' daily builds live at build.protomaps.com/YYYYMMDD.pmtiles and
// they expire: a build older than about a week is gone, and today's does not
// exist until it has been made. A default built from today's date is
// therefore a 404 for part of every day, and a hardcoded date rots within the
// week -- both failing in the hands of the person least equipped to diagnose
// it. This is the same stable snapshot on Source Cooperative that osmbase's
// own command defaults to, which is also what keeps a store shared between
// the two tools filled from ONE source rather than two builds of the same
// ground.
const DefaultArchive = "https://data.source.coop/protomaps/openstreetmap/v4.pmtiles"

// Archive is an opened PMTiles archive, together with what a command has to
// be able to say about where its bytes come from.
//
// It is a thin binding of osmbase's reader, its coalescing range reader and
// its acquisition package -- thin on purpose. Everything about WHICH ground
// to fetch is the caller's, everything about how to fetch it is osmbase's,
// and what is left here is opening the thing and keeping the index and the
// raw bytes together. That pairing is the one piece worth owning: a plan made
// against one archive and fetched from another would not fail, it would fill
// the store with plausible wrong tiles under the right names.
type Archive struct {
	reader *pmtiles.Reader

	// bytes is the same archive read as raw spans rather than as tiles. A
	// fetch asks for one range covering eighty-five tiles instead of
	// eighty-five requests, which is the whole reason a remote archive is
	// usable at all.
	bytes  io.ReaderAt
	closer io.Closer

	name   string
	remote bool

	size    int64
	hasSize bool

	stats func() (int, int64)
	quiet func()
}

// OpenArchive opens the archive named by source, or DefaultArchive when
// source is empty.
//
// trace, when not nil, is called with a one-line description of every range
// request the archive makes. It exists for the PLANNING phase, which reads
// the archive's directories and can sit silent for several seconds against a
// remote host -- and it must be turned off with Silence before a progress bar
// is drawn, because the two write to the same stream and a carriage return
// landing in the middle of a trace line makes both unreadable.
func OpenArchive(source string, trace func(string)) (*Archive, error) {
	if source == "" {
		source = DefaultArchive
	}
	var a *Archive
	var err error
	switch {
	case strings.HasPrefix(source, "https://"), strings.HasPrefix(source, "http://"):
		a, err = openRemoteArchive(source, trace)
	case strings.Contains(source, "://"):
		scheme, _, _ := strings.Cut(source, "://")
		return nil, fmt.Errorf("tilemap: the map archive %q uses the %q scheme; give an https URL or the path to a local .pmtiles file", source, scheme)
	default:
		a, err = openLocalArchive(source)
	}
	if err != nil {
		return nil, err
	}
	// Refused here rather than at the first tile, because the failure
	// downstream is unreadable: a PNG handed to a protobuf decoder does not
	// announce itself, it reports something about an undefined field number.
	if t := a.reader.Header().TileType; t != pmtiles.TileTypeMVT {
		a.Close()
		return nil, fmt.Errorf("tilemap: the map archive %s holds %s tiles, and a basemap is drawn from vector tiles (mvt)", a.name, t)
	}
	return a, nil
}

func openLocalArchive(path string) (*Archive, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tilemap: there is no file at %s; a map archive is an https URL or the path to a .pmtiles file", path)
		}
		return nil, fmt.Errorf("tilemap: looking at the map archive %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("tilemap: %s is a directory; a map archive is one .pmtiles file", path)
	}
	r, err := pmtiles.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tilemap: opening the map archive %s: %w", path, err)
	}
	// Opened twice, as tiles and as raw bytes, because the two questions are
	// different: the reader answers "where is this tile", a fetch asks for a
	// span covering many. Two handles on one file is cheaper than reaching
	// into the reader for its source.
	f, err := os.Open(path)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("tilemap: opening the map archive %s for coalesced reads: %w", path, err)
	}
	return &Archive{
		reader: r, bytes: f, closer: closers{r, f},
		name: path, size: info.Size(), hasSize: true,
	}, nil
}

func openRemoteArchive(url string, trace func(string)) (*Archive, error) {
	src, err := acquire.NewRangeReader(url)
	if err != nil {
		return nil, fmt.Errorf("tilemap: reaching the map archive: %w", err)
	}
	if trace != nil {
		src.Trace = func(off int64, n int, elapsed time.Duration) {
			reqs, _ := src.Stats()
			trace(fmt.Sprintf("request %d: %d bytes at offset %d in %s",
				reqs, n, off, elapsed.Round(time.Millisecond)))
		}
	}
	r, err := pmtiles.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("tilemap: reading the map archive %s: %w", src.URL(), err)
	}
	// src.URL rather than the string the user typed: a private mirror behind
	// userinfo and a presigned URL with a signature in its query are both
	// ordinary, and this name is printed, recorded in the store's manifest
	// and pasted into bug reports.
	a := &Archive{reader: r, bytes: src, name: src.URL(), remote: true, stats: src.Stats}
	a.quiet = func() { src.Trace = nil }
	a.size, a.hasSize = src.Size()
	return a, nil
}

// Name is the archive's URL or path in the form that is safe to print: a
// remote one is the reader's redacted URL, never the string the user typed.
func (a *Archive) Name() string { return a.name }

// Remote reports whether reading this archive talks to anybody. It is what a
// command branches on to decide whether there is a privacy decision to put in
// front of the user at all.
func (a *Archive) Remote() bool { return a.remote }

// Size is the archive's total size and whether that is known, for saying how
// small a slice of it a fetch takes.
func (a *Archive) Size() (int64, bool) { return a.size, a.hasSize }

// Traffic is how many range requests have been made and how many bytes they
// moved, and false for a local archive -- which has neither, and for which
// reporting "0 requests" would invite the reader to think the number meant
// something.
func (a *Archive) Traffic() (requests int, bytes int64, ok bool) {
	if a.stats == nil {
		return 0, 0, false
	}
	requests, bytes = a.stats()
	return requests, bytes, true
}

// Silence turns off the per-request trace. See OpenArchive.
func (a *Archive) Silence() {
	if a.quiet != nil {
		a.quiet()
	}
}

func (a *Archive) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer.Close()
}

// Attribution is the credit the archive's own metadata declares, RAW -- as
// the archive wrote it, markup and all.
//
// Deliberately not run through render.PlainCredit. What goes into a store's manifest
// is a faithful record of what the source said, so that a store written here
// and a store written by osmbase's own command hold the same bytes and stay
// interchangeable; the conversion to text a frame can carry belongs at the
// render end, where it is needed and where a store somebody else filled gets
// it too. See OpenLocal.
//
// An archive that declares none is an error rather than an empty string,
// because of what fitdash would do with the result: OpenLocal refuses to draw
// a map it cannot credit, so a fetch that succeeded here would leave the user
// with a directory of tiles and a render that will not use them. Better to
// spend nothing and say so.
func (a *Archive) Attribution() (string, error) {
	raw, err := a.reader.Metadata()
	if err != nil {
		return "", fmt.Errorf("tilemap: reading the metadata of the map archive %s: %w", a.name, err)
	}
	var meta struct {
		Attribution string `json:"attribution"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return "", fmt.Errorf("tilemap: the metadata of the map archive %s is not JSON this can read: %w", a.name, err)
		}
	}
	if strings.TrimSpace(meta.Attribution) == "" {
		return "", fmt.Errorf("tilemap: the map archive %s declares no attribution, and a render from it could credit nobody; a rendered map is a Produced Work under its data licence, so fetching this would download tiles fitdash then refuses to draw", a.name)
	}
	return meta.Attribution, nil
}

// SourceID is the name the store files this archive's tiles under.
//
// Asked of slice rather than computed here, so that a source fetched by
// fitdash and the same source fetched by osmbase land in one directory rather
// than two copies of the same ground.
func (a *Archive) SourceID() string { return slice.SourceID(a.name, "") }

// AddTo registers this archive as a source of store and returns the source to
// fetch into. It WRITES: a caller that has not yet decided to download
// anything must not call it.
func (a *Archive) AddTo(store *slice.Store, attribution string) (*slice.Source, error) {
	h := a.reader.Header()
	src, err := store.AddSource(slice.SourceDesc{
		Source:          a.name,
		Attribution:     attribution,
		TileType:        h.TileType.String(),
		TileCompression: slice.Compression(h.TileCompression.String()),
		SourceZoom:      slice.ZoomRange{Min: h.MinZoom, Max: h.MaxZoom},
	})
	if err != nil {
		return nil, fmt.Errorf("tilemap: recording %s as a source of the map store: %w", a.name, err)
	}
	return src, nil
}

// Plan works out exactly which tiles b needs, where they are and what they
// cost, WITHOUT reading a byte of tile data.
//
// dst may be nil, which is what a store that does not exist yet holds:
// nothing. That is what lets a dry run cost an area on a machine that has
// never fetched without creating a directory first.
//
// maxZoom is acquire.AutoZoom to let the depth follow the extent. Letting it
// is almost always right and is why the flag has that default: a park run and
// a flight want different treatment, and the area's own size is the only
// thing that knows which this is.
func (a *Archive) Plan(ctx context.Context, dst *slice.Source, b slice.Bounds, cellZoom uint8, maxZoom int) (*acquire.Plan, error) {
	h := a.reader.Header()
	p, err := acquire.PlanFor(ctx, a.osm(), dst, acquire.Request{
		Bounds:     b,
		MaxZoom:    maxZoom,
		CellZoom:   cellZoom,
		SourceZoom: slice.ZoomRange{Min: h.MinZoom, Max: h.MaxZoom},
	})
	if err != nil {
		return nil, fmt.Errorf("tilemap: planning a fetch of %s from %s: %w", boundsText(b), a.name, err)
	}
	return p, nil
}

// Fetch carries out a plan, filling dst.
//
// progress may be nil. When it is not, the per-request trace must already
// have been turned off -- see OpenArchive -- or the two will overwrite each
// other on the same stream.
func (a *Archive) Fetch(ctx context.Context, p *acquire.Plan, dst *slice.Source, progress func(acquire.Progress)) (acquire.Result, error) {
	res, err := acquire.Fetch(ctx, p, a.osm(), dst, acquire.FetchOptions{Progress: progress})
	if err != nil {
		return acquire.Result{}, fmt.Errorf("tilemap: fetching %d tiles from %s: %w", p.Tiles, a.name, err)
	}
	return res, nil
}

// osm is this archive in the shape osmbase's acquisition package takes it.
func (a *Archive) osm() acquire.Archive {
	return acquire.Archive{Index: a.reader, Bytes: a.bytes, Name: a.name}
}

// boundsText spells an area for an error message, in the order the flags and
// the manifest use.
func boundsText(b slice.Bounds) string {
	return fmt.Sprintf("west %.4f, south %.4f, east %.4f, north %.4f", b.West, b.South, b.East, b.North)
}

// closers closes several things and reports the first failure, for the local
// archive's two handles on one file.
type closers []io.Closer

func (cs closers) Close() error {
	var first error
	for _, c := range cs {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
