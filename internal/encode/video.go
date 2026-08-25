package encode

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// stderrCaptureLimit caps what is kept from ffmpeg's stderr. ffmpeg is run
// quietly, so anything it says is a signal and the whole of it normally fits;
// the cap exists only against a pathologically chatty failure.
const stderrCaptureLimit = 64 * 1024

// stderrCapture is an io.Writer keeping the first stderrCaptureLimit bytes.
//
// It needs a mutex because when an exec.Cmd's Stderr is not an *os.File, the
// exec package copies into it from a goroutine that runs until Wait returns.
// Writes and the eventual String read (always after Wait) would otherwise
// race.
type stderrCapture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (s *stderrCapture) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if remaining := stderrCaptureLimit - s.buf.Len(); remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
			s.truncated = true
		}
		s.buf.Write(p[:n])
	} else {
		s.truncated = len(p) > 0
	}
	// Always report the full write as accepted: this is a diagnostic sink,
	// not a real pipe, and a short write would make ffmpeg see a broken
	// stderr and possibly change behaviour for reasons unrelated to the video.
	return len(p), nil
}

func (s *stderrCapture) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := strings.TrimSpace(s.buf.String())
	if s.truncated {
		out += "\n... (truncated)"
	}
	return out
}

// PositionalPath makes p safe to hand ffmpeg as a BARE positional argument --
// an output filename following no flag of its own.
//
// ffmpeg parses any argument beginning with "-" as an option, so an ordinary
// file whose name starts with a dash is read as one. A file named "-y" or "-i"
// is not a theoretical concern: it would be silently interpreted as a flag and
// the render would fail, or worse, succeed while writing somewhere else.
// Prefixing "./" makes it an unambiguous relative path.
//
// Only bare positionals need this. A path passed as a flag's VALUE is already
// unambiguous, because the parser has consumed the flag and takes the next
// argument literally.
func PositionalPath(p string) string {
	if strings.HasPrefix(p, "-") {
		return "./" + p
	}
	return p
}

// videoArgs builds ffmpeg's argument list for cfg.
//
// Split out from OpenVideo so the argument shape is unit-testable without
// spawning anything -- the arguments are where this package's real decisions
// live, and a test that had to run ffmpeg to check them would be slow, would
// need the binary, and would report a mistake as an encode failure rather than
// as the wrong flag.
//
// The shape, and how it differs from videofx's overlay encoder, which every
// line of this is a deliberate departure from:
//
//   - Input is rawvideo on stdin and NOTHING ELSE. videofx has the source
//     clip as input 0 and composites onto it; here there is no source.
//   - -an. No source means no audio to map or copy.
//   - No -map_metadata, and this is a privacy decision rather than an
//     omission. videofx carries creation_time and the location tags forward
//     because its output is a re-encode of the user's own clip and losing them
//     would be data loss. fitdash's output is a NEW file, and stamping the
//     activity's coordinates into it would silently attach precise location
//     data to something the user is likely about to share. See the "Renders
//     are personal data" section of CLAUDE.md.
//   - libx264 + yuv420p rather than hevc_videotoolbox; see DefaultCodec.
//
// -movflags +faststart moves the moov atom to the front so the file starts
// playing before it has fully downloaded, which costs one extra pass at the
// end and is what makes the output reasonable to share.
func videoArgs(cfg Config) []string {
	codec := cfg.Codec
	if codec == "" {
		codec = DefaultCodec
	}
	crf := cfg.CRF
	if crf == 0 {
		crf = DefaultCRF
	}
	return []string{
		"-y",
		// The rawvideo input needs its geometry declared, because the format
		// carries none: it is bytes.
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", cfg.Width, cfg.Height),
		"-r", strconv.FormatFloat(cfg.FPS, 'f', -1, 64),
		"-i", "pipe:0",
		"-an",
		"-c:v", codec,
		"-preset", "medium",
		"-crf", strconv.Itoa(crf),
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		PositionalPath(cfg.OutputPath),
	}
}

