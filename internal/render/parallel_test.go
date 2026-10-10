package render

import (
	"bytes"
	"context"
	"image"
	"runtime"
	"testing"

	"github.com/wisborg/fitdash/internal/panel"
)

// captureSink keeps a copy of every frame written, and the order they came.
type captureSink struct {
	frames map[int][]byte
	order  []int
	want   func(i int) bool
}

func (s *captureSink) WriteFrame(i int, img *image.RGBA) error {
	s.order = append(s.order, i)
	if s.want == nil || s.want(i) {
		s.frames[i] = bytes.Clone(img.Pix)
	}
	return nil
}

func (s *captureSink) Close() error { return nil }

// selectiveCapture is captureSink as an encode.Selector.
type selectiveCapture struct{ *captureSink }

func (s selectiveCapture) Wants(i int) bool { return s.want(i) }

// runFrames draws every frame through Run, and each again through Render, the
// one-shot path; they must agree to the byte, frame by frame, whatever order
// Run's workers finished them in, and reach the sink in order.
func runFrames(t *testing.T, r *Renderer, ctx *panel.Context, sink interface {
	WriteFrame(int, *image.RGBA) error
	Close() error
}, got *captureSink) {
	t.Helper()
	// Several at once even on a machine with one core: the test is about
	// frames finishing out of order, which goroutines do there too.
	r.SetConcurrency(4)
	var progressed []int
	if err := Run(context.Background(), r, sink, func(i, n int) { progressed = append(progressed, i) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for k := 1; k < len(got.order); k++ {
		if got.order[k] <= got.order[k-1] {
			t.Fatalf("frame %d reached the sink after frame %d; frames must be written in order", got.order[k], got.order[k-1])
		}
	}
	if len(progressed) != r.Frames() {
		t.Fatalf("progress told %d times, want once for each of %d frames", len(progressed), r.Frames())
	}
	for k, i := range progressed {
		if i != k+1 {
			t.Fatalf("progress told %d in place %d; it must count up one frame at a time", i, k)
		}
	}
	for i, pix := range got.frames {
		whole := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(whole, r.Frame(i)); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}
		if !bytes.Equal(whole.Pix, pix) {
			t.Errorf("frame %d drawn by Run differs from Render's", i)
		}
	}
}

// Frames drawn several at once are the frames drawn one at a time: every
// frame of the real landscape layout, whose panels each promise that
// Dynamic does not change their Painter -- the promise that makes it safe.
func TestRun_FramesDrawnAtOnceAreTheFramesDrawnInTurn(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 640, 360, 10)
	r, err := New(ctx, panel.LandscapeLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := &captureSink{frames: map[int][]byte{}}
	runFrames(t, r, ctx, got, got)
	if len(got.frames) != r.Frames() || len(got.order) != r.Frames() {
		t.Errorf("%d frames captured of %d written, want %d", len(got.frames), len(got.order), r.Frames())
	}
}

// The same under the highlight wash, whose bases are blended per frame, and
// for a sink that wants only some frames: those it does not want are still
// offered in order, and only those it does are drawn.
func TestRun_FramesDrawnAtOnceUnderWashAndSelection(t *testing.T) {
	ctx, _ := buildHighlightContext(t, 320, 180, 10)
	ctx.HighlightStyle = panel.HighlightStyleWash
	r, err := New(ctx, highlightMarkerLayout(
		markerPanel{name: "a", accept: true},
		markerPanel{name: "b", accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := &captureSink{frames: map[int][]byte{}, want: func(i int) bool { return i%3 == 0 }}
	runFrames(t, r, ctx, selectiveCapture{got}, got)
	if want := (r.Frames() + 2) / 3; len(got.frames) != want {
		t.Errorf("%d frames captured, want %d", len(got.frames), want)
	}
}

// Auto is a frame a core, held to the pixels of eight 1080p frames in flight.
func TestAutoConcurrency(t *testing.T) {
	cores := runtime.GOMAXPROCS(0)
	if got, want := AutoConcurrency(1920, 1080), min(cores, 8); got != want {
		t.Errorf("1080p: %d, want %d", got, want)
	}
	if got, want := AutoConcurrency(3840, 2160), min(cores, 2); got != want {
		t.Errorf("4K: %d, want %d", got, want)
	}
	if got := AutoConcurrency(16000, 16000); got != 1 {
		t.Errorf("a frame larger than the budget: %d, want 1", got)
	}
}
