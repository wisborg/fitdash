// Package encode turns a sequence of rendered frames into a file.
//
// It knows nothing about panels, activities, layouts or time. It takes
// *image.RGBA values and a frame rate, and that narrowness is the point:
//
//   - `--frames` is a SINK SWAP and nothing else. `scripts/fd frames` and
//     `scripts/fd render` run the identical render path, which is what makes
//     the fast visual loop trustworthy as a proxy for the real one. If this
//     package knew what a panel was, the two paths could diverge.
//   - Every render test can run without ffmpeg on PATH, against a sink that
//     keeps its frames in memory. Only this package's own end-to-end test
//     needs the binary, and it skips without it.
//   - The renderer's static-vs-dynamic equality test operates on images
//     rather than on a video.
package encode

import (
	"fmt"
	"image"
)

// Sink consumes rendered frames in presentation order.
//
// Every implementation must be closed exactly once, and Close must be called
// even when writing failed: the ffmpeg sink holds a subprocess and an open
// pipe, and abandoning it leaves ffmpeg blocked on a read with a handle on a
// half-written output file. The idiom is a deferred Close for the error paths
// plus an explicit one on the success path, where the error matters:
//
//	sink, err := encode.OpenVideo(ctx, cfg)
//	if err != nil {
//		return err
//	}
//	defer sink.Close()
//	... write frames ...
//	return sink.Close()   // the error here is the encode's own verdict
//
// Close is idempotent, which is what makes that pair safe.
type Sink interface {
	// WriteFrame consumes frame i. The image must match the sink's configured
	// dimensions; it is NOT retained, so a caller may reuse one buffer across
	// the whole render.
	//
	// The index is passed rather than counted internally because a sink that
	// implements Selector does not see every frame, so its own call count
	// would no longer identify which frame it was handed -- and a PNG named
	// for the wrong frame is worse than no PNG.
	WriteFrame(i int, img *image.RGBA) error

	// Close finishes the output and reports whether it succeeded. Calling it
	// more than once is safe and returns the same result.
	Close() error
}

// Selector is an optional capability a Sink may implement: it only needs some
// of the frames.
//
// A renderer that knows this can skip DRAWING the frames the sink will discard,
// which is the difference between --frames being a preview and being a full
// render that throws almost everything away. It lives here rather than in the
// renderer because only the sink knows what it wants.
//
// A sink that does not implement it receives every frame, which is what a video
// encoder needs: skipping one there would shorten the output.
type Selector interface {
	// Wants reports whether frame i will be used.
	Wants(i int) bool
}

// Config describes the video a Sink produces.
type Config struct {
	// OutputPath is the file to write. An existing file is overwritten.
	OutputPath string

	// Width, Height are the frame dimensions. Both must be positive and
	// EVEN -- see Validate.
	Width, Height int

	// FPS is the frame rate. It is a float64 because 29.97 has to be
	// expressible, and it is the same number the caller's timeline uses to
	// map a frame index to an instant, so the container's timestamps and the
	// dashboard's own clock cannot disagree about how long a frame lasts.
	FPS float64

	// CRF is H.264's quality knob, where LOWER is better and larger. 18-28 is
	// the useful range. Zero means DefaultCRF rather than "lossless" -- see
	// that constant.
	CRF int

	// Codec is the ffmpeg video encoder. Empty means DefaultCodec.
	Codec string
}

const (
	// DefaultCodec is libx264 rather than anything hardware-accelerated.
	// videofx uses hevc_videotoolbox, which is both faster and better, and is
	// also Darwin-only; fitdash is a cross-platform CLI whose output is meant
	// to be shared, so universal playability wins over encode speed. Paired
	// with yuv420p (see videoArgs) this plays everywhere.
	DefaultCodec = "libx264"

	// DefaultCRF is the quality used when Config.CRF is zero.
	//
	// Zero is remapped rather than passed through, and the remapping is the
	// point: -crf 0 means LOSSLESS to x264, so a caller who left the field
	// unset -- a struct literal in a test, a config that did not mention
	// quality -- would silently get a file many times larger than intended
	// and an encode several times slower. There is no plausible reading of an
	// unset field as a request for lossless. A caller who genuinely wants it
	// can say CRF: 0 only by way of a flag that names it, and today none
	// does.
	DefaultCRF = 20
)

// Validate reports why cfg cannot be used, or nil.
//
// Odd dimensions are REJECTED rather than rounded. yuv420p subsamples chroma
// by two in each direction and so requires even dimensions, and ffmpeg will
// fail on its own if handed odd ones -- but it fails deep in a subprocess with
// a message about a filter or a pixel format, long after the caller has
// forgotten what it asked for. Rounding is worse still: a render that silently
// came out 1919 pixels wide is a surprise the user finds much later, in a file
// whose dimensions do not match the flag they typed.
func (c Config) Validate() error {
	if c.OutputPath == "" {
		return fmt.Errorf("encode: no output path")
	}
	if c.Width <= 0 || c.Height <= 0 {
		return fmt.Errorf("encode: dimensions must be positive, got %dx%d", c.Width, c.Height)
	}
	if c.Width%2 != 0 || c.Height%2 != 0 {
		return fmt.Errorf("encode: width and height must both be even (yuv420p subsamples chroma by two), got %dx%d", c.Width, c.Height)
	}
	if c.FPS <= 0 {
		return fmt.Errorf("encode: fps must be positive, got %v", c.FPS)
	}
	if c.CRF < 0 || c.CRF > 51 {
		return fmt.Errorf("encode: crf must be in 0..51, got %d", c.CRF)
	}
	return nil
}

// checkFrame reports why img cannot be written at the given dimensions.
//
// The stride check is not redundant with the bounds check. A sub-image --
// img.SubImage(r).(*image.RGBA) -- has the bounds a caller asked for while
// keeping its PARENT's stride, so its Pix is not the tightly packed
// width*height*4 that rawvideo expects. Writing it anyway feeds ffmpeg the
// parent's trailing pixels on every row, producing a video that is sheared
// rather than one that fails, which is the kind of bug that gets diagnosed as
// a rendering problem.
func checkFrame(img *image.RGBA, w, h int) error {
	if img == nil {
		return fmt.Errorf("encode: nil frame")
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		return fmt.Errorf("encode: frame is %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	if img.Stride != w*4 {
		return fmt.Errorf("encode: frame stride is %d, want %d -- a sub-image keeps its parent's stride and cannot be written directly", img.Stride, w*4)
	}
	return nil
}
