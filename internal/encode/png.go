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
// It implements Selector, so a renderer that asks can skip drawing the frames
// this sink would discard. Without that, --frames rendered every frame of the
// activity to write five images: a 25-minute run at 30 fps drew and threw away
// 46,600 frames, taking 36 seconds to produce 5 PNGs, while both the README
// and scripts/fd advertised it as the fast visual loop. It was no faster than a
// full render minus the ffmpeg pipe.
//
// Selecting frames does not change what a written frame CONTAINS. Every frame
// is drawn the same way from the same per-frame state, and the static layer is
// rasterized once regardless, so a frame written here is identical to the one
// the video would have held -- which is what makes the preview trustworthy.
type PNGFrames struct {
	dir      string
	want     map[int]bool
	highest  int
	seen     map[int]bool
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
	return &PNGFrames{dir: dir, want: want, seen: map[int]bool{}, highest: -1}, nil
}

// Wants reports whether frame i was requested, so a renderer can skip drawing
// the rest.
//
// It also RECORDS that frame i exists, which is why it takes a pointer
// receiver and is not the pure predicate it looks like. Once a renderer starts
// skipping frames, WriteFrame no longer sees them, so the only remaining
// evidence of how long the render was is what the sink was asked about --
// and without it Close could not tell a user that their requested frame 99
// was beyond a render of 5.
func (p *PNGFrames) Wants(i int) bool {
	p.note(i)
	return p.want[i]
}

// note records that frame i was reached, from either path.
func (p *PNGFrames) note(i int) {
	if i > p.highest {
		p.highest = i
	}
	p.seen[i] = true
}

// FrameName is the path PNGFrames writes frame i to.
//
// Exported so a caller can report which files a render WOULD write without
// running it (--dry-run) and name the same files the sink then creates. The
// alternative -- spelling "frame-%06d.png" a second time at that call site --
// is a format string that can drift from this one, and the drift would show
// up as a dry run confidently naming files that never appear under those
// names.
func FrameName(dir string, i int) string {
	return filepath.Join(dir, fmt.Sprintf("frame-%06d.png", i))
}

// WriteFrame writes img when its index was requested, and otherwise discards
// it. As with Video, the first error is remembered and returned by every later
// call rather than repeated once per frame.
//
// It still discards an unwanted frame rather than rejecting it, because a
// renderer that ignores Selector is not wrong -- only slower -- and a sink that
// refused frames it had not asked for would break it.
func (p *PNGFrames) WriteFrame(i int, img *image.RGBA) error {
	if p.writeErr != nil {
		return p.writeErr
	}
	p.note(i)
	if !p.want[i] {
		return nil
	}
	if img == nil {
		p.writeErr = fmt.Errorf("encode: nil frame at index %d", i)
		return p.writeErr
	}
	name := FrameName(p.dir, i)
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
			if !p.seen[i] {
				missing = append(missing, i)
			}
		}
		if len(missing) > 0 {
			sort.Ints(missing)
			p.closeErr = fmt.Errorf("encode: requested frame(s) %v were never reached; the render produced %d frames", missing, p.highest+1)
		}
	})
	return p.closeErr
}
