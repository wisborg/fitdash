package panel

import (
	"context"
	"errors"
	"image"
	"time"

	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// basemapTimeout bounds how long a whole render will wait for imagery.
//
// A basemap is decoration and the render is the product, so a map service
// that has stopped answering must cost a render some seconds and not its
// existence. It covers every view together rather than each one separately:
// what a user is willing to wait for is "before my render starts", not "per
// image", and a course with three zooming highlights should not be able to
// wait three times as long as one without.
const basemapTimeout = 30 * time.Second

// basemapView is one fetched image and the ground it covers.
//
// The bounds are kept in PROJECTED units rather than degrees because that is
// what the per-frame cross-fade needs: deciding which part of this image the
// current viewport is looking at is a rectangle intersection, and doing it in
// degrees would mean projecting twice per frame to answer a question that is
// already linear here.
type basemapView struct {
	img                      image.Image
	minX, minY, spanX, spanY float64
}

// fetchBasemaps resolves imagery for every view this render will draw: the
// whole course, and one per highlight that zooms.
//
// Failure is not an error the caller must handle. Every outcome that is not
// an image -- no provider, no network, a refused key, a service that is
// down -- returns no view and a note, and the panel draws the outline on
// clean background exactly as it always has. That is what makes "offline
// must keep working" a property of the code rather than a hope: there is no
// path through here that can fail a render.
func fetchBasemaps(ctx *Context, rp *routePainter, base route.Projection, marks []routeMark) (*basemapView, []*basemapView, string) {
	if ctx.Basemap == nil {
		return nil, nil, ""
	}
	box := rp.box
	placerW, placerH := rp.placeArea()

	c, cancel := context.WithTimeout(context.Background(), basemapTimeout)
	defer cancel()

	// The error is carried back, not swallowed. A provider knows things the
	// caller cannot work out -- that the key was refused, that the plan does
	// not cover this endpoint, that the service is rate-limiting -- and says
	// so in words chosen for a user to act on. Dropping it and substituting
	// "could not be fetched" throws away the only sentence that would have
	// told somebody what to do next.
	//
	// A nil view with a nil error is the third case, and it is not a failure
	// at all: the course could not be placed on a map, so nothing was ever
	// asked of anyone.
	fetch := func(p route.Projection) (*basemapView, error) {
		// ONE call, and the degrees derived from its result rather than
		// asked for separately. The rectangle the service is asked about and
		// the rectangle the returned image is drawn into have to be the same
		// rectangle: if they are ever computed twice and drift apart, the
		// imagery sits a few pixels beside the route it was fetched for, and
		// nothing short of comparing pixels would notice.
		//
		// Derived from the route's own PLACEMENT rather than from the box,
		// for the same reason -- see route.Projection.CoverBox.
		minX, minY, spanX, spanY, ok := p.CoverBox(placerW, placerH, box.W, box.H, rp.inset, rp.inset)
		if !ok {
			return nil, nil
		}
		north, west := tilemap.Unproject(minX, minY)
		south, east := tilemap.Unproject(minX+spanX, minY+spanY)

		img, err := ctx.Basemap.Image(c, tilemap.View{
			North: north, West: west, South: south, East: east,
			Width: int(box.W), Height: int(box.H),
		})
		if err != nil {
			return nil, err
		}
		if img == nil {
			return nil, errors.New("the service returned no image")
		}
		return &basemapView{img: img, minX: minX, minY: minY, spanX: spanX, spanY: spanY}, nil
	}

	baseView, err := fetch(base)
	switch {
	case baseView != nil:
	case err != nil:
		// The whole-course view is the one every render needs. Without it
		// there is no basemap worth having, and fetching the zoomed ones
		// would be several more requests to a service that has just proved
		// it cannot answer.
		return nil, nil, "no imagery: " + err.Error()
	default:
		// Nothing was asked of the service, so it must not be blamed. This
		// is an activity whose course cannot be placed -- every fix at one
		// spot, say -- and pointing the user at their key or their network
		// for a property of their own file is worse than saying nothing.
		return nil, nil, "this activity has no course that can be placed on a map, so none was fetched"
	}

	zooms := make([]*basemapView, len(marks))
	missing, firstErr := 0, error(nil)
	for i, m := range marks {
		if !m.zoomOK {
			continue
		}
		var zoomErr error
		if zooms[i], zoomErr = fetch(m.zoom); zooms[i] == nil {
			missing++
			if firstErr == nil {
				firstErr = zoomErr
			}
		}
	}
	note := ""
	if missing > 0 {
		note = "some zoomed views have no imagery; they fall back to the whole-course map"
		if firstErr != nil {
			note += " (" + firstErr.Error() + ")"
		}
	}
	return baseView, zooms, note
}

// drawInto composites this view into the box for a viewport, at an opacity.
//
// The image covers a fixed rectangle of the projected plane. The viewport is
// another rectangle of the same plane, and drawing one for the other is the
// linear map between them -- so the destination is the box scaled by the
// ratio of the two spans and offset by their difference. Everything outside
// the box is clipped by the caller, which is what lets a zoomed viewport
// magnify a corner of the image without the rest of it landing on the panels
// alongside.
func (v *basemapView) drawInto(c *Canvas, box Box, minX, minY, spanX, spanY float64, opacity float64) {
	if v == nil || v.img == nil || spanX <= 0 || spanY <= 0 || v.spanX <= 0 || v.spanY <= 0 {
		return
	}
	scaleX, scaleY := box.W/spanX, box.H/spanY
	dst := Box{
		X: box.X + (v.minX-minX)*scaleX,
		Y: box.Y + (v.minY-minY)*scaleY,
		W: v.spanX * scaleX,
		H: v.spanY * scaleY,
	}
	c.Image(v.img, dst, opacity)
}

// BasemapReporter is implemented by a Painter that fetched map imagery and
// has something to say about how it went.
//
// It exists so the render summary can report a basemap that did not arrive
// without internal/render or cmd knowing what a basemap is. A fetch that
// failed is invisible in the frames -- the route is simply drawn on
// background, exactly as it would be with no --basemap at all -- so if it is
// not said in words the user is left comparing a render against what they
// expected and finding nothing wrong with it.
type BasemapReporter interface {
	// BasemapNote is empty when there is nothing to report.
	BasemapNote() string
	// BasemapDrew reports whether any imagery actually reached the frame.
	BasemapDrew() bool
}

// BasemapNote reports how the imagery fetch went.
func (p *routePainter) BasemapNote() string { return p.mapNote }

// BasemapDrew reports whether imagery reached the frame.
func (p *routePainter) BasemapDrew() bool { return p.baseMap != nil }

// BasemapSuppressor is implemented by a Painter that can be asked to leave
// its map imagery out for a pass.
//
// It exists for the pause notice. That card is placed wherever the render
// draws least, measured by sampling frames and counting pixels that are not
// background -- and a basemap inks its panel's whole box, so every candidate
// position over the map counts as full and the card is pushed onto a
// READOUT instead. Imagery is context and a reading is content: a notice over
// the map is fine, a notice over the heart rate is not.
//
// So the mask is measured with imagery suppressed, which restores exactly the
// placement a render without --basemap would have chosen. It is a toggle
// rather than a second rendering path because the alternative -- teaching
// internal/render what a basemap is -- would put that knowledge in the one
// package that composites whatever panels draw without knowing what any of it
// means.
type BasemapSuppressor interface {
	SuppressBasemap(bool)
}

// SuppressBasemap leaves the imagery out of the next draws, or puts it back.
func (p *routePainter) SuppressBasemap(v bool) { p.suppressMap = v }
