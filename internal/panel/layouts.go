package panel

// SelectLayout picks the arrangement for a frame of this shape.
//
// Orientation is chosen from the frame rather than from a flag, because it is
// not a preference: a landscape tree squeezed into a portrait frame gives every
// panel an absurd aspect ratio, and there is no reason a user would want that.
// A --layout flag selecting between whole arrangements can come later, when
// there is more than one arrangement per orientation to choose between.
func SelectLayout(w, h int) Layout {
	if h > w {
		return PortraitLayout()
	}
	return LandscapeLayout()
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
					{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
				}},
				{Dir: Col, Weight: 1, Children: []Slot{
					{Panel: HeartRate(), Pad: 0.01},
					{Panel: Power(), Pad: 0.01},
				}},
			}},
			{Panel: ElevationPanel{}, Weight: 1, Pad: 0.01},
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
			{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: HeartRate(), Pad: 0.01},
				{Panel: Power(), Pad: 0.01},
			}},
			{Panel: ElevationPanel{}, Weight: 2, Pad: 0.01},
		}},
	}
}
