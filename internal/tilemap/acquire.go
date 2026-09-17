package tilemap

import (
	"context"
	"errors"
	"fmt"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
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
// It is osmbase's fetch.Archive plus the two things that are fitdash's rather
// than the library's: the wording of its errors, and what it does about an
// archive that credits nobody. Everything else -- telling a path from a URL,
// the vector tile check, the redacted name, the traffic accounting, keeping
// the tile index and the raw bytes bound together so a plan made against one
// archive cannot be fetched from another -- used to be spelled here in three
// hundred lines and is now spelled once, upstream, where osmbase's own
// command reads the same code.
//
// That matters beyond saving the lines. The default store root is shared
// between the two programs, so both have to file the same archive under the
// same ID or the same ground lands on disk twice under two names. The two
// implementations did agree; they agreed by coincidence, and this removes the
// coincidence.
type Archive struct {
	*fetch.Archive
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
	a, err := fetch.Open(source, fetch.Options{
		Default: DefaultArchive,
		Trace:   trace,
		// Required here, unlike in osmbase's own command, because every
		// fitdash use of an archive ends in a vector tile decoder. Refused at
		// open rather than at the first tile: a PNG handed to a protobuf
		// decoder does not announce itself, it reports something about an
		// undefined field number.
		RequireVectorTiles: true,
	})
	if err != nil {
		return nil, fmt.Errorf("tilemap: %w", err)
	}
	return &Archive{Archive: a}, nil
}

// Attribution is the credit the archive's own metadata declares, RAW -- as
// the archive wrote it, markup and all.
//
// Deliberately not run through render.PlainCredit. What goes into a store's
// manifest is a faithful record of what the source said, so that a store
// written here and a store written by osmbase's own command hold the same
// bytes and stay interchangeable; the conversion to text a frame can carry
// belongs at the render end, where it is needed and where a store somebody
// else filled gets it too. See OpenLocal.
//
// An archive that declares none is an error rather than an empty string, and
// the message is fitdash's own rather than the library's, because the
// consequence is: OpenLocal refuses to draw a map it cannot credit, so a
// fetch that succeeded here would leave the user with a directory of tiles
// and a render that will not use them. Better to spend nothing and say so.
// osmbase reports it as a sentinel precisely so each consumer can decide
// this for itself.
func (a *Archive) Attribution() (string, error) {
	credit, err := a.Archive.Attribution()
	if errors.Is(err, fetch.ErrNoAttribution) {
		return "", fmt.Errorf("tilemap: the map archive %s declares no attribution, and a render from it could credit nobody; a rendered map is a Produced Work under its data licence, so fetching this would download tiles fitdash then refuses to draw", a.Name())
	}
	if err != nil {
		return "", fmt.Errorf("tilemap: %w", err)
	}
	return credit, nil
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
//
// The archive's own zoom range is NOT passed: fetch.Archive fills it in from
// the header, which is where that fact lives.
func (a *Archive) Plan(ctx context.Context, dst *slice.Source, b slice.Bounds, cellZoom uint8, maxZoom int) (*acquire.Plan, error) {
	return a.Archive.Plan(ctx, dst, acquire.Request{
		Bounds:   b,
		MaxZoom:  maxZoom,
		CellZoom: cellZoom,
	})
}
