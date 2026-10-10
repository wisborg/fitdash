package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"

	"github.com/wisborg/fitdash/internal/panel"
)

// A letter Go Mono has is written in Go Mono, whatever follows it, so the
// dashboard and a map of Latin names look as they always did; one it lacks
// is asked of the fonts after it; one no font has is drawn as a box, and
// named in the summary.
func TestFonts_FallBackForLettersGoMonoLacks(t *testing.T) {
	defer func(f func() []string) { systemFontPaths = f }(systemFontPaths)
	systemFontPaths = func() []string { return nil }

	none, err := loadFonts(nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("no --font and no system fonts loaded %d fonts, %v", len(none), err)
	}
	mono, err := panel.NewFaceCacheWith(none...)
	if err != nil {
		t.Fatal(err)
	}
	face := faceSource(mono)(20)
	if _, ok := face.GlyphAdvance('ก'); ok {
		t.Fatal("precondition: Go Mono has no Thai, the case this exists for")
	}

	// A fallback with a letter Go Mono lacks: Go Regular has the macron
	// letters of Latin Extended Additional that Go Mono does not, which a
	// real Thai font stands for here without depending on this machine.
	reg, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	both, err := panel.NewFaceCacheWith(reg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := faceSource(mono)(20).GlyphAdvance('A')
	b, _ := faceSource(both)(20).GlyphAdvance('A')
	if a != b {
		t.Errorf("'A' is %v wide with a fallback, %v without; Go Mono must come first", b, a)
	}

	font.MeasureString(faceSource(both)(20), "กรุงเทพ")
	var buf bytes.Buffer
	writeMissingLetters(&buf, both)
	if !strings.Contains(buf.String(), "ก") || !strings.Contains(buf.String(), "--font") {
		t.Errorf("missing letters reported as %q; want the Thai letters named and --font suggested", buf.String())
	}

	path := filepath.Join(t.TempDir(), "extra.ttf")
	if err := os.WriteFile(path, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := loadFonts([]string{path}); err != nil || len(got) != 1 {
		t.Errorf("a --font loaded %d fonts, %v; want it", len(got), err)
	}
	if _, err := loadFonts([]string{filepath.Join(t.TempDir(), "none.ttf")}); err == nil {
		t.Error("a --font that cannot be read was accepted")
	}
}

// Each Fresh copy draws with faces of its own -- frames and a flyover's tiles
// draw at the same time -- and a letter missing from any is reported once.
func TestFonts_FreshCopiesAndMissingOnce(t *testing.T) {
	reg, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	fonts, err := panel.NewFaceCacheWith(reg)
	if err != nil {
		t.Fatal(err)
	}
	a, b := faceSource(fonts)(16), faceSource(fonts.Fresh())(16)
	if a == b {
		t.Error("a Fresh copy shares a face; a face is one goroutine's to draw with")
	}
	font.MeasureString(a, "ก")
	font.MeasureString(b, "ก")
	if got := fonts.Missing(); len(got) != 1 || got[0] != 'ก' {
		t.Errorf("missing %q, want the one letter once", string(got))
	}
}
