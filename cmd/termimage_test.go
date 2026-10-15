package cmd

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// A picture is drawn only on a terminal known to show one, outside tmux and
// screen; --terminal is obeyed whatever the output; and only the choice
// that would surprise -- braille in tmux, on a terminal that could show a
// picture -- is explained.
func TestChooseTerminal(t *testing.T) {
	for _, c := range []struct {
		name     string
		mode     string
		env      map[string]string
		tty      bool
		want     string
		withNote bool
	}{
		{"iTerm2", termAuto, map[string]string{"TERM_PROGRAM": "iTerm.app"}, true, termITerm, false},
		{"iTerm2 over ssh", termAuto, map[string]string{"LC_TERMINAL": "iTerm2"}, true, termITerm, false},
		{"WezTerm", termAuto, map[string]string{"TERM_PROGRAM": "WezTerm"}, true, termITerm, false},
		{"kitty", termAuto, map[string]string{"TERM": "xterm-kitty"}, true, termKitty, false},
		{"kitty by window", termAuto, map[string]string{"KITTY_WINDOW_ID": "1"}, true, termKitty, false},
		{"Ghostty", termAuto, map[string]string{"TERM_PROGRAM": "ghostty"}, true, termKitty, false},
		{"Apple's Terminal", termAuto, map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, true, termBraille, false},
		{"into a pipe", termAuto, map[string]string{"TERM_PROGRAM": "iTerm.app"}, false, termBraille, false},
		{"iTerm2 in tmux", termAuto, map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX": "/tmp/tmux-501/default,1,0"}, true, termBraille, true},
		{"screen", termAuto, map[string]string{"LC_TERMINAL": "iTerm2", "TERM": "screen-256color"}, true, termBraille, true},
		{"kitty asked for, into a pipe", termKitty, nil, false, termKitty, false},
		{"braille asked for in iTerm2", termBraille, map[string]string{"TERM_PROGRAM": "iTerm.app"}, true, termBraille, false},
	} {
		how, note, err := chooseTerminal(c.mode, func(k string) string { return c.env[k] }, c.tty)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if how != c.want || (note != "") != c.withNote {
			t.Errorf("%s: drawn %s with note %q, want %s (note: %v)", c.name, how, note, c.want, c.withNote)
		}
	}
	if _, _, err := chooseTerminal("sixel", func(string) string { return "" }, true); err == nil {
		t.Error("--terminal sixel was accepted")
	}
}

// fakePNG is bytes standing in for a PNG, long enough to need several of
// kitty's chunks.
var fakePNG = bytes.Repeat([]byte("\x89PNG not really"), 1000)

// iTerm2's image is the whole file in one escape sequence, the width
// asked for in characters.
func TestITermImage(t *testing.T) {
	var b bytes.Buffer
	if err := writeITermImage(&b, fakePNG, 80); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("^\x1b]1337;File=inline=1;size=(\\d+);width=80;preserveAspectRatio=1:([A-Za-z0-9+/=]+)\a\n$").FindStringSubmatch(b.String())
	if m == nil {
		t.Fatalf("not an iTerm2 inline image: %q...", b.String()[:60])
	}
	if got, _ := base64.StdEncoding.DecodeString(m[2]); !bytes.Equal(got, fakePNG) || m[1] != "15000" {
		t.Errorf("the image's bytes or size (%s) are not the file's", m[1])
	}
}

// kitty's image is in chunks no longer than the protocol takes, the first
// saying what it is and asking for no answer, each but the last saying
// more follows; together they are the file.
func TestKittyImage(t *testing.T) {
	var b bytes.Buffer
	if err := writeKittyImage(&b, fakePNG, 80); err != nil {
		t.Fatal(err)
	}
	seqs := regexp.MustCompile("\x1b_G([^;]*);([^\x1b]*)\x1b\\\\").FindAllStringSubmatch(b.String(), -1)
	if len(seqs) < 3 {
		t.Fatalf("%d chunks for %d bytes, want several", len(seqs), len(fakePNG))
	}
	var data strings.Builder
	for i, s := range seqs {
		ctrl, chunk := s[1], s[2]
		if len(chunk) > kittyChunk {
			t.Errorf("chunk %d is %d long, over kitty's %d", i, len(chunk), kittyChunk)
		}
		if i == 0 && !strings.HasPrefix(ctrl, "a=T,f=100,q=2,c=80,") {
			t.Errorf("the first chunk says %q; it should transmit a PNG to show at 80 columns, unanswered", ctrl)
		}
		if more := strings.HasSuffix(ctrl, "m=1"); more != (i < len(seqs)-1) {
			t.Errorf("chunk %d of %d says m=%v", i, len(seqs), more)
		}
		data.WriteString(chunk)
	}
	if got, _ := base64.StdEncoding.DecodeString(data.String()); !bytes.Equal(got, fakePNG) {
		t.Error("the chunks together are not the file")
	}
}

// A terminal's picture covers the characters asked for at twice their
// pixels, in the PNG's proportions, no taller than the rows less the
// prompt's; a terminal that does not say its pixels is guessed at.
func TestTerminalPixels(t *testing.T) {
	ts := termSize{Cols: 100, Rows: 40, Width: 800, Height: 640}
	if w, h := terminalPixels(ts, 100, 40, 1); w != 1600 || h != 600 {
		t.Errorf("100 by 40 characters of 8 by 16 pixels: %d by %d, want 1600 by 600", w, h)
	}
	if _, h := terminalPixels(ts, 100, 10, 1); h != 2*16*9 {
		t.Errorf("a picture %d high in 10 rows, want no more than nine rows' %d", h, 2*16*9)
	}
	if w, _ := terminalPixels(termSize{}, 100, 40, 1); w != 2000 {
		t.Errorf("unknown pixels: %d wide, want 100 characters of ten at twice", w)
	}
	if w, h := terminalPixels(termSize{Cols: 200, Rows: 50, Width: 6000, Height: 3000}, 200, 50, 1); w != 3200 || h != 1200 {
		t.Errorf("a wide screen's chart is %d by %d, want held to 3200 by 1200", w, h)
	}
	if w, h := terminalPixels(ts, 100, 40, 2); h != w*plotImageHeightFor(2)/plotImageWidth {
		t.Errorf("two strips' picture is %d by %d, not in a two-strip PNG's proportions", w, h)
	}
}
