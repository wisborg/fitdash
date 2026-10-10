package cmd

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"

	osm "github.com/wisborg/osmbase/render"
)

// mapFonts are the fonts a local basemap writes its names in: fitdash's own
// face first, then each --font, then whichever of a few well-known system
// fonts this machine has -- each letter from the first of them that can
// write it, through osmbase's Fallback.
//
// The Go Mono face the dashboard is drawn in has Latin, Greek and Cyrillic
// and nothing else, and a flight over Thailand wrote every name there as a
// row of boxes. This is course's answer to the same thing, with course's
// flags and its list of system fonts: fitdash ships no font beyond the one
// golang.org/x/image already carries, so nothing new is bundled and no new
// licence comes with it. It is the map's names only; the dashboard's own text
// stays in Go Mono.
type mapFonts struct {
	fonts []*opentype.Font

	mu   sync.Mutex
	made []*osm.Fallback // every face made, for missingLetters
}

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

// loadMapFonts parses Go Mono, then extra, then the system fonts present. A
// --font that cannot be read is an error -- somebody named it -- and a system
// font that cannot be read is passed over.
func loadMapFonts(extra []string) (*mapFonts, error) {
	mono, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, fmt.Errorf("render: parsing the built-in font: %w", err)
	}
	list := []*opentype.Font{mono}
	for _, path := range extra {
		f, err := parseFont(path)
		if err != nil {
			return nil, fmt.Errorf("render: --font %s: %w", path, err)
		}
		list = append(list, f)
	}
	for _, path := range systemFontPaths() {
		if f, err := parseFont(path); err == nil {
			list = append(list, f)
		}
	}
	return &mapFonts{fonts: list}, nil
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

// faces is a set of faces of every size asked for, each a Fallback over all
// the fonts: one goroutine's to draw with, as a face is. A flyover's tiles
// each have a set of their own; see tilemap.Local.FreshLabels.
func (m *mapFonts) faces() func(px float64) font.Face {
	var mu sync.Mutex
	sized := map[int]font.Face{}
	return func(px float64) font.Face {
		key := int(math.Round(max(px, 1)))
		mu.Lock()
		defer mu.Unlock()
		if f, ok := sized[key]; ok {
			return f
		}
		var fs []font.Face
		for _, ttf := range m.fonts {
			f, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: float64(key), DPI: 72, Hinting: font.HintingFull})
			if err == nil {
				fs = append(fs, f)
			}
		}
		fb := osm.NewFallback(fs...)
		if fb == nil {
			sized[key] = nil
			return nil
		}
		m.mu.Lock()
		m.made = append(m.made, fb)
		m.mu.Unlock()
		sized[key] = fb
		return fb
	}
}

// missingLetters are the letters asked of any face made that no font had.
func (m *mapFonts) missingLetters() []rune {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[rune]bool{}
	var out []rune
	for _, f := range m.made {
		for _, r := range f.Missing() {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// writeMissingLetters tells the user the map has names it could not write,
// and how to get them written.
func writeMissingLetters(w io.Writer, m *mapFonts) {
	missing := m.missingLetters()
	if len(missing) == 0 {
		return
	}
	sample := missing
	if len(sample) > 12 {
		sample = sample[:12]
	}
	fmt.Fprintf(w, "basemap: %d letters in the map's names are in no font this machine has, drawn as boxes: %s\n", len(missing), string(sample))
	fmt.Fprintf(w, "         pass --font with a font that has them, or --lang en for the names in English where the map has them\n")
}
