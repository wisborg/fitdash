package encode

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPNGFrames_WritesOnlyTheRequestedIndices checks that selection actually
// selects -- that unrequested frames are consumed and discarded rather than
// written, and that the written ones are named by their index in the render.
func TestPNGFrames_WritesOnlyTheRequestedIndices(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenPNGFrames(dir, []int{0, 3, 7})
	if err != nil {
		t.Fatalf("OpenPNGFrames: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := sink.WriteFrame(solidFrame(4, 4, uint8(i), 0, 0)); err != nil {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"frame-000000.png", "frame-000003.png", "frame-000007.png"}
	if len(names) != len(want) {
		t.Fatalf("wrote %v, want exactly %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("file %d is %q, want %q", i, names[i], w)
		}
	}
}

// TestPNGFrames_WritesTheFrameItWasGiven guards against the selection working
// while the CONTENT is wrong -- an off-by-one that writes frame 4 into
// frame-000003.png would pass the name test above completely.
//
// Each frame is filled with its own index as a red value, so the pixel read
// back identifies which frame was actually written.
func TestPNGFrames_WritesTheFrameItWasGiven(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenPNGFrames(dir, []int{5})
	if err != nil {
		t.Fatalf("OpenPNGFrames: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := sink.WriteFrame(solidFrame(4, 4, uint8(100+i), 0, 0)); err != nil {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(filepath.Join(dir, "frame-000005.png"))
	if err != nil {
		t.Fatalf("opening the written frame: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	if got, want := uint8(r>>8), uint8(105); got != want {
		t.Errorf("frame-000005.png holds red=%d, want %d -- the wrong frame was written", got, want)
	}
}

// TestPNGFrames_UnreachedIndexIsAnError pins the failure rather than a silent
// nothing.
//
// Asking for the frame at 40 minutes of a 25-minute activity is a mistake the
// user wants told about at once. The alternative -- an empty output directory
// and no explanation -- reads as a broken program, and the user's next move is
// to debug the renderer.
func TestPNGFrames_UnreachedIndexIsAnError(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenPNGFrames(dir, []int{1, 99})
	if err != nil {
		t.Fatalf("OpenPNGFrames: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := sink.WriteFrame(solidFrame(4, 4, 0, 0, 0)); err != nil {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
	err = sink.Close()
	if err == nil {
		t.Fatal("Close returned nil for a frame index the render never reached")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the error must name the unreached index; got: %v", err)
	}
	// It must also say how many frames there actually were, or the user has
	// no way to pick a valid one.
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("the error must report the frame count; got: %v", err)
	}
}

// TestPNGFrames_CloseIsIdempotent covers the deferred-Close-plus-explicit-
// Close idiom the Sink contract prescribes: the second call must return the
// same verdict, not a different error about being closed already.
func TestPNGFrames_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenPNGFrames(dir, []int{0})
	if err != nil {
		t.Fatalf("OpenPNGFrames: %v", err)
	}
	if err := sink.WriteFrame(solidFrame(2, 2, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	first := sink.Close()
	second := sink.Close()
	if first != nil {
		t.Fatalf("first Close: %v", first)
	}
	if second != nil {
		t.Errorf("second Close returned %v, want the same nil verdict as the first", second)
	}

	// And on the failing path.
	bad, err := OpenPNGFrames(t.TempDir(), []int{42})
	if err != nil {
		t.Fatal(err)
	}
	e1, e2 := bad.Close(), bad.Close()
	if e1 == nil {
		t.Fatal("expected an error for an unreached index")
	}
	if e1.Error() != e2.Error() {
		t.Errorf("Close is not idempotent on the error path: %v then %v", e1, e2)
	}
}

// TestOpenPNGFrames_RejectsUnusableRequests keeps the constructor honest about
// inputs that would otherwise fail confusingly much later.
func TestOpenPNGFrames_RejectsUnusableRequests(t *testing.T) {
	if _, err := OpenPNGFrames("", []int{0}); err == nil {
		t.Error("accepted an empty directory")
	}
	if _, err := OpenPNGFrames(t.TempDir(), nil); err == nil {
		t.Error("accepted an empty index list; there would be nothing to write")
	}
	if _, err := OpenPNGFrames(t.TempDir(), []int{-1}); err == nil {
		t.Error("accepted a negative frame index")
	}
}

// TestPNGFrames_DuplicateIndicesCollapse pins that `at` is read as a set. A
// caller assembling indices from several flags can repeat one, and writing the
// same file twice or erroring would both be worse than ignoring it.
func TestPNGFrames_DuplicateIndicesCollapse(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenPNGFrames(dir, []int{2, 2, 0, 2})
	if err != nil {
		t.Fatalf("OpenPNGFrames: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := sink.WriteFrame(solidFrame(2, 2, 0, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(sink.Written()); got != 2 {
		t.Errorf("wrote %d files (%v), want 2 -- duplicate indices must collapse", got, sink.Written())
	}
}

// compile-time proof that both sinks satisfy the interface the renderer will
// hold them by. Cheap, and it fails at build time rather than at a call site.
var (
	_ Sink = (*PNGFrames)(nil)
	_ Sink = (*Video)(nil)
)
