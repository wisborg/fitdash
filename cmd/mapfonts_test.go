package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
)

// A letter Go Mono has is written in Go Mono, whatever follows it, so a
// map of Latin names looks as it always did; one it lacks is asked of the
// fonts after it; one no font has is drawn as a box, and named.
func TestMapFonts_FallBackForLettersGoMonoLacks(t *testing.T) {
	defer func(f func() []string) { systemFontPaths = f }(systemFontPaths)
	systemFontPaths = func() []string { return nil }

	mono, err := loadMapFonts(nil)
	if err != nil {
		t.Fatal(err)
	}
	face := mono.faces()(20)
	if _, ok := face.GlyphAdvance('ก'); ok {
		t.Fatal("precondition: Go Mono has no Thai, the case this exists for")
	}
	font.MeasureString(face, "กรุงเทพ")
	var buf bytes.Buffer
	writeMissingLetters(&buf, mono)
	if !strings.Contains(buf.String(), "ก") || !strings.Contains(buf.String(), "--font") {
		t.Errorf("missing letters reported as %q; want the Thai letters named and --font suggested", buf.String())
	}

	// A --font comes after Go Mono: Go Regular stands in for one here, and
	// has letters Go Mono has not -- but for "A" Go Mono's is still used,
	// so its advance is Go Mono's fixed width.
	path := filepath.Join(t.TempDir(), "extra.ttf")
	if err := os.WriteFile(path, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	both, err := loadMapFonts([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := mono.faces()(20).GlyphAdvance('A')
	b, _ := both.faces()(20).GlyphAdvance('A')
	if a != b {
		t.Errorf("'A' is %v wide with a --font after Go Mono, %v without; Go Mono must come first", b, a)
	}
	if _, err := loadMapFonts([]string{filepath.Join(t.TempDir(), "none.ttf")}); err == nil {
		t.Error("a --font that cannot be read was accepted")
	}
}

// Each set of faces is its own -- a flyover's tiles draw in parallel, each
// with a set -- and the letters missing from any are reported once.
func TestMapFonts_FreshSetsAndMissingOnce(t *testing.T) {
	defer func(f func() []string) { systemFontPaths = f }(systemFontPaths)
	systemFontPaths = func() []string { return nil }
	mf, err := loadMapFonts(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mf.faces()(16), mf.faces()(16)
	if a == b {
		t.Error("two sets share a face; a face is one goroutine's to draw with")
	}
	font.MeasureString(a, "ก")
	font.MeasureString(b, "ก")
	if got := mf.missingLetters(); len(got) != 1 || got[0] != 'ก' {
		t.Errorf("missing %q, want the one letter once", string(got))
	}
}
