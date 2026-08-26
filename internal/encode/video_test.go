package encode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// requireFFmpeg skips a test that needs the real binary.
//
// Everything else in this package is testable without it, and deliberately so:
// the argument shape is where this package's decisions live, and those are
// checked by videoArgs's own tests. Only the tests below actually need a
// subprocess, so a machine without ffmpeg still gets meaningful coverage
// rather than a package that cannot be tested at all.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; skipping the end-to-end encode")
	}
}

// probeStreams returns ffprobe's report for path, as one line of key=value.
func probeStreams(t *testing.T, path string) string {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH; cannot verify the output")
	}
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,nb_frames,pix_fmt,codec_name",
		"-of", "default=noprint_wrappers=1",
		path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return string(out)
}

// TestVideo_EndToEndProducesAPlayableFile is the only test here that runs
// ffmpeg. It asserts the output's own properties rather than that the call
// returned no error -- an encode that silently produced a zero-frame file
// would satisfy the latter completely.
func TestVideo_EndToEndProducesAPlayableFile(t *testing.T) {
	requireFFmpeg(t)

	const (
		w, h   = 64, 48
		frames = 12
		fps    = 12
	)
	out := filepath.Join(t.TempDir(), "out.mp4")
	sink, err := OpenVideo(context.Background(), Config{
		OutputPath: out, Width: w, Height: h, FPS: fps,
	})
	if err != nil {
		t.Fatalf("OpenVideo: %v", err)
	}
	defer sink.Close()

	// Vary the frames so the encoder cannot collapse the whole clip into one
	// keyframe and a run of "nothing changed" -- which would still produce a
	// file, and would make a frame count prove less than it appears to.
	for i := 0; i < frames; i++ {
		if err := sink.WriteFrame(i, solidFrame(w, h, uint8(i*20), uint8(255-i*20), 128)); err != nil {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty")
	}
	if got := sink.Frames(); got != frames {
		t.Errorf("Frames() = %d, want %d", got, frames)
	}

	probe := probeStreams(t, out)
	for _, want := range []string{
		"width=" + strconv.Itoa(w),
		"height=" + strconv.Itoa(h),
		"pix_fmt=yuv420p",
		"codec_name=h264",
		"nb_frames=" + strconv.Itoa(frames),
	} {
		if !strings.Contains(probe, want) {
			t.Errorf("ffprobe output is missing %q\ngot:\n%s", want, probe)
		}
	}
}

// TestVideo_WrongSizedFrameIsRejectedBeforeItReachesFFmpeg checks the guard
// fires in the sink rather than as a confusing subprocess failure. ffmpeg
// handed a short frame does not reject it -- it waits for the rest of what it
// thinks is the current frame and consumes the NEXT one to fill it, so the
// whole video is offset by a partial frame from that point on.
func TestVideo_WrongSizedFrameIsRejectedBeforeItReachesFFmpeg(t *testing.T) {
	requireFFmpeg(t)

	out := filepath.Join(t.TempDir(), "out.mp4")
	sink, err := OpenVideo(context.Background(), Config{
		OutputPath: out, Width: 64, Height: 48, FPS: 10,
	})
	if err != nil {
		t.Fatalf("OpenVideo: %v", err)
	}
	defer sink.Close()

	if err := sink.WriteFrame(0, solidFrame(32, 48, 0, 0, 0)); err == nil {
		t.Error("WriteFrame accepted a frame of the wrong width")
	}
	// The first error must stick: a caller looping over 45,000 frames should
	// get one actionable error, not one per frame, and must not be told that
	// a later correctly-sized frame succeeded after the stream was already
	// corrupted.
	if err := sink.WriteFrame(0, solidFrame(64, 48, 0, 0, 0)); err == nil {
		t.Error("WriteFrame succeeded after a previous write had failed; the first error must stick")
	}
	if err := sink.Close(); err == nil {
		t.Error("Close reported success after a write had failed")
	}
}

// TestVideo_CloseIsIdempotent covers the idiom the Sink contract prescribes:
// a deferred Close for the error paths plus an explicit one on the success
// path. The second call must return the first's verdict rather than a fresh
// error about an already-closed pipe.
func TestVideo_CloseIsIdempotent(t *testing.T) {
	requireFFmpeg(t)

	out := filepath.Join(t.TempDir(), "out.mp4")
	sink, err := OpenVideo(context.Background(), Config{
		OutputPath: out, Width: 32, Height: 32, FPS: 10,
	})
	if err != nil {
		t.Fatalf("OpenVideo: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := sink.WriteFrame(i, solidFrame(32, 32, 10, 20, 30)); err != nil {
			t.Fatal(err)
		}
	}
	if first := sink.Close(); first != nil {
		t.Fatalf("first Close: %v", first)
	}
	if second := sink.Close(); second != nil {
		t.Errorf("second Close returned %v, want the same nil verdict", second)
	}
}

// TestOpenVideo_InvalidConfigFailsBeforeSpawning checks the validation runs
// first. Spawning ffmpeg and letting it reject the geometry would report the
// problem as an encode failure, with ffmpeg's wording, rather than as the bad
// argument it is.
func TestOpenVideo_InvalidConfigFailsBeforeSpawning(t *testing.T) {
	_, err := OpenVideo(context.Background(), Config{
		OutputPath: filepath.Join(t.TempDir(), "o.mp4"), Width: 1919, Height: 1080, FPS: 30,
	})
	if err == nil {
		t.Fatal("OpenVideo accepted an odd width")
	}
	if !strings.Contains(err.Error(), "even") {
		t.Errorf("error should explain the even-dimensions requirement; got: %v", err)
	}
}

// TestOpenVideo_FailingFFmpegReportsItsOwnDiagnostic pins that ffmpeg's stderr
// reaches the caller. "exit status 1" on its own is not actionable, and it is
// how ffmpeg failures surface unless the output is captured and attached.
func TestOpenVideo_FailingFFmpegReportsItsOwnDiagnostic(t *testing.T) {
	requireFFmpeg(t)

	// An encoder that does not exist: ffmpeg starts, then exits complaining.
	out := filepath.Join(t.TempDir(), "o.mp4")
	sink, err := OpenVideo(context.Background(), Config{
		OutputPath: out, Width: 32, Height: 32, FPS: 10, Codec: "definitely_not_a_codec",
	})
	if err != nil {
		// Failing at Open is also acceptable, as long as it says why.
		if !strings.Contains(err.Error(), "definitely_not_a_codec") && !strings.Contains(err.Error(), "ffmpeg") {
			t.Errorf("error does not carry ffmpeg's diagnostic: %v", err)
		}
		return
	}
	// ffmpeg exits asynchronously, so the failure may surface at either the
	// write or the close. Both must carry the diagnostic.
	_ = sink.WriteFrame(0, solidFrame(32, 32, 0, 0, 0))
	closeErr := sink.Close()
	if closeErr == nil {
		t.Fatal("Close reported success for an unknown codec")
	}
	if !strings.Contains(closeErr.Error(), "ffmpeg:") {
		t.Errorf("error should carry ffmpeg's own stderr; got: %v", closeErr)
	}
}
