package encode

import (
	"image"
	"strings"
	"testing"
)

// solidFrame builds a tightly-packed w×h frame, the shape a real renderer
// produces.
func solidFrame(w, h int, r, g, b uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
	}
	return img
}

// TestConfig_ValidateRejectsOddDimensions pins the rejection rather than a
// rounding, which is the decision worth protecting.
//
// yuv420p subsamples chroma by two, so odd dimensions cannot be encoded. A
// future "helpful" change rounds them to even and produces a file whose size
// does not match the flag the user typed -- discovered much later, in the
// output, with nothing to explain it. Failing at the boundary where the number
// was supplied is the whole point.
func TestConfig_ValidateRejectsOddDimensions(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"a valid config", Config{OutputPath: "o.mp4", Width: 1920, Height: 1080, FPS: 30}, ""},
		{"odd width", Config{OutputPath: "o.mp4", Width: 1919, Height: 1080, FPS: 30}, "even"},
		{"odd height", Config{OutputPath: "o.mp4", Width: 1920, Height: 1081, FPS: 30}, "even"},
		{"both odd", Config{OutputPath: "o.mp4", Width: 1919, Height: 1081, FPS: 30}, "even"},
		{"zero width", Config{OutputPath: "o.mp4", Width: 0, Height: 1080, FPS: 30}, "positive"},
		{"negative height", Config{OutputPath: "o.mp4", Width: 1920, Height: -2, FPS: 30}, "positive"},
		{"no fps", Config{OutputPath: "o.mp4", Width: 1920, Height: 1080}, "fps"},
		{"no output path", Config{Width: 1920, Height: 1080, FPS: 30}, "output path"},
		{"crf out of range", Config{OutputPath: "o.mp4", Width: 1920, Height: 1080, FPS: 30, CRF: 52}, "crf"},
		// 29.97 is why FPS is a float64 rather than an int.
		{"a fractional frame rate", Config{OutputPath: "o.mp4", Width: 1920, Height: 1080, FPS: 29.97}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case c.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil, want an error mentioning %q", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Errorf("Validate() = %v, want an error mentioning %q", err, c.wantErr)
			}
		})
	}
}

// TestVideoArgs_ShapeAndDefaults asserts the properties of the argument list
// that carry a decision, by name rather than by position.
//
// Position-independent checks on purpose: an assertion that the whole slice
// equals a golden literal would fail on any harmless reordering and would say
// only "the args changed", which is the kind of test that gets updated by
// pasting the new value in. Each check below names the property it is for.
func TestVideoArgs_ShapeAndDefaults(t *testing.T) {
	args := videoArgs(Config{OutputPath: "out.mp4", Width: 1920, Height: 1080, FPS: 30})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-f rawvideo",   // the input format carries no geometry of its own
		"-pix_fmt rgba", // ... so the input pixel format is declared
		"-s 1920x1080",  // ... and so are the dimensions
		"-r 30",         // ... and the rate
		"-i pipe:0",     // stdin is the only input; there is no source clip
		"-an",           // no source means no audio
		"-c:v " + DefaultCodec,
		"-crf 20",          // DefaultCRF, because zero means lossless to x264
		"-pix_fmt yuv420p", // output format: universal playability
		"-movflags +faststart",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("videoArgs is missing %q\ngot: %s", want, joined)
		}
	}

	if got := args[len(args)-1]; got != "out.mp4" {
		t.Errorf("output path is %q and must be the final argument, got args ending %v", got, args[len(args)-3:])
	}

	// The privacy decision, asserted as an absence. videofx passes
	// -map_metadata to carry creation_time and location tags from its source;
	// fitdash has no source, and stamping the activity's coordinates into a
	// new file the user is about to share would attach precise location data
	// to it silently. A future change that adds metadata carrying should fail
	// here and be made to justify itself.
	for _, forbidden := range []string{"-map_metadata", "use_metadata_tags", "-metadata"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("videoArgs contains %q; fitdash deliberately writes no metadata into the output (see videoArgs's doc comment)", forbidden)
		}
	}
}

