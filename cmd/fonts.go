package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"

	"github.com/wisborg/fitdash/internal/panel"
)

// The fonts a render writes its text in: the dashboard's Go Mono first, then
// each --font, then whichever of a few well-known system fonts this machine
// has -- each letter from the first of them that can write it, through
// osmbase's Fallback (see panel.NewFaceCacheWith). One list for all of it:
// the readouts, a --label's name, the map's names.
//
// Go Mono has Latin, Greek and Cyrillic and nothing else, and a flight over
// Thailand wrote every name there as a row of boxes. This is course's answer
// to the same thing, with course's flags and its list of system fonts:
// fitdash ships no font beyond the one golang.org/x/image already carries,
// so nothing new is bundled and no new licence comes with it.

// systemFontPaths is systemFonts, or none, for a test that must not depend
// on the fonts of the machine it runs on.
var systemFontPaths = systemFonts

// systemFonts are fonts with wide coverage that operating systems ship, most
// likely first: course's list. Only those present are used, and a system
// with none of them writes what Go Mono can.
func systemFonts() []string {
	switch runtime.GOOS {
	case "darwin":
		out := []string{
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
			"/Library/Fonts/Arial Unicode.ttf",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/System/Library/Fonts/AppleSDGothicNeo.ttc",
			"/System/Library/Fonts/Supplemental/KefaIII.ttf", // Ethiopic
			"/System/Library/Fonts/Supplemental/Kailasa.ttc", // Tibetan
		}
		// One Sangam MN font per South and Southeast Asian script.
		sangam, _ := filepath.Glob("/System/Library/Fonts/Supplemental/*Sangam MN.tt?")
		out = append(out, sangam...)
		// The small Noto fonts macOS ships, one script each, for the scripts
		// Arial Unicode lacks; after it, so they are asked only for letters
		// nothing before them has.
		noto, _ := filepath.Glob("/System/Library/Fonts/Supplemental/NotoSans*-Regular.ttf")
		return append(out, noto...)
	case "windows":
		dir := os.Getenv("WINDIR") + `\Fonts\`
		return []string{dir + "arialuni.ttf", dir + "Nirmala.ttf", dir + "LeelawUI.ttf", dir + "msyh.ttc", dir + "malgun.ttf", dir + "seguisym.ttf"}
	}
	var out []string
	for _, dir := range []string{"/usr/share/fonts/truetype/noto/", "/usr/share/fonts/noto/", "/usr/share/fonts/opentype/noto/", "/usr/share/fonts/google-noto/"} {
		for _, f := range []string{"NotoSans-Regular.ttf", "NotoSansThai-Regular.ttf", "NotoSansDevanagari-Regular.ttf",
			"NotoSansBengali-Regular.ttf", "NotoSansTamil-Regular.ttf", "NotoSansArabic-Regular.ttf",
			"NotoSansHebrew-Regular.ttf", "NotoSansCJK-Regular.ttc"} {
			out = append(out, dir+f)
		}
	}
	return append(out, "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf")
}

// loadFonts parses the fonts to fall back on: extra, then the system fonts
// present -- 113 of them on a Mac, in about a fifth of a second. A --font
// that cannot be read is an error -- somebody named it -- and a system font
// that cannot be read is passed over.
func loadFonts(extra []string) ([]*opentype.Font, error) {
	var list []*opentype.Font
	for _, path := range extra {
		f, err := parseFont(path)
		if err != nil {
			return nil, fmt.Errorf("--font %s: %w", path, err)
		}
		list = append(list, f)
	}
	for _, path := range systemFontPaths() {
		if f, err := parseFont(path); err == nil {
			list = append(list, f)
		}
	}
	return list, nil
}

// parseFont reads a TrueType or OpenType font, or the first font of a
// collection.
func parseFont(path string) (*opentype.Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".ttc") || strings.HasPrefix(string(data), "ttcf") {
		c, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, err
		}
		return c.Font(0)
	}
	return opentype.Parse(data)
}

// faceSource is the faces of fonts as a function of their size in pixels:
// nil for a size that will not build, which a map takes as "the base face".
func faceSource(fonts *panel.FaceCache) func(px float64) font.Face {
	if fonts == nil {
		return nil
	}
	return func(px float64) font.Face {
		f, err := fonts.Face(px)
		if err != nil {
			return nil
		}
		return f
	}
}

// writeMissingLetters tells the user the render has text it could not
// write, and how to get it written.
func writeMissingLetters(w io.Writer, fonts *panel.FaceCache) {
	missing := fonts.Missing()
	if len(missing) == 0 {
		return
	}
	sample := missing
	if len(sample) > 12 {
		sample = sample[:12]
	}
	fmt.Fprintf(w, "fonts: %d letters in the map's names or the dashboard's text are in no font this machine has, drawn as boxes: %s\n", len(missing), string(sample))
	fmt.Fprintf(w, "       pass --font with a font that has them, or --lang en for the map's names in English where the map has them\n")
}
