package panel

// DefaultLayout is the shipped arrangement.
//
// One panel today, filling the frame inside a margin. It is a function rather
// than a variable so the Layout always carries its Name -- a struct literal
// assembled elsewhere would have an empty one, and a diagnostic that named a
// layout while the pixels showed another is a lie that is hard to catch.
//
// There is deliberately no --layout flag yet, and no landscape/portrait pair.
// With a single panel the three arrangements the design calls for would all
// resolve to the same rectangle, so the flag would be surface with no effect
// behind it -- and a flag that does nothing is worse than a missing one,
// because someone will eventually rely on it. It arrives with the second
// panel, which is when the arrangements start to differ.
func DefaultLayout() Layout {
	return Layout{
		Name:      "default",
		Margin:    0.03,
		FontScale: 0.05,
		Root:      Slot{Panel: ElapsedPanel{}},
	}
}
