package panel

import (
	"image"
	"testing"
)

// The card is whole through the holds, fades over a second into and out of
// them, and is gone in between; a timeline without holds has none.
func TestCardWeight(t *testing.T) {
	const fps, frames, lead, tail = 10.0, 500, 30, 30
	for _, c := range []struct {
		i    int
		want float64
	}{
		{0, 1}, {29, 1}, {34, 0.5}, {39, 0}, {250, 0},
		{460, 0}, {465, 0.5}, {470, 1}, {499, 1},
	} {
		if got := cardWeight(c.i, frames, lead, tail, fps); got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("frame %d: weight %.2f, want %.2f", c.i, got, c.want)
		}
	}
	if got := cardWeight(0, frames, 0, 0, fps); got != 0 {
		t.Errorf("no holds, weight %v at the first frame", got)
	}
}

// The flyover layout is selectable by name and never chosen by auto; its
// one panel fills the frame.
func TestFlyoverLayout(t *testing.T) {
	l, err := SelectLayout("flyover", 1920, 1080)
	if err != nil || l.Name != "flyover" || l.Margin != 0 {
		t.Fatalf("SelectLayout(flyover): %+v %v", l, err)
	}
	for _, size := range [][2]int{{1920, 1080}, {1080, 1920}} {
		if a, _ := SelectLayout(LayoutAuto, size[0], size[1]); a.Name == "flyover" {
			t.Errorf("auto chose the flyover for %v", size)
		}
	}
}

// A card is drawn centred, over the picture, and only as much as its weight.
func TestDrawCard(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 450))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 24, DefaultTheme(), faces)
	box := Box{W: 800, H: 450}
	card := SummaryCard{Title: "TG950 BKK → CPH", Lines: []string{"26 Jul 2026", "5209.1 nm · 11:10:23"}}
	c.Fill(c.Theme.Accent)
	drawCard(c, box, card, 0)
	if got := countNear(img, c.Theme.Accent, 2); got != 800*450 {
		t.Errorf("at weight 0, %d pixels changed", 800*450-got)
	}
	drawCard(c, box, card, 1)
	if countNear(img, c.Theme.Accent, 2) == 800*450 {
		t.Fatal("at weight 1 nothing was drawn")
	}
	// Centred: the middle is the card's, the corners still the picture.
	if near := countNearInBox(img, Box{X: 390, Y: 215, W: 20, H: 20}, c.Theme.Accent, 2); near == 400 {
		t.Error("the middle of the picture is not under the card")
	}
	for _, corner := range []Box{{W: 20, H: 20}, {X: 780, Y: 430, W: 20, H: 20}} {
		if near := countNearInBox(img, corner, c.Theme.Accent, 2); near != 400 {
			t.Errorf("the card reaches the corner at %+v", corner)
		}
	}
}
