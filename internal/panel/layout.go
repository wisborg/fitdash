package panel

import (
	"fmt"
	"math"
)

// Box is a resolved rectangle in frame pixels. A panel draws inside its box
// and never outside it.
//
// A rectangle, not an anchor point, and that is the largest departure from the
// HUD this project's sibling renders over footage. There, gauges size
// themselves and grow away from an anchor, which is right for small readouts
// floating over an image -- if two of them touch it is ugly rather than wrong.
// Here the panels ARE the image and must tile the frame, and anchor-and-grow
// is precisely what collides in a portrait frame, where every panel growing
// rightward from a left anchor meets one growing leftward from a right anchor
// as the width collapses. Handing out disjoint rectangles makes overlap
// structurally impossible.
//
// The cost is real: a panel can no longer size itself and let the layout
// absorb it. It must fit a rectangle it was given, sizing text from the box's
// smaller dimension and shrinking long readouts. That is the work of actually
// laying out a dashboard.
type Box struct{ X, Y, W, H float64 }

// inset shrinks b by d on every side, clamping to zero rather than going
// negative -- a negative extent would silently invert every containment test
// downstream.
func (b Box) inset(d float64) Box {
	out := Box{X: b.X + d, Y: b.Y + d, W: b.W - 2*d, H: b.H - 2*d}
	if out.W < 0 {
		out.W = 0
	}
	if out.H < 0 {
		out.H = 0
	}
	return out
}

// Dir is how a split divides its box among its children.
type Dir int

const (
	// Row places children left to right, dividing the width.
	Row Dir = iota
	// Col places children top to bottom, dividing the height.
	Col
	// Alt does not divide its box at all: the first child that survives
	// the keep filter takes the WHOLE box, and the rest are not asked.
	//
	// This exists for a box whose candidates are mutually exclusive
	// rather than complementary -- the elevation profile and the distance
	// readout, in layouts.go, are the case it was built for, and the
	// design note there explains why a shared predicate on the losing
	// candidate's own Accepts was rejected in favour of this. Because
	// pruning (see pruneSlot) removes a rejected leaf before any box is
	// divided, Alt needs no separate placement logic: once pruned to its
	// one survivor, the ordinary proportional division in placeSlot hands
	// that survivor everything, exactly as any other single-child split
	// would.
	//
	// A weight on an Alt child would do nothing -- there is never more
	// than one survivor to divide space between -- so Validate rejects
	// one rather than silently ignoring it.
	//
	// The gap this leaves: when an earlier candidate wins, the keep
	// filter is never asked about a later one, so that candidate appears
	// in neither Resolve's placements nor a caller's "declined" report
	// (see cmd/render.go's summary). Nothing is hidden from the frame --
	// the surviving candidate drew, and drew the thing the later one would
	// have shown -- but a reader grepping a decline summary for the later
	// candidate's name finds nothing. Closing it needs a new "superseded"
	// callback the engine does not have today; not worth adding for the
	// one slot that needs it.
	Alt
)

func (d Dir) String() string {
	switch d {
	case Col:
		return "col"
	case Alt:
		return "alt"
	default:
		return "row"
	}
}

// Slot is a node in a layout: either a leaf holding one Panel, or a split
// dividing its box among Children.
//
// A Slot with a non-nil Panel is a leaf and its Children are ignored; the two
// are not meant to be combined, and Validate says so rather than letting a
// half-built tree resolve into something surprising.
type Slot struct {
	// Panel makes this a leaf.
	Panel Panel

	// Dir and Children make this a split.
	Dir      Dir
	Children []Slot

	// Weight is this slot's share of its parent's extent, relative to its
	// siblings'. Zero means one, so a tree of equal children needs no weights
	// at all -- which is the common case and should not have to be spelled.
	Weight float64

	// Pad insets this slot's box on every side, as a fraction of the frame's
	// SMALLER dimension. A fraction of the smaller dimension rather than of
	// the box's own size, so the gap between two panels is the same gap
	// wherever they sit and however the frame is shaped.
	Pad float64
}

