package panel

import (
	"fmt"
	"strings"
)

// LayoutAuto names the arrangement chosen from the frame's own shape.
const LayoutAuto = "auto"

// Layouts are the arrangements --layout can choose, besides auto.
func Layouts() []Layout { return []Layout{LandscapeLayout(), PortraitLayout()} }

// SelectLayout picks an arrangement by name, or from the frame's shape when
// the name is LayoutAuto.
//
// Auto remains the default and is right almost always: a landscape tree
// squeezed into a portrait frame gives every panel an absurd aspect ratio.
// Naming one explicitly is for the cases automation cannot know about -- a
// square frame, or a wide render meant to sit beside something else -- and
// forcing a mismatch is the user's to make, not this function's to prevent.
//
// An unknown name is refused rather than falling back, for the same reason
// --theme refuses one: a typo should not cost a whole render.
func SelectLayout(name string, w, h int) (Layout, error) {
	if name == LayoutAuto || name == "" {
		if h > w {
			return PortraitLayout(), nil
		}
		return LandscapeLayout(), nil
	}
	for _, l := range Layouts() {
		if l.Name == name {
			return l, nil
		}
	}
	names := []string{LayoutAuto}
	for _, l := range Layouts() {
		names = append(names, l.Name)
	}
	return Layout{}, fmt.Errorf("panel: unknown layout %q; use %s", name, strings.Join(names, ", "))
}

// LandscapeLayout is the wide arrangement: the clock on the left at twice the
// width, the readouts stacked beside it.
//
// Layouts are built by functions so they always carry a Name. A Layout
// assembled as a struct literal elsewhere would have an empty one, and a
// diagnostic naming a layout while the pixels show another is a lie that is
// hard to catch.
func LandscapeLayout() Layout {
	return Layout{
		Name:      "landscape",
		Margin:    0.03,
		FontScale: 0.05,
		// The elevation profile gets a full-width strip along the bottom
		// rather than a share of the readout column. It is a wide graph by
		// nature -- a distance axis with two labels at its ends -- and in a
		// narrow box those labels grow toward each other until they collide.
		Root: Slot{Dir: Col, Children: []Slot{
			{Dir: Row, Weight: 4, Children: []Slot{
				{Dir: Col, Weight: 3, Children: []Slot{
					{Panel: RoutePanel{}, Weight: 3, Pad: 0.01},
					{Dir: Row, Weight: 2, Children: []Slot{
						{Panel: ElapsedPanel{}, Weight: 3, Pad: 0.01},
						{Panel: Distance(), Weight: 2, Pad: 0.01},
					}},
				}},
				{Dir: Col, Weight: 1, Children: []Slot{
					{Panel: HeartRate(), Pad: 0.01},
					{Panel: Pace(), Pad: 0.01},
					{Panel: Power(), Pad: 0.01},
					{Panel: Cadence(), Pad: 0.01},
				}},
			}},
			{Panel: ElevationPanel{}, Weight: 1, Pad: 0.01},
			// HighlightPanel declines outright with no --highlight
			// configured (see its own Accepts), and Resolve prunes a
			// declining leaf BEFORE dividing space among its siblings --
			// so this row costs an ordinary render nothing: the rows
			// above it get exactly the boxes they would have gotten had
			// this line never been added. See
			// highlight_panel_test.go's pixel-identical test.
			{Panel: HighlightPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}

// PortraitLayout is the tall arrangement: everything in one column, the clock
// given the most room.
//
// A different TREE rather than the landscape one rescaled, because the
// available width collapses in portrait and a row of three panels there would
// give each a sliver too narrow to read.
func PortraitLayout() Layout {
	return Layout{
		Name:      "portrait",
		Margin:    0.03,
		FontScale: 0.05,
		Root: Slot{Dir: Col, Children: []Slot{
			{Panel: RoutePanel{}, Weight: 4, Pad: 0.01},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: ElapsedPanel{}, Weight: 3, Pad: 0.01},
				{Panel: Distance(), Weight: 2, Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: HeartRate(), Pad: 0.01},
				{Panel: Pace(), Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: Power(), Pad: 0.01},
				{Panel: Cadence(), Pad: 0.01},
			}},
			{Panel: ElevationPanel{}, Weight: 2, Pad: 0.01},
			// See LandscapeLayout's own comment beside HighlightPanel: it
			// declines and Resolve prunes it before the sibling rows'
			// space is divided, so this row costs an ordinary render
			// nothing.
			{Panel: HighlightPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}