// Video is a Sink that pipes raw frames to ffmpeg.
type Video struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stderr   *stderrCapture
	w, h     int
	frames   int
	writeErr error

	closeOnce sync.Once
	closeErr  error
}

// OpenVideo starts ffmpeg and returns a Sink writing to cfg.OutputPath.
//
// ffmpeg's absence is reported here, in terms of what the user can do about
// it, rather than being allowed to surface as exec's bare
// `exec: "ffmpeg": executable file not found in $PATH` from somewhere deeper.
func OpenVideo(ctx context.Context, cfg Config) (*Video, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("encode: ffmpeg not found on PATH; it is required to write a video (try `scripts/fd check-deps`): %w", err)
	}

	// -hide_banner -loglevel error -nostats: ffmpeg's normal chatter would
	// fill the stderr capture with progress lines, so anything captured is
	// actually a signal.
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostats"}, videoArgs(cfg)...)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	capture := &stderrCapture{}
	cmd.Stderr = capture

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("encode: opening ffmpeg stdin for %s: %w", cfg.OutputPath, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("encode: starting ffmpeg for %s: %w", cfg.OutputPath, err)
	}
	return &Video{cmd: cmd, stdin: stdin, stderr: capture, w: cfg.Width, h: cfg.Height}, nil
}

// WriteFrame pipes one frame's pixels to ffmpeg.
//
// The first write error is remembered and returned by every later call, and
// the frame is not re-sent. ffmpeg dying mid-render (a full disk, a bad codec
// argument) makes every subsequent write fail with EPIPE, and without this a
// caller looping over 45,000 frames gets 45,000 near-identical errors, only
// the first of which says anything about the cause.
func (v *Video) WriteFrame(img *image.RGBA) error {
	if v.writeErr != nil {
		return v.writeErr
	}
	if err := checkFrame(img, v.w, v.h); err != nil {
		v.writeErr = err
		return err
	}
	if _, err := v.stdin.Write(img.Pix); err != nil {
		// A write error here is nearly always ffmpeg having exited, in which
		// case its stderr says why and the EPIPE does not. Close collects
		// that; report it now so the caller does not have to.
		v.writeErr = fmt.Errorf("encode: writing frame %d to ffmpeg: %w%s", v.frames, err, stderrSuffix(v.stderr))
		return v.writeErr
	}
	v.frames++
	return nil
}

// Frames reports how many frames were accepted, for a caller's summary line.
func (v *Video) Frames() int { return v.frames }

// Close closes ffmpeg's stdin and waits for it to finish.
//
// Closing stdin is what tells ffmpeg the stream has ended; without it ffmpeg
// blocks on a read forever and Wait never returns. Both happen exactly once,
// so the deferred-Close-plus-explicit-Close idiom in Sink's doc comment is
// safe, and the second call returns the same verdict as the first rather than
// a confusing "already closed".
func (v *Video) Close() error {
	v.closeOnce.Do(func() {
		closeErr := v.stdin.Close()
		waitErr := v.cmd.Wait()
		switch {
		case waitErr != nil:
			// ffmpeg's own diagnostic is the useful part; "exit status 1" on
			// its own is not actionable.
			v.closeErr = fmt.Errorf("encode: ffmpeg failed after %d frames: %w%s", v.frames, waitErr, stderrSuffix(v.stderr))
		case closeErr != nil:
			v.closeErr = fmt.Errorf("encode: closing ffmpeg stdin: %w", closeErr)
		case v.writeErr != nil:
			// ffmpeg exited 0 but a frame never made it. Do not report
			// success: the file exists and is short.
			v.closeErr = v.writeErr
		}
	})
	return v.closeErr
}

// stderrSuffix renders captured ffmpeg output for appending to an error, or ""
// when it said nothing.
func stderrSuffix(c *stderrCapture) string {
	if s := c.String(); s != "" {
		return "\nffmpeg: " + s
	}
	return ""
}