// isLeaf reports whether s holds a panel rather than children.
func (s Slot) isLeaf() bool { return s.Panel != nil }

// Layout is a whole dashboard arrangement.
type Layout struct {
	// Name identifies the layout in diagnostics. It is set by the function
	// that builds the layout and never by a caller: a Layout assembled as a
	// bare struct literal carries no name, and a log line that named a layout
	// while showing another's pixels is a lie that is hard to catch.
	Name string

	// Margin insets the whole frame, as a fraction of its smaller dimension.
	Margin float64

	// FontScale sets the base text size as a fraction of the frame's smaller
	// dimension. It lives on the Layout rather than on a panel because eight
	// panels each choosing their own base size is eight programs in one frame.
	FontScale float64

	// Root is the tree.
	Root Slot
}

// MarginPx resolves Margin -- a fraction of the frame's smaller dimension --
// to pixels for a frame of w by h. It is exactly the quantity Resolve insets
// its own working frame by before dividing anything among the tree, exported
// so a caller outside this package can locate that same guaranteed-empty
// band without re-deriving the formula by hand.
//
// internal/render's highlight border is the reason this exists: it draws
// inside the margin on the strength of a comment claiming the two can never
// collide, a guarantee that used to rest on two independent copies of
// `Margin * min(w, h)` agreeing rather than on one formula computed once. If
// Resolve's own margin handling ever changes, a caller still doing the
// arithmetic by hand would silently stop matching the real empty band.
func (l Layout) MarginPx(w, h int) float64 {
	return l.Margin * math.Min(float64(w), float64(h))
}

// Placed is one panel and the box it was given.
type Placed struct {
	Panel Panel
	Box   Box
}

// Validate reports why l cannot be resolved, or nil.
func (l Layout) Validate() error {
	if l.Margin < 0 {
		return fmt.Errorf("panel: layout %q has a negative margin", l.Name)
	}
	if l.FontScale <= 0 {
		return fmt.Errorf("panel: layout %q has a non-positive font scale", l.Name)
	}
	return validateSlot(l.Root, l.Name, "root")
}

