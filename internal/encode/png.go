package encode

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// PNGFrames is a Sink that writes selected frames as PNG files instead of
// encoding a video.
//
// It exists so `--frames` is a SINK SWAP and nothing more: the render path
// above it is byte-for-byte the same one that produces a video, which is what
// makes the fast visual loop a trustworthy proxy for the real render. A
// separate "preview" code path would be free to diverge from the real one, and
// would diverge exactly when someone was relying on it to debug a panel.
//
// Frames not selected are consumed and discarded, so a caller renders the same
// sequence either way. That is deliberate rather than wasteful at this layer:
// skipping the render of unselected frames is the caller's optimisation to
// make, and doing it here would mean the frames that ARE written had been
// produced by a different traversal than a full render performs.
type PNGFrames struct {
	dir      string
	want     map[int]bool
	index    int
	written  []string
	writeErr error

	closeOnce sync.Once
	closeErr  error
}

// OpenPNGFrames returns a Sink writing the frames whose indices appear in at,
// as dir/frame-%06d.png.
//
// Duplicate indices are collapsed and the order of at does not matter: frames
// arrive in presentation order regardless, so treating at as a set rather than
// a sequence is the only reading that cannot mislead.
func OpenPNGFrames(dir string, at []int) (*PNGFrames, error) {
	if dir == "" {
		return nil, fmt.Errorf("encode: no output directory for frames")
	}
	if len(at) == 0 {
		return nil, fmt.Errorf("encode: no frame indices requested")
	}
	want := make(map[int]bool, len(at))
	for _, i := range at {
		if i < 0 {
			return nil, fmt.Errorf("encode: frame index %d is negative", i)
		}
		want[i] = true
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("encode: creating %s: %w", dir, err)
	}
	return &PNGFrames{dir: dir, want: want}, nil
}

// WriteFrame writes img when its index was requested, and otherwise discards
// it. As with Video, the first error is remembered and returned by every later
// call rather than repeated once per frame.
func (p *PNGFrames) WriteFrame(img *image.RGBA) error {
	if p.writeErr != nil {
		return p.writeErr
	}
	i := p.index
	p.index++
	if !p.want[i] {
		return nil
	}
	if img == nil {
		p.writeErr = fmt.Errorf("encode: nil frame at index %d", i)
		return p.writeErr
	}
	name := filepath.Join(p.dir, fmt.Sprintf("frame-%06d.png", i))
	f, err := os.Create(name)
	if err != nil {
		p.writeErr = fmt.Errorf("encode: creating %s: %w", name, err)
		return p.writeErr
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		p.writeErr = fmt.Errorf("encode: writing %s: %w", name, err)
		return p.writeErr
	}
	if err := f.Close(); err != nil {
		p.writeErr = fmt.Errorf("encode: closing %s: %w", name, err)
		return p.writeErr
	}
	p.written = append(p.written, name)
	return nil
}

// Written returns the paths written, in the order they were written, so a
// caller can tell the user where to look.
func (p *PNGFrames) Written() []string { return p.written }

// Close reports whether every requested frame was written.
//
// A requested index beyond the end of the render is an ERROR, not a silent
// nothing. Asking for the frame at 40 minutes of a 25-minute activity is a
// mistake the user wants told about immediately -- the alternative is an empty
// output directory and no explanation, which reads as a broken program.
func (p *PNGFrames) Close() error {
	p.closeOnce.Do(func() {
		if p.writeErr != nil {
			p.closeErr = p.writeErr
			return
		}
		var missing []int
		for i := range p.want {
			if i >= p.index {
				missing = append(missing, i)
			}
		}
		if len(missing) > 0 {
			sort.Ints(missing)
			p.closeErr = fmt.Errorf("encode: requested frame(s) %v were never reached; the render produced %d frames", missing, p.index)
		}
	})
	return p.closeErr
}
