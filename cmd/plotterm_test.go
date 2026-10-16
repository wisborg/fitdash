package cmd

import (
	"testing"

	"github.com/wisborg/output/chart/terminal"
)

// A terminal's picture covers the characters asked for at twice their
// pixels, in the PNG's proportions, no taller than the rows less the
// prompt's; a terminal that does not say its pixels is guessed at.
func TestTerminalPixels(t *testing.T) {
	ts := terminal.Size{Cols: 100, Rows: 40, Width: 800, Height: 640}
	if w, h := terminalPixels(ts, 100, 40, 1); w != 1600 || h != 600 {
		t.Errorf("100 by 40 characters of 8 by 16 pixels: %d by %d, want 1600 by 600", w, h)
	}
	if _, h := terminalPixels(ts, 100, 10, 1); h != 2*16*9 {
		t.Errorf("a picture %d high in 10 rows, want no more than nine rows' %d", h, 2*16*9)
	}
	if w, _ := terminalPixels(terminal.Size{}, 100, 40, 1); w != 2000 {
		t.Errorf("unknown pixels: %d wide, want 100 characters of ten at twice", w)
	}
	if w, h := terminalPixels(terminal.Size{Cols: 200, Rows: 50, Width: 6000, Height: 3000}, 200, 50, 1); w != 3200 || h != 1200 {
		t.Errorf("a wide screen's chart is %d by %d, want held to 3200 by 1200", w, h)
	}
	if w, h := terminalPixels(ts, 100, 40, 2); h != w*plotImageHeightFor(2)/plotImageWidth {
		t.Errorf("two strips' picture is %d by %d, not in a two-strip PNG's proportions", w, h)
	}
}