func validateSlot(s Slot, layout, path string) error {
	if s.Weight < 0 {
		return fmt.Errorf("panel: layout %q: %s has a negative weight %v", layout, path, s.Weight)
	}
	if s.Pad < 0 {
		return fmt.Errorf("panel: layout %q: %s has a negative pad %v", layout, path, s.Pad)
	}
	switch {
	case s.isLeaf():
		if len(s.Children) > 0 {
			return fmt.Errorf("panel: layout %q: %s holds both a panel (%s) and %d children; a slot is one or the other",
				layout, path, s.Panel.Name(), len(s.Children))
		}
	case len(s.Children) == 0:
		return fmt.Errorf("panel: layout %q: %s holds neither a panel nor any children", layout, path)
	default:
		for i, c := range s.Children {
			childPath := fmt.Sprintf("%s.%s[%d]", path, s.Dir, i)
			if s.Dir == Alt && c.Weight != 0 {
				// An Alt slot never divides space between children -- the
				// first survivor takes all of it -- so a weight here does
				// nothing and would mislead a reader exactly as the dead
				// 4:1 weights this slot replaced would have.
				return fmt.Errorf("panel: layout %q: %s has a weight of %v, but a weight on an alt child does nothing",
					layout, childPath, c.Weight)
			}
			if err := validateSlot(c, layout, childPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// Resolve assigns every kept panel a box, for a frame of w by h pixels.
//
// keep decides which panels are placed at all. A panel it rejects is not
// merely skipped: it is removed from the tree BEFORE any box is divided, so it
// contributes nothing to its siblings' weight denominator and they grow into
// the space it would have taken.
//
// **That is the whole mechanism behind "the layout closes up around a panel
// that has no data to show."** There is no redistribution special case to get
// wrong, and no policy about who inherits the gap -- pruning plus proportional
// division is the entire implementation. A split left with a single survivor
// needs no handling either: one child's share of the weights is all of them,
// so it takes the whole box on its own.
//
// A nil keep places every panel.
//
// The result is in the tree's own order, which is deterministic, so two
// renders of one layout produce the same placements and a test can index into
// them.
func (l Layout) Resolve(w, h int, keep func(Panel) bool) ([]Placed, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("panel: layout %q cannot resolve into a %dx%d frame", l.Name, w, h)
	}
	if keep == nil {
		keep = func(Panel) bool { return true }
	}

	root, ok := pruneSlot(l.Root, keep)
	if !ok {
		return nil, nil
	}

	unit := math.Min(float64(w), float64(h))
	frame := Box{W: float64(w), H: float64(h)}.inset(l.MarginPx(w, h))
	if frame.W <= 0 || frame.H <= 0 {
		return nil, fmt.Errorf("panel: layout %q has a margin of %v, which leaves nothing of a %dx%d frame", l.Name, l.Margin, w, h)
	}

	var out []Placed
	if err := placeSlot(root, frame, unit, l.Name, "root", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// pruneSlot removes rejected leaves and any split left with no survivors,
// reporting whether anything of s remains.
func pruneSlot(s Slot, keep func(Panel) bool) (Slot, bool) {
	if s.isLeaf() {
		return s, keep(s.Panel)
	}
	if s.Dir == Alt {
		// The first child that survives (recursively pruned, so an Alt
		// candidate that is itself a split works too) takes the slot
		// whole; later candidates are never even asked. See the Alt doc
		// comment for why this, rather than a shared predicate on the
		// losing candidate, is the mechanism.
		for _, c := range s.Children {
			if pc, ok := pruneSlot(c, keep); ok {
				s.Children = []Slot{pc}
				return s, true
			}
		}
		return Slot{}, false
	}
	kept := make([]Slot, 0, len(s.Children))
	for _, c := range s.Children {
		if pc, ok := pruneSlot(c, keep); ok {
			kept = append(kept, pc)
		}
	}
	if len(kept) == 0 {
		return Slot{}, false
	}
	s.Children = kept
	return s, true
}

// placeSlot divides box among s's surviving children and appends the leaves.
//
// unit is the frame's smaller dimension, which every fractional pad is
// measured against -- passed down rather than recomputed so a nested slot
// cannot start measuring against its own box and produce a gap that changes
// size with depth.
func placeSlot(s Slot, box Box, unit float64, layout, path string, out *[]Placed) error {
	box = box.inset(s.Pad * unit)
	if box.W <= 0 || box.H <= 0 {
		return fmt.Errorf("panel: layout %q: %s resolved to a %gx%g box; the frame is too small for this arrangement",
			layout, path, box.W, box.H)
	}

	if s.isLeaf() {
		*out = append(*out, Placed{Panel: s.Panel, Box: box})
		return nil
	}

	var total float64
	for _, c := range s.Children {
		total += weightOf(c)
	}

	// Walk the children accumulating an exact fractional offset and taking
	// each child's box from one boundary to the next, rather than summing
	// rounded widths. Summing would let a rounding remainder open a
	// one-pixel seam between neighbours -- visible as a hairline of
	// background between two panels, and exactly the kind of artefact that
	// gets blamed on the drawing code.
	var used float64
	for i, c := range s.Children {
		used += weightOf(c)
		frac := used / total

		child := box
		if s.Dir == Col {
			end := box.Y + box.H*frac
			child.Y = box.Y + box.H*(used-weightOf(c))/total
			child.H = end - child.Y
		} else {
			end := box.X + box.W*frac
			child.X = box.X + box.W*(used-weightOf(c))/total
			child.W = end - child.X
		}
		if err := placeSlot(c, child, unit, layout, fmt.Sprintf("%s.%s[%d]", path, s.Dir, i), out); err != nil {
			return err
		}
	}
	return nil
}

// weightOf reads a slot's weight, treating zero as one so an evenly divided
// split needs no weights spelled out.
func weightOf(s Slot) float64 {
	if s.Weight == 0 {
		return 1
	}
	return s.Weight
}