// TestVideoArgs_ZeroCRFBecomesTheDefault pins the remapping specifically,
// because it looks like a mistake and is not.
//
// -crf 0 is LOSSLESS to x264. A caller who simply did not mention quality --
// a struct literal in a test, a config built field by field -- would otherwise
// get a file many times larger and an encode several times slower, with
// nothing anywhere saying so.
func TestVideoArgs_ZeroCRFBecomesTheDefault(t *testing.T) {
	zero := strings.Join(videoArgs(Config{OutputPath: "o.mp4", Width: 2, Height: 2, FPS: 1}), " ")
	if !strings.Contains(zero, "-crf 20") {
		t.Errorf("an unset CRF must become DefaultCRF, not 0 (lossless); got: %s", zero)
	}
	// An explicitly chosen CRF must survive untouched.
	explicit := strings.Join(videoArgs(Config{OutputPath: "o.mp4", Width: 2, Height: 2, FPS: 1, CRF: 28}), " ")
	if !strings.Contains(explicit, "-crf 28") {
		t.Errorf("an explicit CRF must be passed through; got: %s", explicit)
	}
}

// TestPositionalPath_ProtectsDashLeadingNames is the argument-injection guard.
//
// ffmpeg parses any argument beginning with "-" as an option, so an output
// file named "-y" would be read as the overwrite flag and the real output
// path would be missing entirely. The fixture directory a test builds under
// t.TempDir() is always absolute, so this rule is invisible to every other
// test in the package -- it has to be checked directly.
func TestPositionalPath_ProtectsDashLeadingNames(t *testing.T) {
	cases := []struct{ in, want string }{
		{"out.mp4", "out.mp4"},
		{"/abs/out.mp4", "/abs/out.mp4"},
		{"./out.mp4", "./out.mp4"},
		{"-y", "./-y"},
		{"-i", "./-i"},
		{"-weird name.mp4", "./-weird name.mp4"},
		// A dash inside the name is harmless; only a leading one is parsed
		// as an option, and rewriting the rest would mangle paths a user
		// would otherwise recognise in an error message.
		{"my-run.mp4", "my-run.mp4"},
	}
	for _, c := range cases {
		if got := PositionalPath(c.in); got != c.want {
			t.Errorf("PositionalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// And the rewritten path must actually reach the argument list.
	args := videoArgs(Config{OutputPath: "-y", Width: 2, Height: 2, FPS: 1})
	if got := args[len(args)-1]; got != "./-y" {
		t.Errorf("videoArgs passed %q as the output; a dash-leading name must be made positional", got)
	}
}

// TestCheckFrame_RejectsASubImage pins the stride check.
//
// A sub-image has the bounds the caller asked for but keeps its PARENT's
// stride, so its Pix is not the tightly packed width*height*4 rawvideo
// expects. Writing it produces a SHEARED video rather than an error -- each
// row picking up the parent's trailing pixels -- which is diagnosed as a
// rendering bug and not as an encoding one. A bounds-only check passes this.
func TestCheckFrame_RejectsASubImage(t *testing.T) {
	parent := image.NewRGBA(image.Rect(0, 0, 100, 100))
	sub := parent.SubImage(image.Rect(0, 0, 50, 50)).(*image.RGBA)

	if sub.Bounds().Dx() != 50 || sub.Bounds().Dy() != 50 {
		t.Fatalf("precondition: sub-image should be 50x50, got %v", sub.Bounds())
	}
	if err := checkFrame(sub, 50, 50); err == nil {
		t.Error("checkFrame accepted a sub-image whose stride is its parent's; it would encode sheared")
	}

	if err := checkFrame(solidFrame(50, 50, 1, 2, 3), 50, 50); err != nil {
		t.Errorf("checkFrame rejected a well-formed frame: %v", err)
	}
	if err := checkFrame(solidFrame(50, 50, 1, 2, 3), 60, 50); err == nil {
		t.Error("checkFrame accepted a frame of the wrong width")
	}
	if err := checkFrame(nil, 50, 50); err == nil {
		t.Error("checkFrame accepted a nil frame")
	}
}
