package cmd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

// termSize is a terminal's size: in characters, and in pixels where it
// says.
type termSize struct {
	Cols, Rows    int
	Width, Height int
}

// The ways plot can draw in the terminal: an image in iTerm2's protocol or
// kitty's, or braille dots, which any terminal shows.
const (
	termAuto    = "auto"
	termITerm   = "iterm"
	termKitty   = "kitty"
	termBraille = "braille"
)

// chooseTerminal is how a chart is drawn in the terminal for --terminal
// mode, given the environment and whether the output is a terminal at all,
// and a note for the reader where the choice would surprise them: in tmux,
// a terminal that shows pictures is drawn braille, and its user may not
// know why. Elsewhere braille is what the terminal can show, and saying so
// on every chart would be noise.
//
// auto draws an image only where it will be seen as one: on a terminal
// rather than into a pipe or a file, where the image would arrive as a
// page of base64; in a terminal known to show images, by the variables it
// sets; and not inside tmux or screen, which pass an image's escape
// sequence through only when configured to, and otherwise drop it or show
// it as text. Everywhere else it is braille, which any terminal shows.
//
// The terminals are known by what they set and an ssh session forwards:
// iTerm2's LC_TERMINAL travels with the locale, where TERM_PROGRAM does
// not. Where detection misses a terminal that can show images, --terminal
// iterm or kitty says so outright, and is obeyed whatever the output is.
func chooseTerminal(mode string, getenv func(string) string, tty bool) (how, note string, err error) {
	switch mode {
	case termITerm, termKitty, termBraille:
		return mode, "", nil
	case termAuto:
	default:
		return "", "", fmt.Errorf("--terminal %q: use auto, iterm, kitty or braille", mode)
	}
	if !tty {
		return termBraille, "", nil
	}
	if getenv("TMUX") != "" || getenv("STY") != "" || strings.HasPrefix(getenv("TERM"), "screen") || strings.HasPrefix(getenv("TERM"), "tmux") {
		return termBraille, "drawn in braille: tmux and screen pass a picture through only when set to; --terminal iterm or kitty draws one anyway", nil
	}
	term, program := getenv("TERM"), getenv("TERM_PROGRAM")
	switch {
	case getenv("KITTY_WINDOW_ID") != "" || term == "xterm-kitty", program == "ghostty" || term == "xterm-ghostty":
		return termKitty, "", nil
	case program == "iTerm.app" || getenv("LC_TERMINAL") == "iTerm2", program == "WezTerm":
		return termITerm, "", nil
	}
	return termBraille, "", nil
}

// writeITermImage writes png to w as iTerm2's inline image, cols characters
// wide, its height as its shape makes it: the whole file, base64, in one
// escape sequence. WezTerm reads the same.
func writeITermImage(w io.Writer, png []byte, cols int) error {
	_, err := fmt.Fprintf(w, "\x1b]1337;File=inline=1;size=%d;width=%d;preserveAspectRatio=1:%s\a\n",
		len(png), cols, base64.StdEncoding.EncodeToString(png))
	return err
}

// kittyChunk is the most base64 kitty's graphics protocol takes in one
// escape sequence.
const kittyChunk = 4096

// writeKittyImage writes png to w in kitty's graphics protocol, cols
// characters wide: the base64 in chunks of at most kittyChunk, each but the
// last saying more follows. q=2 asks the terminal not to answer: an answer
// arrives on the terminal's input, where the shell would read it as typing.
func writeKittyImage(w io.Writer, png []byte, cols int) error {
	data := base64.StdEncoding.EncodeToString(png)
	var b bytes.Buffer
	for first := true; first || len(data) > 0; first = false {
		n := min(len(data), kittyChunk)
		chunk, more := data[:n], 0
		data = data[n:]
		if len(data) > 0 {
			more = 1
		}
		if first {
			fmt.Fprintf(&b, "\x1b_Ga=T,f=100,q=2,c=%d,m=%d;%s\x1b\\", cols, more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	b.WriteString("\n")
	_, err := w.Write(b.Bytes())
	return err
}
