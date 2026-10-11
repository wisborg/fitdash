package panel

import (
	"image/color"
	"time"
)

// SummaryCard is what a full-frame flyover shows over its overview at the
// start and the end: a title -- the user's, or the flight, or the sport --
// and a line or two of the activity's figures, already written in the
// render's units. Subtitle, the user's line saying what the video shows, is
// shown under the title at the start only: it introduces the video, and the
// card at the end is its summary. Empty draws no card.
type SummaryCard struct {
	Title    string
	Subtitle string
	Lines    []string
}

// cardFade is how long the card takes to fade, out as the camera starts
// down from the overview and in as it arrives back.
const cardFade = time.Second

// FlyoverLayout is the full-frame flyover: the route panel alone, filling the
// frame with no margin, its camera flying after the activity. Nothing else
// is drawn while it flies; a summary card is shown over the overview it
// holds at the start and the end (see Timeline.WithHolds and SummaryCard).
// --layout flyover asks for it, and it is never chosen by auto.
func FlyoverLayout() Layout {
	return Layout{
		Name:      "flyover",
		Margin:    0,
		FontScale: 0.05,
		Root:      Slot{Panel: RoutePanel{}, Weight: 1},
	}
}

// cardAt is the card frame i shows: the whole of it at the start, and at the
// end without the subtitle, which introduces the video rather than sums it
// up.
func cardAt(card SummaryCard, i, frames int) SummaryCard {
	if i >= frames/2 {
		card.Subtitle = ""
	}
	return card
}

// cardWeight is how much of the card frame i shows: all of it through the
// holds, fading over cardFade into and out of them, and none in between.
func cardWeight(i, frames, lead, tail int, fps float64) float64 {
	if lead == 0 && tail == 0 {
		return 0
	}
	fade := cardFade.Seconds() * fps
	weight := func(since float64) float64 {
		if since < 0 {
			return 1
		}
		return max(0, 1-since/fade)
	}
	w := 0.0
	if lead > 0 {
		w = weight(float64(i - (lead - 1)))
	}
	if tail > 0 {
		w = max(w, weight(float64((frames-tail)-i)))
	}
	return w
}

// drawCard draws card centred in box at weight: on a panel of the theme's
// background, a little translucent so the map shows it is over the map, the
// title above its lines, fitted to the box.
func drawCard(c *Canvas, box Box, card SummaryCard, weight float64) {
	if weight <= 0 || card.Title == "" && len(card.Lines) == 0 {
		return
	}
	maxW := box.W * 0.8
	titlePx, subPx, linePx := c.BasePx*1.25, c.BasePx*0.95, c.BasePx*0.8
	if card.Title != "" {
		if px, err := c.FitTextSize(card.Title, maxW, titlePx); err == nil {
			titlePx = px
		}
	}
	if card.Subtitle != "" {
		if px, err := c.FitTextSize(card.Subtitle, maxW, subPx); err == nil {
			subPx = px
		}
	}
	widest := 0.0
	for _, l := range card.Lines {
		if px, err := c.FitTextSize(l, maxW, linePx); err == nil && px < linePx {
			linePx = px
		}
	}
	if card.Title != "" {
		if w, _, err := c.MeasureText(card.Title, titlePx); err == nil {
			widest = w
		}
	}
	if card.Subtitle != "" {
		if w, _, err := c.MeasureText(card.Subtitle, subPx); err == nil {
			widest = max(widest, w)
		}
	}
	for _, l := range card.Lines {
		if w, _, err := c.MeasureText(l, linePx); err == nil {
			widest = max(widest, w)
		}
	}
	pad := linePx * 1.1
	gap := linePx * 0.55
	// The card's rows, each with the space above it: measured and drawn
	// from the one list, so the card is always as tall as what is in it.
	type row struct {
		text      string
		px, above float64
		ink       color.Color
	}
	var rows []row
	add := func(text string, px float64, ink color.Color) {
		above := gap
		switch {
		case len(rows) == 0:
			above = 0
		case ink == c.Theme.Dim && rows[len(rows)-1].ink != c.Theme.Dim:
			above = 2 * gap // the figures set off from the title above
		}
		rows = append(rows, row{text, px, above, ink})
	}
	if card.Title != "" {
		add(card.Title, titlePx, c.Theme.Foreground)
	}
	if card.Subtitle != "" {
		add(card.Subtitle, subPx, c.Theme.Foreground)
	}
	for _, l := range card.Lines {
		add(l, linePx, c.Theme.Dim)
	}
	h := 2 * pad
	for _, r := range rows {
		h += r.above + r.px
	}
	w := widest + 2*pad
	b := Box{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: w, H: h}
	c.Rect(b, Fade(c.Theme.Background, 0.94*weight))
	y := b.Y + pad
	for _, r := range rows {
		y += r.above + r.px
		// Anchored by the bottom of the line, a little above it for the
		// descenders: gg's vertical anchor of 0 is the baseline, and a
		// title anchored at its top stood above the card.
		_ = c.Text(r.text, b.X+w/2, y-r.px*0.12, 0.5, 0, r.px, Fade(r.ink, weight))
	}
}
