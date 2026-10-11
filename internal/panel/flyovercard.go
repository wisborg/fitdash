package panel

import (
	"time"
)

// SummaryCard is what a full-frame flyover shows over its overview at the
// start and the end: a title -- the flight, or the sport and the day -- and
// a line or two of the activity's figures, already written in the render's
// units. Empty draws no card.
type SummaryCard struct {
	Title string
	Lines []string
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
	titlePx, linePx := c.BasePx*1.25, c.BasePx*0.8
	if card.Title != "" {
		if px, err := c.FitTextSize(card.Title, maxW, titlePx); err == nil {
			titlePx = px
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
	for _, l := range card.Lines {
		if w, _, err := c.MeasureText(l, linePx); err == nil {
			widest = max(widest, w)
		}
	}
	pad := linePx * 1.1
	gap := linePx * 0.55
	h := 2 * pad
	if card.Title != "" {
		h += titlePx
	}
	if len(card.Lines) > 0 {
		h += float64(len(card.Lines))*linePx + float64(len(card.Lines)-1)*gap
		if card.Title != "" {
			h += gap * 2
		}
	}
	w := widest + 2*pad
	b := Box{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: w, H: h}
	c.Rect(b, Fade(c.Theme.Background, 0.94*weight))
	y := b.Y + pad
	if card.Title != "" {
		// Anchored by the bottom of the line: gg's vertical anchor of 0 is
		// the baseline, and a title anchored there stood above the card.
		_ = c.Text(card.Title, b.X+w/2, y+titlePx, 0.5, 0, titlePx, Fade(c.Theme.Foreground, weight))
		y += titlePx + gap*2
	}
	for _, l := range card.Lines {
		_ = c.Text(l, b.X+w/2, y+linePx*0.85, 0.5, 0, linePx, Fade(c.Theme.Dim, weight))
		y += linePx + gap
	}
}
