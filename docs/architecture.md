# fitdash rendering architecture

How a FIT activity becomes a dashboard video, and why the pieces are shaped the way
they are. This is the design the renderer is being built to; `CLAUDE.md` is the
working rules, and this is the reasoning behind them.

Much of it is a deliberate departure from [videofx][videofx], which solves a related
problem — compositing a telemetry HUD onto footage someone shot — and whose `internal/hud`
was read closely before any of this was decided. Where fitdash follows it, it says so.
Where it departs, the reason is almost always the same one: **videofx's gauges float over
an image, and fitdash's panels are the image.**

[videofx]: https://github.com/wisborg/videofx

## Packages

| Package | Contents |
|---|---|
| `internal/panel/` | `Panel`, `Painter`, `Box`, `Slot`, `Layout`, `Canvas`, `Theme`, `Context`, `Frame`, `Timeline`, and one file per panel |
| `internal/render/` | the frame loop, the static/dynamic composition, `Renderer` |
| `internal/encode/` | `Sink`, the ffmpeg rawvideo pipe, the PNG-frames sink |
| `internal/route/` | GPS projection and the route panel |
| `internal/inspect/` | already exists; the render path reads its `Report` (see "One rule for absence") |

`Context`, `Frame` and `Timeline` live in `internal/panel` rather than `internal/render`,
because the types a panel is handed belong with the contract that defines them — and
because the other direction is an import cycle. `internal/render` imports `internal/panel`
and `internal/encode`; `internal/panel` imports only `fitactivity`, `internal/inspect` and
the drawing libraries.

## The panel contract: three phases

```go
type Panel interface {
	Name() string
	Accepts(ctx *Context) bool           // before any box exists
	Prepare(ctx *Context, box Box) Painter
}

type Painter interface {
	Static(c *Canvas)                    // called ONCE
	Dynamic(c *Canvas, f Frame)          // called per frame, over the static layer
}
```

A `Panel` is configuration — which metric, drawn how. A `Painter` is that panel bound to
one render, holding everything that is fixed for the whole video: its box, its projection,
its axis range, its font sizes.

### Why this rather than videofx's `Gauge`

videofx has `Draw(r, dc, box, f Frame)` and an optional `DrawStatic(r, dc, box, f Frame)`
— **the same `Frame` type in both**. The static call receives a `Frame` with only the
dimensions and the render-wide context filled in and every per-frame field left at its zero
value. That is a convention held up by a doc comment, and it is the convention that broke:
`Course.StartDistance` in videofx's `hud.go` carries a twenty-line warning explaining that
the field must live on the per-render context and never on `Frame`, because an origin
supplied per frame is simply *absent* when the static layer rasterizes. The axis labels get
scaled against zero while the playhead composited on top uses the real origin. Both layers
draw. They disagree. Nothing errors.

Splitting the phases makes that a compile error rather than a rule:

1. **`Static` has no `Frame` in scope.** Not a zeroed one — none. There is no per-frame
   value to read, so there is nothing that can be silently absent.
2. **`Static` and `Dynamic` are methods on the same `Painter`.** The axis origin is one
   field of one struct, read by both. In videofx each layer recomputed the geometry from
   its own `Frame`, which is what let the two computations diverge.
3. **The expensive per-render arithmetic has somewhere to live.** videofx needs a
   mutex-guarded memo map keyed on a route slice's backing-array pointer, its length, the
   box and the frame size — purely because the projection had no home between frames. Here
   it is a field on the Painter, computed in `Prepare`. **That cache is deleted before it
   is written**, which is the clearest sign the split is the right shape.

`Static` is required, not an optional interface assertion. A panel with nothing invariant
embeds `panel.NoStatic`, so "this panel has no chrome" is a greppable declaration rather
than a silent omission.

**`Dynamic` must not mutate the Painter.** It costs nothing today and it is what keeps
parallel frame rendering possible later. videofx needs a mutex precisely because it has no
such rule.

### What a panel draws onto

A `*Canvas`, not a raw drawing context. It carries the frame dimensions, the base text size
(`FontScale * min(W,H)`), a `Theme` and a font-face cache.

A panel never names a colour literal, for the same reason it never names a pixel constant:
eight panels each picking their own grey is eight programs in one frame. `Theme.Absent` in
particular is the colour of a placeholder, so "no data" looks the same everywhere.

The frame is **opaque** — the renderer fills it with `Theme.Background` before anything
draws. videofx renders a mostly-transparent overlay because there is footage beneath it.
Here the background *is* the product, which is also what lets a declining panel leave clean
background rather than a hole.

## Layout: a weighted tree

`Box` is a **rectangle**, not an anchor point:

```go
type Box struct{ X, Y, W, H float64 }
```

This is the largest departure from videofx, whose `Box` is an anchor plus a direction and
whose gauges size themselves and grow away from it. That model is right for small readouts
floating over footage — if two of them touch it is ugly rather than wrong. It is wrong here,
because fitdash's panels have to tile the frame, and anchor-and-grow is exactly what
produces the portrait collision: every panel growing rightward from a left anchor meets one
growing leftward from a right anchor as the width collapses.

Handing out disjoint rectangles makes overlap structurally impossible. The cost is real and
worth stating: a panel can no longer size itself and let the layout absorb it. It must fit
a rectangle it was given, sizing its text from `min(box.W, box.H)` and shrinking long
readouts. That is more work per panel, and it is the work of actually laying out a dashboard.

The arrangement is a tree of nested rows and columns:

```go
type Slot struct {
	Panel    Panel   // leaf
	Dir      Dir     // or a split: Row | Col
	Children []Slot
	Weight   float64 // share of the parent's extent
	Pad      float64 // inset, fraction of min(frame W,H)
}

func (l Layout) Resolve(w, h int, keep func(Panel) bool) []Placed
```

`Resolve` prunes every leaf `keep` rejects (collapsing a split with no survivors, and
replacing one with a single survivor by that survivor), then divides each box among the
surviving children in proportion to their weights, then applies the insets.

**"The layout closes up around a declining panel" is real because of pruning plus weight
division, and for no other reason.** There is no redistribution special case: a pruned
panel is not in the denominator, so its siblings grow. It is a tree walk, table-testable
against known weights with no pixels involved.

Everything is fractional. Weights are dimensionless; `Margin`, `Pad` and `FontScale` are
fractions of `min(w, h)` — the *smaller* dimension, following videofx, so text is sized
against the narrow edge in either orientation. **There is no pixel constant in a layout.**
1080p, 4K and portrait are the same tree evaluated at three sizes.

Portrait goes one step further than scaling: landscape and portrait are different *trees*
(row-major against column-major), chosen by frame shape. Squeezing a landscape tree into a
portrait frame produces boxes with absurd aspect ratios. Layouts are built by functions so
they always carry a `Name` — videofx learned that a struct-literal layout with an empty name
makes a log line lie about what is on screen.

### What one more panel costs

One new file in `internal/panel/`, and one line in each layout that wants it. Nothing in
`internal/render` changes: the loop iterates the resolved placements and calls two methods,
and it does not know what a panel is beyond that. The neighbours shrink automatically
because the weight denominator grew.

That gives the panel contract a reviewable tripwire with a yes/no answer: **did the diff
touch `internal/render`?** If adding a panel did, the contract has sprung a leak.

(The heading used to name a specific panel number, which went stale the moment the count
moved. The claim it makes has since been paid out twice at once by the climb and gradient
panels — two new files, one line in each tree, and `internal/render.go` untouched. That
is worth recording as evidence rather than as a restatement: the reason the tripwire held
there is *not* that the panels are simple, since one of them reads a model that had to be
lifted onto `Context` to land at all. The lift was a change to `Context` and to the tests
that build one, and to nothing in the frame loop.)

### The alternative that was rejected

Flat fractional rectangles — each panel carrying `{X, Y, W, H}` in [0,1] of the frame — are
simpler to implement and trivial to read. They were rejected for two reasons. Closing up is
undefined in them: a declining panel leaves a rectangle of background and nothing knows who
should grow into it, so the behaviour would be aspirational. And hand-authoring eight
non-overlapping rectangles for landscape *and* portrait is a spreadsheet exercise in which
every arithmetic slip is a silent overlap that shows up only at one particular resolution.

The price of the tree is that a panel cannot be placed at an arbitrary spot; it only
expresses nested rows and columns. That is acceptable for a dashboard, and a free-placement
escape hatch can be added later without disturbing it.

### One band, two candidates — now with three jobs between them

One full-width band runs across the bottom of both trees, in the row directly above the
marker strip's own box, and it shows **either** the elevation profile **or** the live
distance readout — never both, and never neither while the activity carries the data for
one of them. The profile used to have that band to itself and the readout used to sit
beside the clock. Both moved for the same reason: **the filled area under the profile's
trace is now this project's distance indicator**, so a separate readout drawn anywhere on
the frame is the same quantity said twice.

That choice between the two, and the `Alt` slot that makes it, is unchanged from when this
section was first written — see "The mechanism is an ordered alternatives slot" below.
What changed is what it means for the profile to *win* that choice. The profile no longer
only draws a curve, a fill and two axes: when a `--highlight` or a `--label` is configured,
the winning profile now also draws every highlight's block, every label's tick, and up to
two name rows reserved above the plot — the whole of what a separate marker strip used to
be the only place to find. So this band, when the profile takes it, carries **three**
jobs at once: the profile itself, the distance indicator (the fill), and the marker
strip's own content, folded onto the profile's own axis rather than left in a box of its
own. "The mark rows and the fold-in" below is that mechanism in full; "Absorbed, a fourth
outcome" further down is how the render summary keeps that fold from reading as a panel
that silently vanished.

The fill runs from the trace's own start to the playhead, and its leading edge is the
position. That is why the full-height playhead rule the panel used to draw is gone: the
edge of the fill marks the same x, and drawing both left a bright stub hanging above the
trace through the empty upper part of the plot, reading as an object in its own right
rather than as the fill's boundary. The dot on the trace stays, because it answers a
different question — *which elevation* the activity is at, not how far along it is — and
because it is the mark the route panel already uses for "you are here". If the leading
edge ever reads as mushy, the remedy is to stroke that edge, not to bring the rule back.

Three changes to the profile itself follow from the fill, and each of them looks
gratuitous until it is read as part of it:

- **The plot gained a floor and a baseline rule.** `yForElevation` now spans
  `floorElev..maxElev`, where `floorElev` sits below the recorded minimum by
  `elevationFloorHeadroom` — a fraction of the elevation *range*, never a constant number
  of metres, which would give a 100 m climb a floor 1% below it and a 5 m one a floor 20%
  below. A literal zero was rejected: it squashes a high-altitude activity into the top
  few percent of the plot, and an activity recorded at the coast dips below sea level, so
  part of its trace would fall through the baseline. The floor is not an elevation anybody
  measured, so **it carries no label** — a label there would name a reading that does not
  exist. The baseline itself is real ink drawn in `Static`, which gives the fill a defined
  bottom at frame 0, before any distance has been read, and is what makes a single
  exported PNG of the panel legible.
- **The distance axis starts at zero**, not at the model's own `StartDistance`. The
  elevation model keeps only samples carrying *both* distance and altitude, and a
  barometer commonly takes a few metres to settle after the distance stream has already
  started, so the model's first point can land some way along the activity. Starting the
  axis there would have quietly claimed the activity began where the barometer woke up.
  The gap between zero and the first altitude reading is drawn instead, as described under
  absent data below. The axis's two dim end labels are now the only distance *numbers* on
  the frame, and they state the scale rather than the position — the fill states the
  position, which is the division of labour the readout beside them made redundant.
- **Both axes are now placed through the same mapping the trace uses.** Both distance
  labels come from `xForDistance` and both elevation labels from `yForElevation`, so a
  label cannot name one value while pointing at another. That was already this panel's
  design principle on the x axis — it is the panel that would otherwise have walked into
  videofx's axis-origin trap — and the floor is what made it necessary on the y axis too:
  the low label used to be placed at the plot's bottom edge, which stopped being
  `yForElevation(minElev)` the moment a floor existed below the minimum.

**One honest caveat about the fill, because it is the thing a viewer can over-read.** Its
horizontal extent is distance and nothing else, but its *height* is terrain: a hilly
stretch fills more area than a flat stretch of the same length. Read as a progress bar it
is exact; read as a measure of effort or of work done it is misleading, and no arrangement
of colours fixes that. The alternative — a rectangle of constant height, whose area really
would be proportional to distance — throws away the profile, which is the panel's other
job. This is why the fill is a restrained faded-`Foreground` wash that the `Dim` trace
stays legible through, rather than something that competes with the trace for attention.

**The mechanism is an ordered alternatives slot, `Dir: Alt`.** An `Alt` slot does not
divide its box: the first child that survives the `keep` filter takes the whole of it, and
the later children are never asked. The band is one such slot holding the two existing
panels, in order — the profile, then the readout — and it reads at the call site as the
sentence it implements. `Validate` rejects a non-zero `Weight` on an `Alt` child, because
there is never more than one survivor to divide space between and a weight there would
mislead a reader exactly as a ratio between two mutually exclusive children would.
`placeSlot` needs nothing: pruning happens before any box is divided, so by the time
placement runs the slot has exactly one child and the ordinary proportional division hands
it everything. `internal/render` needs nothing either — the tripwire stays shut.

**The cheaper alternative, and why it is a trap rather than merely a tie.** `Readout`
already has a `carries` hook, so a strip variant of the distance readout whose predicate
is "distance is carried *and* the elevation panel declines" is a few lines and adds no
mechanism. It does not even re-derive anything: it would call the one existing rule,
`ElevationPanel.Accepts`, exactly once. It fails on something else entirely. `Accepts` is
a pure function of the render `Context`, and the `keep` filter belongs to `Resolve` — an
accept test cannot see it. So `--bottom-band distance`, built that way, would remove the
profile *and* leave that variant declining on its own account, since the elevation panel
still accepts the activity. The band would prune and distance would vanish from the render
entirely: the feature the arrangement exists to make possible would be broken by the
arrangement. Under `Alt` the same flag composes correctly and stays one line — `keep`
rejects the profile, the slot falls through, and the readout takes the band. This is not
hypothetical: `--bottom-band` (`internal/render.New`'s keep filter, gated on
`Context.BottomBand`) is exactly that one line, added after this section was written and
left unchanged by it.

**It is still a layout grouping, not a composite panel**, and that is a separate decision
from the one above. A single panel drawing a profile with a readout in the corner would be
the first in this project to sub-divide its own box, which is what the slot tree exists
for; and it would report one `Name()` in the render summary while showing another panel's
data, so an activity with no elevation would show the composite as *drawn* and never
mention that the profile was omitted — honest pixels behind a dishonest summary, which is
the failure the decline summary exists to prevent.

**The gap the slot leaves, named rather than hidden.** When the profile wins, `keep` is
never asked about the readout, so distance appears in **neither** the drawn list nor the
declined list of the render summary. Nothing is being concealed — the profile drew, and
the profile is what shows distance — but someone who greps that summary for "distance"
finds nothing at all, which is a worse experience than either answer. Closing it needs a
third outcome in the engine, "superseded", reported to the caller alongside placed and
declined; that is more machinery than one slot justifies, and it should be revisited the
day a second `Alt` appears. Until then it is written down here and in `Alt`'s own doc
comment so it is a known cost rather than a surprise. **What will force it is panel
selection by name.** Panel names are already the key `keep` rejects on, so the flag that
lets a user ask for a specific set of panels is that same filter with a user-supplied set
— and on the day it exists, typing the name of a superseded panel and getting neither that
panel nor a word about why stops being a cosmetic gap in a summary and becomes a support
question, asked by someone with no way to discover from the program that the answer is
"something else is already showing you that". The cost of the gap is a function of how
specifically a user can ask, and that is about to change.

**What makes the ordering safe.** Putting the profile first can only lose a display if
there is an activity the profile accepts and the readout does not, and there is not:
`ElevationPanel.Accepts` requires `Carries(MetricDistance)` alongside
`Carries(MetricElevation)`, because it needs a distance axis to plot against.
`TestElevationPanel_AcceptsImpliesDistanceReadoutAccepts` pins that implication rather
than a branch defending against its failure, and it fails loudly the day someone relaxes
`Accepts`. **A branch for an unreachable case is untested code wearing the shape of a
considered one:** nothing exercises it, nothing fails when it rots, and the next reader
cannot tell whether it guards a case that happens or a case that cannot.

The profile also declines on a flat or zero-span model, which is new since the fill became
the only distance indicator. A flat activity has a model and would have taken the band,
drawing two identical elevation labels and no fill — distance shown nowhere, the
unexplained hole arrived at from a new direction. Declining hands the band to the readout,
which is the honest outcome, and the `Alt` slot is what makes that fall-through free.
`TestResolve_DeclineCombinationsOverTheRealLayoutsLeaveNoUnclaimedRectangle` covers the
combinations over the *real* trees at three frame sizes with a `keep` that rejects by
panel name, and it asserts not only that the boxes still tile but **which** panels were
placed — geometry alone would have passed just as happily with the readout drawn beside
the profile, which is the arrangement this section exists to say is gone.

### The mark rows and the fold-in: why distance, never time

**Ruling: a mark drawn inside the profile's own box — a highlight's block, a label's
tick — must be positioned by distance. There is no defensible time-indexed option for it,
and this is forced, not chosen.** The profile's box already contains a moving indicator of
its own: the fill's leading edge, at `xForDistance(f.Sample.Distance)`, which is this
project's one distance readout (see above). A second, time-indexed ribbon drawn in the same
box would give that box two horizontal axes and two playheads, sitting at different x on
the same frame the moment the activity pauses — the fill's edge stalled at one x while a
time-based ribbon's playhead kept moving past it. Both layers would draw, they would
disagree, and nothing would error: the videofx axis-origin failure, reproduced in a form
the viewer actually sees. So every mark folded onto this axis is resolved once, in
`Prepare`, by converting the highlight's or label's own activity-time offset to a distance
via the track's gap-aware sample lookup (`ElevationPanel`'s exported `TimeToDistance`, in
`internal/panel/elevation.go`) and then placed exactly the way the trace, the fill and both
distance labels already are — through `xForDistance`, the one origin this axis has ever
had.

**The honest cost, stated rather than hidden.** Distance is monotone in time but not
injective, so a highlight spanning a stop can shrink toward zero width on this axis, and
two labels either side of a pause can land on the same x. More importantly: **a mark's
width on this axis is ground covered, not time elapsed**, and once the standalone strip is
gone (see the fold-in below), **nothing on the frame states duration any more**. A viewer
who wants to know how long a highlighted stretch lasted has to read it from the base clock,
not from the width of its block. That trade was put to the user directly, along with the
fact that a highlight over a full stop collapses to the axis's own smallest expressible
mark, and they chose to accept it rather than keep a second, time-indexed ribbon that would
have avoided it (`--bottom-band distance` remains the escape hatch for anyone who wants the
old, time-indexed strip back — see below).

**The fold-in itself.** `ElevationPanel` additionally reads `ctx.Highlights`, `ctx.Labels`
and `ctx.Timeline.Start()` in `Prepare`, and reserves up to two rows at the *top* of its own
box, above the plot — never inside the empty upper region the trace happens to leave, which
would make the row's own position a function of the data, a second origin in a panel whose
whole design is one. The label name sits in the top row, the highlight name beneath it, the
plot below both, matching the marker strip's own top-to-bottom order (label above the
ribbon, highlight below it) so a viewer moving between a strip render and a profile render
reads the same stack. The reservation is conditional on what is actually configured: with
neither a `--highlight` nor a `--label`, zero rows are reserved and the plot is exactly the
rectangle it resolved to before this feature existed, which is what keeps the frozen
pixel-identity test green untouched. `ElevationPanel` stayed one panel type rather than
growing a second, composite one for this — it already subdivides its own box into gutter,
plot and label row, so a mark row is one more piece of its own chrome, not a second panel's
content wearing its name (see `internal/panel/marks.go`, which both this panel and the
marker strip's own `Prepare` now call for the shared geometry: widening a span to a minimum
width, sweeping neighbours apart, clamping a name's anchor, and sizing a row's text against
the longest name in it).

### Absorbed, a fourth outcome

Folding the marker strip's content into the profile creates a case the render summary did
not have a name for: a panel — `MarkerPanel` — that is *not* placed in its own box, has
*not* declined (`Accepts` never even asked, because the marks are visibly on screen, just
not there), and was *not* omitted by a flag the way `--bottom-band distance` omits the
profile. `internal/render.New`'s `keep` filter now expresses this as a third rejection,
`MarkerPanel`-only, guarded on `profileTakesTheBand` (the exact same expression, computed
once, that decides whether the profile is drawing at all — see the "cheaper alternative"
trap discussion above: this could not be expressed in `MarkerPanel.Accepts`, because
`Accepts` cannot see what `keep` is about to do, and asking it to would repeat that exact
mistake one panel later) and on at least one highlight or label actually being configured,
so an ordinary render with neither still reports the strip's plain decline, unchanged.

`Renderer` gains a third list beside `Declined()` and `Omitted()` — `Absorbed()` — and
`cmd`'s render summary gains a fourth heading for it. The wording matters: an absorbed panel
is not corrected the way a false decline would be, because nothing about it was false —
its content is genuinely on screen, just inside a different panel's box — so the summary's
job is to say *where*, not to walk back a claim. This is a different gap than the one named
above under "The gap the slot leaves": that one is about the `Alt` slot never asking the
readout's own `Accepts`, and remains open; this one is about a panel excluded by `keep` for
a reason that is neither "nothing to show" nor "a flag removed it before asking," and it is
closed rather than merely written down, because — unlike the `Alt` case — the reason here is
knowable inside `keep` at the moment it decides, with no extra machinery required to surface
it.

### The marker strip carries no caption — and is now the fallback presentation

This section used to describe the *only* place a highlight's block or a label's tick was
drawn. It no longer is: whenever the elevation profile is both accepted and taking the
bottom band (see "The mark rows and the fold-in", above), the profile draws the marks
instead, on its own distance axis, and `MarkerPanel`'s own row — always a box separate from
the profile's, in both trees — is pruned away entirely, its neighbours growing into the
space exactly as they would for any other declining panel, even though `MarkerPanel` itself
never declined (`Absorbed()`, not `Declined()`; see "Absorbed, a fourth outcome", above).
The strip described below is now what a render falls back to instead: when the activity has
no elevation to plot at all, or when the profile still has the band but loses it to
`--bottom-band distance` (which restores this strip as the deliberate escape hatch — see its
own help text). Everything below is still exactly true of that box; what changed is that it
is no longer the *only* home for a highlight's block or a label's tick, only the one this
project falls back to.

The marker strip's invariant content is the ribbon, each highlight's block and each label's
tick. There is no heading over it: the word `MARKERS` used to be drawn there and has been
deleted rather than merely switched off. It named the *panel* rather than the thing on
screen, and a horizontal bar with coloured marks on it and a playhead sweeping across is a
scrubber, which is legible without a caption in a way the word never made it more so.
Removing it is also what freed the row the label name now occupies above the ribbon, with
the highlight name below — three rows in the box's height where there were four, and the
crowding that produced was the complaint that prompted the change. `Accepts` guarantees at
least one block or one tick exists, so the box is never a bare bar.

**One caveat, recorded because a reviewer will hit it and reach for the caption.** In a
*still* frame — `scripts/fd frames`, a thumbnail — nothing is sweeping, and the strip is a
dim bar with some coloured marks on it and no words at all. It reads as unfinished. It is
not: the motion is the content, the video is the product, and a single exported PNG of this
panel will always look sparser than the render it came from. The painter's own `Static`
comment says the same thing, and this paragraph exists so the two do not come to disagree.

## The elevation widgets: climb and gradient

Two panels share a full-width row directly above the elevation band, in both trees.
`climb` answers "how much climbing has this activity done so far", as two horizontal
tracks — gain above, loss below, up is up, so no legend says which is which — each filled
to the cumulative figure over a dim ghost showing the activity's own total. `gradient`
answers "how steep is it right now", as a single line that tilts with the terrain beside a
signed percentage. Both read the same elevation model the profile plots, and neither draws
any part of the other's content: three panels on one model, each placeable and — once
panels can be selected by name — each selectable on its own.

Three flags borrowed verbatim from videofx choose how that model is smoothed:
`--elevation-smoothing` sets the Gaussian width directly, `--elevation-gain` and
`--elevation-loss` give the smoothing a known total to tune itself against. The names, and
the help text minus its videofx-only prefix, are the sibling's, for the reason
`--power-source` already gives: a user moving between the two programs should not have to
learn the same idea twice under two spellings.

### One model, built once, and one tuning resolved once

The elevation model used to be built wherever it was needed. `ElevationPanel.Accepts`
built one to decide whether there was anything worth plotting, `Prepare` built another to
plot it, and `internal/render`'s `profileTakesTheBand` built a third to decide whether the
marker strip was absorbed. That was defensible while the cost was one pass over the
samples and the model had exactly one consumer. It stops being defensible on both counts
at once here: a *tuned* build is not one pass but a search — the library runs full
smoothing passes repeatedly, hunting the sigma whose computed totals match the target it
was given — and two more panels calling `Accepts` and `Prepare` apiece would have taken
the count from three builds a render to eight.

So `Context` gained `Elevation`, built once by whoever constructs the context.
`panel.BuildElevation` is the single spelling, and it is the only place a nil track is
turned into a nil model rather than handed to a library function that would range over
it.

**The cost argument is the smaller half.** The real one is that a second build site is a
second place the same track can produce a different model — the moment the tuning is
threaded through one call and not another, `Accepts` and `Prepare` are answering questions
about two different curves, and the failure is a panel that accepts an activity and then
draws something the acceptance test never saw. Building once removes the possibility
rather than documenting the requirement.

**The flags resolve in exactly one place too**, `cmd`'s `resolveElevationTuning`, in a
four-level precedence: an explicit `--elevation-smoothing` wins outright and the targets
are ignored (which is what the library already does once a sigma is set — the help says so
rather than leaving it to be found by experiment); otherwise a known gain or loss sets the
targets; otherwise the FIT file's own reported totals, which is today's silent behaviour,
unchanged in effect; otherwise the library's default. Letting each of three panels reach
its own conclusion from the same three flags is the disagreement this whole section exists
to prevent, one level up from the model itself.

That resolution is now **reported** rather than silent: one summary line naming the sigma
actually used and which of the four levels produced it, printed only when the activity
carries a model at all. It is the difference between a user being able to reason about why
a profile looks flatter than the ride felt and having to guess whether that is the terrain
or the tuning. The source label is carried as a plain string through `cmd`'s own
`renderInputs`, not on `Context`: a sigma the library derived and a sigma a user typed can
be the same number, so which of the two happened is a fact about how `resolveElevationTuning`
resolved the flags, not about anything a panel reads back off the model, and no panel has a
reason to see it.

### The strip row, and the two placements rejected

The row is placed as **the same fraction of each tree's total**, not the same number:
weight 1 in the landscape tree, whose rows sum to 8, and weight 2 in the portrait tree,
whose rows sum to 16. An eighth in both. Writing it as a fraction of the tree is the form
that survives editing — "measured against the four gauges" would be stale the first time
a user drops a gauge — and it is the answer to the mistake the `Alt` band's own weight
comment already records, where copying a landscape weight into portrait gave the same
number a smaller share of a larger total.

The cost is stated plainly rather than buried: everything above the row loses an eighth of
its height — the route, the clock, the distance readout and all four gauges. That is a
smaller and far more evenly spread cost than a fifth gauge would take out of the gauge
column alone.

**Inside the row, climb sits left and gradient right**, following the grammar the frame
already has: metrics that only ever increase on the left (the clock, distance, and now
gain and loss), metrics that fluctuate on the right (the gauges, and now the current
grade).

**The placement that costs nothing was rejected, and the second reason is a real bug.**
Putting all three panels inside the existing band as `Row{Profile, Climb, Gradient}` would
have taken no height from anything. It fails twice. First, `--bottom-band distance`
rejects `elevation` **by name** inside `internal/render`'s keep filter, so two more names
would have to be added there or the flag would half-work — and that is a change to
`internal/render`, which is precisely the tripwire adding a panel is supposed to leave
shut. Second, and worse: the band is an `Alt` slot, and an `Alt` slot hands its whole box
to the **first child surviving pruning**. If the profile ever declined while the two new
panels accepted, the row would prune to them, they would take the band, the distance
readout below them would never be asked, and **distance would disappear from the render** —
the exact failure "the cheaper alternative, and why it is a trap" above is about, arrived
at one panel later. Guarding it would mean yoking three `Accepts` methods together by hand
and keeping them yoked.

Folding all three into `ElevationPanel`'s own chrome was rejected for a different reason:
zero layout cost, one `Accepts`, one model — and one **name**, so a user could never
afterwards ask for the gradient without the profile.

### The layout episode: the slack was inside the panel, not in the split

This is the most useful thing here for anyone who touches the row next, because the
symptom presents as a layout problem and two rounds were spent treating it as one.

The two panels' content read as two groups with a gulf between them. The row's weights
were changed twice, in **opposite** directions, and neither worked. Giving climb more
width grew the gulf; giving it less shortened the gain and loss bars, which are the entire
reading the panel exists for. **No weight could have worked**, and it is worth being
precise about why: `climb` reserved its track a *fixed fraction* of its own box, so
whatever width it was handed, a fixed proportion of it was left over as empty space at the
panel's own right edge. A bigger box scaled that emptiness up. A smaller box scaled the
bars down. The slack was inside the panel; the split was never the thing that was wrong,
and no ratio between two boxes can remove space that one of them is going to leave empty
regardless.

The fix was to let the track take **whatever width remains** after the caption column, the
gaps and the reading column, and to anchor the reading immediately past the track's own
end rather than at the box's far edge. That removed the second rule competing with the
first: nothing downstream depends on where the box's edge is, only on where the track's
edge is. With no structural slack left, the two groups sit adjacent at *any* weights, and
the weights go back to meaning only what a weight should mean — how much width the bars
get relative to the gradient's line and reading. An even split was then a judgement made
on a rendered frame rather than an attempt to compensate for something.

Two consequences worth keeping. The gradient's content is sized off `min(box.W, box.H)`,
so on a wide, short strip it is bounded by the row's **height**; surplus width is daylight
inside its own box, and because it anchors its content at its own left edge that daylight
lands at the row's outer edge rather than between the two groups. And the alternative fix
— pushing each panel's content toward its neighbour — was rejected outright, because it
would depend on where the *neighbour's* box happens to sit, which is a layout fact no
panel can see, and it would therefore break silently the next time the weights moved.

### The gradient window: a distance, never a time

**The premise the request came with needed correcting, and the correction is the reason
the number is what it is.** The gradient was asked to be averaged "over a little period"
because elevation readings are notoriously inaccurate. That is true of raw readings, but
almost all of that averaging has already happened by the time this panel sees the data:
the model's own Gaussian smoothing is tuned against the file's totals, and on a tuned file
widening this window across an order of magnitude barely moves the reading at all. So the
window is not what rescues a noisy barometer — that job is done upstream — it is a
**second-stage filter choosing the run of ground the slope is measured over**. Planning to
the request while choosing the number on that understanding is the whole of the ruling.

**It is a distance window, and a time window was rejected on its merits rather than for
convenience.** Gradient is a property of terrain, not of the clock. A time window covers
whatever ground the athlete happened to cross in that time, so it shrinks the length of
road considered exactly where they are slowest — on the steep climb, which is where the
reading matters most and where a short window is noisiest — and smears hundreds of metres
into one figure on a fast descent. It would systematically under-report climbs and
over-smooth descents, in opposite directions on the same activity, and nothing on screen
would say which of the two you were looking at.

**Thirty metres is videofx's constant, taken verbatim, and the defence is agreement rather
than derivation.** The library query reads ±window, so this is a sixty-metre run of
ground. A freshly derived number might well be individually better; it would also mean two
programs reading the same file through the same library reporting different grades for the
same instant, which is worse than either number being suboptimal. That is the argument
`--power-source` already makes about shared vocabulary, extended from a word to a
constant.

**One widening, for one reason.** When a render is compressed hard enough that a single
frame stands for more ground than the window covers, the window opens to half that stride.
This is `smoothSample`'s own argument applied to distance: a reading measured over less
ground than the frame it is drawn on covers is an arbitrary pick from a span the render is
presenting as an instant. The stride is computed from the render's *coarsest* segment —
the timeline's maximum speedup, not its base rate — for the reason
`bindDistancePrecision` already uses the same figure: a highlight slowed toward real time
must never widen a window sized for the rest of the render. Half the stride, not all of
it, because the window is the radius of the span queried and the stride is its diameter.

The branch is known to be reachable rather than assumed to be. Take a hypothetical
ten-kilometre run of an hour, rendered at 480×, 30 fps: a frame advances something over
forty metres, half of that is under thirty, and the floor wins — the widening is inert.
Take a hypothetical hundred-kilometre ride of four hours at the same settings: a frame
advances a little over a hundred metres and the window opens to roughly fifty-five. It
bites on heavily compressed long rides and nowhere else.

**The consequence is a consistency guarantee, and it is why the two panels can sit beside
each other.** The gradient line reports the slope of the profile under the playhead over a
span of roughly a couple of dozen pixels at 1080p on an activity of a few kilometres —
wider on a shorter activity, narrower on a longer one. The two panels are showing the
same terrain at the same resolution, so they cannot visibly disagree about it.

Deriving the window from the model's own sigma was rejected: sigma is expressed in samples
rather than metres, converting it needs a point count the library does not expose, and it
is partly circular anyway, since sigma is tuned to match *vertical* totals rather than to
choose a *horizontal* resolution. A raw two-point slope was rejected because at real-time
pace two points a tenth of a metre apart on a barometric trace produce noise wearing a
number's clothes.

### The tilt is amplified by a constant, and the constant never moves

**A line drawn at the terrain's true angle is invisible at every gradient anyone actually
rides.** Ten percent is 5.7 degrees; twenty percent is 11.3. On a strip a few dozen pixels
tall both read as flat, and a panel whose whole design is "the line leans the way the
ground does" would lean imperceptibly for the entire render.

So the drawn angle is the **true** angle — `atan(grade)`, the honest geometric tilt that a
rise over a run actually has — multiplied by a fixed factor and clamped at 45 degrees.
Using `atan` rather than the grade fraction itself matters: grade *is* the tangent of the
slope angle by definition, so treating the fraction as though it were already an angle
would over-tilt shallow ground and under-tilt steep. Multiplying afterwards preserves
proportionality: double the gradient is double the drawn tilt, right up to the clamp,
which keeps the exaggeration a single stated constant rather than a curve nobody can
invert by eye.

The factor is 4, and it is chosen rather than merely asserted: solving for the grade at
which the amplified angle first reaches the 45-degree clamp gives just under 20%, which is
about the steepest sustained gradient a paved road or maintained trail reaches. The clamp
lands where the terrain does.

**Why it is fixed, and why this is the part a future reader will want to "improve".** The
original design derived the amplification from the activity's own steepest section, so
every render used its full range. That was honest — but only because that design drew a
labelled dial with limit rays to read the tilt against. Once the visual became a bare line,
the scale went with the dial, and a derived amplification would mean **the same tilt on
screen represented a different real gradient on a different activity, with nothing on
screen saying so**. A viewer comparing two of their own renders would be comparing two
scales they were never told about. Comparability was chosen deliberately: the same tilt
means the same gradient in every render fitdash produces, which is only true while the
constant is a constant.

A per-activity scale is the natural thing to reach for — it uses the available range, it
is one line — and it is only wrong in the absence of the dial. That is exactly why it is
recorded here rather than left as an obvious optimisation somebody performs on a Tuesday.

**What keeps an exaggerated line honest is the division of labour.** The line is a
qualitative signal — climbing, level, descending, and roughly how hard — and the signed
percentage beside it is the measurement, printed unexaggerated in videofx's own format
string (`%+.1f%%`) so the same instant reads identically in both programs. Neither half
would be defensible on its own: the line alone overstates, and the number alone is exactly
the bare readout the request asked for something more interesting than.

### No easing between frames, which is unavailable as well as unnecessary

The obvious refinement to a value that tilts a line is to ease it toward its target rather
than snapping. It is not done here, and it is the one place in this panel a reviewer will
reach for the forbidden thing.

**It is unavailable.** Easing needs the previous frame's value kept somewhere, and
`Dynamic` must not mutate its `Painter` — the rule that holds the door open for parallel
frame rendering, and the same rule that already forbids holding a stale fill extent (see
"When the placeholder is an area"). There is nowhere honest to keep the state.

**It is also unnecessary, which is what makes the restriction cost nothing here.** The
window is sixty metres of ground, and at running pace a frame at 30 fps advances on the
order of a tenth of a metre against it. Each frame's reading therefore differs from the
last by a fraction of a percent of the window's own local variation: the line is smooth
**by construction**, and there is nothing left to ease toward. Choosing a distance window
is what bought that property, which is why the two rulings are best read together.

### The gradient carries no caption, and what makes that safe

The panel draws no `GRADIENT` heading. The word names the *panel*, not the thing on
screen, and a line leaning beside a signed percentage is legible without being told it is
a gradient — the same argument that deleted the marker strip's `MARKERS` caption, and the
same disposal: deleted rather than switched off.

**The structural half of the argument is what makes it safe rather than merely tidy, and
it is a dependency between two panels that nothing in the type system records.** Both
panels' `Accepts` is the *identical* shared predicate, so the gradient is never placed
without the climb panel's captioned gain and loss bars beside it in the same row. The
context a viewer uses to read a bare `+6.1%` — "this sits next to a climb readout, so it
is a grade" — is guaranteed by that shared predicate rather than by luck: the two accept
and decline together, so an activity that gets a gradient gets the captioned bars too.
Where the row happens to put them is a separate and weaker guarantee, about adjacency
only; the predicate is the one that matters, because it is what keeps "a bare percentage
never appears alone" true of layouts nobody has written yet. **Anyone who later gives the
two panels different accept conditions silently removes the thing that makes the missing
caption defensible**, and nothing will fail: the render will simply contain a number with
no unit and no context on an activity where climb declined and gradient did not. That is
why the dependency is written down in both directions, here and in the panel's own doc
comment.

### One shared scale for gain and loss

The two tracks are drawn against **one** scale, the larger of the activity's own total
gain and total loss. This is the misreading the design exists to prevent: a viewer
compares the two bars on sight, and normalising each to its own total would make two bars
of equal length mean two different numbers of metres — a lie told in the one dimension the
panel is asking to be read in. Under a shared scale the shorter track's ghost simply ends
short, which states the ratio between the two for free, with no legend and no second
number.

There is no colour coding of up against down. `Theme` has no such role, and a panel
inventing one is the "eight panels each picking their own grey" failure `canvas.go` names.
Sign, caption and vertical position carry it already.

The ghost track is also the cleanest instance of the static/dynamic split in the project:
`Static` draws the destination — the activity's totals, fixed for the whole render — and
`Dynamic` draws the progress. That is what makes a single exported PNG of this panel
legible on its own, which is the weakness the marker strip's own comment records about a
design that only reads in motion.

### Absent data for both panels

Both panels use the **same** accept predicate as the profile, extracted so all three ask
one question rather than each keeping a copy that can drift: elevation and distance both
carried, a model that exists and is not empty, and a real elevation range over a real
distance span. A model the profile declines to plot but the climb bars happily draw from
is the two-panels-disagree failure reached at a different seam.

Activity-level absence is therefore a **decline** for both — an indoor ride's row prunes
entirely and the layout closes up around it. Per-frame absence is a **placeholder**, and
deliberately not a plausible-looking zero: the climb tracks wash their full length in the
profile's own absent fill colour with both readings showing `--`, and the gradient draws
**no line at all** rather than parking one level, because 0% is a real, drawable grade and
parking there is the confident lie in geometric form. The per-frame branch fires on an
ordinary distance dropout and on the pre-data region that "The pre-data trap" below
is about.

## The gauges: a reading against a scale

Four readouts fluctuate rather than only ever increasing — heart rate, pace, power and
cadence — and a bare number gives a viewer no way to tell whether it is a hard effort or
an easy one. `--gauge-style` lets those four draw their reading against a scale in one of
two shapes: `track` puts a horizontal axis beneath the number with both ends labelled and
a marker at the current value, and `dial` puts the identical scale beside the number
instead, as a semicircular arc with a needle. `plain`, the default, is the three centred
rows and nothing else, and is byte-for-byte what it always was.

The two shapes resolve their range through the same rule, so switching between them
changes the shape and nothing else. That is worth stating because the alternative — each
shape deriving its own comfortable range — would make the flag change the reading as well
as its presentation, and a viewer switching styles to see which they preferred would be
comparing two things at once.

### The scale is derived per activity, and that is the opposite of the gradient's ruling

The gradient panel amplifies its tilt by a **fixed** constant, and the section above says
at length why: once the visual became a bare line with no dial to read it against, a
per-activity scale would mean the same tilt on screen represented a different real
gradient on a different activity, with nothing on screen saying so.

The gauges derive their range from the activity anyway, and the two rulings are consistent
rather than contradictory because **the gauges label both endpoints**. A track that reads
`100` at one end and `190` at the other has stated its scale; a viewer comparing two
renders can see that the scales differ, because both are written on the screen they are
looking at. What made a derived amplification dishonest was the absence of the statement,
not the derivation. Drawing the endpoints is the price of deriving the range, and it is
why the labels are load-bearing rather than decoration — a future tidy-up that drops them
to buy back a few pixels of height removes the thing that makes the derivation defensible,
and nothing will fail.

The two panels therefore look inconsistent side by side: one instrument scaled to the
activity, one scaled to a constant. That inconsistency is deliberate and follows from
which of them states its scale.

### The statistic comes from the readout's own accessor, never from the coverage report

`internal/inspect` is the single source of truth for whether an activity carries a metric
at all, and the obvious way to build a scale is to ask it for the metric's range — a field
on `Report`, or a helper taking a metric name. It is the first simplification a reader
will reach for, and it is wrong.

The report describes the **recorded** space. A readout draws in the **presented** space,
and for three of these four metrics the two differ:

- **Heart rate** refuses a recorded zero, because a zero heart rate would mean the person
  is dead — a strap that has not yet picked up a signal, not a reading. The report counts
  those samples as present.
- **Power** may be drawing a Stryd developer field rather than the native FIT power row,
  depending on `--power-source`. The report has one `Power` row and does not know which
  sensor this render chose.
- **Cadence** doubles rpm to spm on a running sport. The report's own range stays in the
  recorded rpm.

So a scale read off the report would put heart rate's floor at a value the number beside
it can never print, and would scale a runner's cadence track to half the figure printed on
it — a two-to-one disagreement between an axis and the number sitting on it, in pixels
nobody can inspect. The range is therefore built by walking the track through the
readout's **own bound value accessor**, the same closure `Dynamic` prints from. That is
not indirection for its own sake: it is what makes it impossible for the scale and the
number to disagree about which samples exist and in what units, because there is only one
accessor and both go through it.

Moving the range onto `Report` would also make `fitdash inspect`'s printed output depend
on a render flag, which is a strictly worse outcome than the duplication it saves:
`--power-source stryd` would change what the coverage report prints about an activity, and
the report is supposed to describe the file.

### The axis spans a smoothed series, and the marker rides that same series

The marker does not move on the sample the rest of the frame is drawn from. It moves on a
**second, more heavily smoothed series**, built once in `Prepare` by walking the track
through the readout's own bound accessor, and the axis spans **that series' own literal
minimum and maximum**. The printed number is untouched: it comes from `Frame.Sample`,
subject only to `--smoothing`, and is never extra-smoothed and never clipped.

One series drives both the axis and the marker, and that is what makes the guarantee
hold: the marker reaches each end of its track at most once and **cannot max out**, while
no single instant can stretch the axis, because no single instant survives the smoothing.

Three earlier derivations were tried and each failed in a way worth recording, because
each is a plausible thing to reinstate.

**Raw minimum to maximum.** One instant dictates the axis and the rest of the activity is
compressed into a corner of it. The two failures are properties of the metrics, not of
any one file: an activity's slowest speed is zero, whose pace is not a large number but
an undefined one, so a pace axis anchored at the true minimum has no finite low end at
all; and a power peak is a single sample several times the sustained effort — a standing
start, a sprint, or a sensor artefact — so an axis stretched to reach it spends its whole
sweep on a value the activity visited once.

**The 5th to 95th percentile of the raw readings.** This fixed the spike and the stop, and
it is what the deleted `inspect.Quantiles` helper existed for. It failed the other way:
clipping is not rare at the tails, and in use the marker sat pinned at one end or the
other often enough to read as broken rather than as informative. An instrument that
saturates during ordinary use is not reporting.

**A hard zero floor for power and cadence.** Recorded in its own section below.

Smoothing the series is what lets a **literal** range be safe, which the first two
attempts could not achieve by choosing a better statistic. It is also why the off-scale
chevrons survive rather than becoming unreachable: the raw number can still pass what the
smoothed marker shows, and when it does, that divergence is exactly the event the chevron
now reports.

The extra smoothing is a multiple of the render's own `--smoothing` window and is a
judgement call documented as one. It is built in `Prepare`, never in `Dynamic` — that is
what keeps a panel change out of `internal/render` and off the tripwire.

### The endpoints snap outward to round numbers

The series' own ends are rounded away from the middle of the range to a per-metric step, so a
track reads `100`–`190`, not `103`–`187`. Two reasons, and the second is the one that
would be missed:

**It makes the endpoint a statement about the axis rather than a claim about the
activity.** "This track reads 100 to 190" is a fact about the instrument. "You hit 187" is
a fact about the workout, and printing it at the end of an axis invites it to be read as
a personal best when it is the extreme of a smoothed series.

**It interacts with smoothing.** On a compressed render `--smoothing` averages each
reading over a window of activity time, so the value actually drawn is a window average
and lies strictly inside the raw range. A marker scaled to the raw peak would therefore
**provably never reach the end of its own track** — and a viewer reads "never hit the top"
as a fact about the effort, not as an artefact of averaging. Snapping outward buys the
headroom that makes the gap unremarkable instead of misleading.

Pace snaps in **pace** space and only then converts back to speed, because the number a
viewer reads at the end of the axis is a pace: rounding to a 30-second step gives `4:00`
and `7:00`, where rounding the speed would give whatever pace a round speed happens to
land on.

### Sweep on speed, label in pace

Pace's marker moves on **speed** while its label and its endpoint text read as **pace**.
The two run in opposite directions — more speed is a smaller pace figure — so the marker
travels toward the fast end as the printed number falls.

This is the right way round because "further along the axis" should mean "more of the
thing being measured", and it is only tolerable because the reading is a marker rather
than a fill. See the next section: a fill that grew as its own number shrank read as a
contradiction, and a dot at a position makes no such claim.

### No hard zero floor for power or cadence

Zero is a real, meaningful reading for both: coasting on a bike, and the gap between
strides. The first version of this therefore forced their floors to `0`, on the reasoning
that a genuine reading deserves a place on the axis.

A render gate overturned that. With the floor at zero, cadence's whole working band was
pressed against the top of its own axis, and the marker travelled a handful of pixels
across a track hundreds of pixels wide for the middle half of the video — an instrument
that admitted every possible reading and communicated none of them. The exact figures are
not reproduced here: they were measured against a private recording, and the band
somebody's cadence occupies is their data, not this project's. The reasoning had one word
wrong: a
real reading must be **reported** honestly, not **positioned** on the axis. A zero is
reported by the below-floor chevron plus the true, unclipped printed number, which is a
complete and honest account of it.

So there is no hard-floor parameter, and there should not be one again. "0 W is a real
reading, force the floor" is a correct observation attached to the wrong remedy, and it is
recorded here because it is the kind of thing that gets reinstated as an obvious fix.

### A marker, not a fill

The reading is a dot on the axis, not a bar filled from the floor. A fill invokes
**quantity**, and quantity was wrong here three times over:

- **It contradicted pace.** Sweeping on speed under a label reading pace meant a slower
  runner filled *more* of the track, and at an ordinary effort the pace gauge looked like
  the emptiest thing on the dashboard.
- **It collided with the climb bars.** `ClimbPanel`'s tracks are horizontal bars that fill
  from the left and only ever grow. In the portrait tree a review found four gauges
  stacked above them using the identical visual grammar for a value that goes up and down,
  and colour was rejected as the *distinguishing* device: a difference carried by colour
  alone is no difference for a viewer who cannot see it. A different shape distinguishes
  them at every reading. (The gauges did later gain a colour ramp — see below — but it
  encodes the value redundantly and never distinguishes one panel from another.)
- **It broke the absent state.** A dropout washed the full-length fill, and the wash was
  measured at 1.37:1 against the dim ghost it sits on — indistinguishable from a genuine
  low reading. "No marker at all" cannot be confused with "marker at the left end", which
  is what makes the absent policy legible.

A short fill anchored at the left end was also, at a low reading, only a few pixels
different from the below-floor chevron that lives in that exact spot.

### The absent wash is solved against the ghost, not against the background

`ElevationPanel` and `ClimbPanel` derive their dropout wash's alpha for contrast against
`Theme.Background`, which is correct for them: that is what they wash onto. The gauge's
ghost track is already drawn in `Theme.Dim`, so the gauge's wash composites onto **`Dim`**,
and reusing the elevation constant solves the wrong equation — measured directly, it lands
under the contrast floor either derivation is trying to guarantee, because `Absent` and
`Dim` are inherently close in luminance in both shipped themes.

The two derivations are the same arithmetic applied to two different backgrounds. Somebody
will notice them and want to unify them into one shared helper; that is the bug, not the
duplication. **A wash is solved against the colour it is actually drawn over**, and
`Theme.Background` is a default that happens to be right for the panels that invented it.

Once the axis gained its colour ramp the backdrop stopped being a single flat colour, so
the gauge's alpha is solved across the tinted samples rather than against `Dim` alone —
the same rule applied to the backdrop that now exists.

### The colour ramp encodes the value, and never carries it alone

The axis carries a green-through-amber-to-red gradient along its length, and the marker or
needle takes the ramp's colour at its own position. This is the one place in the project
where a panel names colours that are not `Theme` roles, and the exception is deliberate
rather than an oversight: it is a **gauge-specific palette**, not a new general role, and
folding it into `Theme` would oblige every future theme to have an opinion about effort.

The accessibility answer is **redundant encoding**, and it is a constraint on the code
rather than a hope. Green-to-red is the common colour-vision-deficiency pairing, so the
marker's *position* must carry the identical information the colour does — the fraction
along the scale is computed once and feeds both. Nothing in the widget may be
distinguishable by colour alone, which is also why the ramp could not have been the answer
to telling the gauge tracks apart from the climb bars.

The two colours that already carry meaning are unaffected: the grey absent wash still
reads as absent, and the off-scale chevron stays in `Theme.Foreground` so it reads as a
live mark rather than as a point on the ramp.

### The dial sits beside the text, not above it

A gauge box is roughly 3.6:1 in the landscape tree — wide and short. A semicircle is
roughly square. The first version stacked the caption, value and unit above a centred arc
and left about 71% of the box's width empty, paying for the arc's height by shrinking the
number and the unit that are the panel's actual content.

Placing the arc in a near-square column with the text beside it uses the width the box
already has. This is `ClimbPanel`'s arrangement for the same reason — instrument here,
reading there — with one role reversed: `ClimbPanel` sizes its text columns first and lets
the track absorb the remainder, because a linear track has no correct size to defend,
whereas an arc must stay roughly square or it stops reading as a dial, so the arc is sized
first and the text takes what is left.

The consequence worth recording is what did **not** happen: `layouts.go` is untouched. A
dial did not need a squarer 2×2 gauge block, so no width was taken from the route panel
and the layout weights were not reopened — a tuning exercise that has cost this project
two rounds already.

### One panel type and a style flag, not new panel types

`heart-rate` is one panel whose reading can be drawn three ways, not three panels. Panel
names are the selection key for the `--panels` flag this design anticipates, so the name
has to survive a change of shape: a user asking for `heart-rate` must get it whichever
style is in force, and `heart-rate-dial` would make the flag's vocabulary depend on an
unrelated flag's value.

Four metrics times three styles as separate types would also be twelve types for one
behaviour, each carrying its own copy of the placeholder branch — which is the branch this
project is least willing to have four copies of.

### The four drawing states

The gauge has four states, and only three of them draw an instrument:

| State | Knowable | Drawn |
|---|---|---|
| **No usable range** — too few present readings, or a degenerate range | `Prepare`, once | No instrument at all; the readout falls back to `plain` and the summary names it |
| **Absent this frame** | per frame | The axis or arc washed, **no marker**, `--` in place of the number |
| **Off scale** | per frame | The chevron at the end it went off, **no marker**, and the true unclipped number |
| **In range** | per frame | The marker at the fraction the scale implies |

The first is resolved once, on the per-render context, and the range is **never**
re-derived or widened afterwards. The tempting variant — "the scale is the maximum seen so
far, so the axis always uses its full range" — is this project's axis-origin bug rebuilt:
the endpoint labels are rasterized once into the static layer, so they would be right for
frame 0 and wrong for every frame after it, both layers would draw, they would disagree,
and nothing would error.

Falling back to `plain` rather than declining the panel is the right response to "no usable
range" because the *number* is still perfectly good — there is nothing wrong with the
reading, only with the axis it would have been drawn against, and declining would remove a
metric the activity actually carries. The render summary names any gauge that fell back,
so a track missing from one gauge and not its neighbours has an explanation the viewer can
read.

## The balance bars: a fixed scale, because comparison is the point

`--gauges balance` replaces the four gauge readouts with pace, step length and four
centre-anchored bars, one per left/right balance metric. Pace and step length stay because
balance varies with effort and stride, and the three are meant to be read together; they sit
at the top, side by side at equal weight, because they are the context the bars are read
against. Step length is an ordinary magnitude — a `Readout`, like pace — never a fifth bar:
it has no natural "even" midpoint the way a left/right split does, so the fixed-scale
machinery below does not apply to it, and it is presented in centimetres rather than the
FIT field's own millimetres, converted at the presentation layer the same way `Cadence`
converts rpm to spm.

### The scale is a constant, which is the gauges' ruling inverted

The gauges derive their range per activity and label both endpoints. These do the opposite,
and the difference is not an inconsistency to be tidied away.

Heart rate has neither a natural anchor nor a natural width, so a gauge's scale has to come
from the activity, and a derived scale is only honest when the screen states it. Balance has
both: **even is a real anchor**, not an arbitrary one, and the useful width is a property of
human gait rather than of one recording.

More decisively, **comparison is what this instrument is for** — in two directions at once.
Across the four bars, so that two equal fills mean two equal asymmetries and the spread
between the metrics is itself readable. And across renders, because balance is something a
person compares against themselves over months rather than within a single video. A derived
range breaks both: the same fill would mean a different asymmetry on a different day, and
labelling the endpoints would make that honest without making it comparable.

So the range is fixed, and — exactly as `GradientPanel`'s fixed amplification draws no dial
— it needs no endpoint labels. What keeps it honest is the unexaggerated number beside it.

**If this is ever made derived, endpoint labels become mandatory that same day.** And if the
range proves too narrow, widen the constant *once, for everybody*: never per activity, and
never per metric. Deriving it per metric would be the more tempting mistake, because the
metrics genuinely differ in spread — and that spread is a finding the shared scale exists to
show, not noise for each bar to normalise away.

### A fill, here, and a marker on the gauges

The gauges rejected a fill because a fill invokes quantity and their quantity ran backwards
against its own label. Here the quantity is real and starts at zero at the anchor — how much
asymmetry — and length and side are two independent facts in two non-colour channels. It
does not collide with `ClimbPanel`'s bars, which fill from the left edge and only grow.

### Which side the bar names

Which side these fields report was originally left unconfirmed. FIT's `stance_time_balance`
is conventionally the left share; the Stryd developer fields carried no *documented*
convention this project could confirm on its own.

That convention is now confirmed for all four, checked against a user's own activity
summary — its own official left/right split for each of the four quantities, never
reproduced in this repository (see `CLAUDE.md`) — and every one of the four fields tracked
that summary's LEFT-side figure. So all four carry the **left** foot's share, uniformly, and
a reading above the midpoint means the left side dominates.

A centre-anchored bar therefore now names a side: the magnitude and a letter (`L` or `R`,
`EVEN` with no letter where the magnitude rounds to zero) are printed together, and the
bar's own fill direction is drawn from the identical sign, so the two can never disagree
about which side a positive deviation names. The letter goes in the **value** row, drawn
fresh every frame, and never the unit row — the unit row is drawn once in `Static`, and the
side genuinely flips mid-activity (whichever foot leads at one instant can trail at the
next). `Distance`'s doc comment records the same trap with metres and kilometres.

This was the one place a code review could not have caught the fix: the arithmetic reads as
equally plausible with the sign either way, and only checking a fill's own direction against
a source of truth outside the repository could tell the two apart.

### These panels do not read the smoothed sample

This is the subtlest thing in the feature. `internal/render` averages developer fields over
the smoothing window, and a developer field's presence test is merely that its key exists —
so a zero refused by the panel's own accessor is averaged straight back in.

For a gauge that is untidy. On a scale a few points wide it is fatal, and predictably so: the
zeros form a contiguous run at the very start, while the footpod computes its first balance,
so a window of tens of seconds of activity would drag the reading off scale and pin the bar
through the opening of the video with nothing on screen to explain it.

Each panel therefore builds its own series in `Prepare`, through its own zero-refusing
accessor, reusing the gauges' existing series machinery. Both the bar and the number come
from that one series, so they cannot disagree about which instants counted.

**`smoothSample` is deliberately not fixed instead.** It is `internal/render` and this is a
panel change; it cannot know which developer fields are balances; and zero is a legitimate
reading for other developer fields. That a zero is not a balance is a domain judgement, and
it belongs to the panel that draws it — the same division `HeartRate` already makes about a
recorded zero heart rate.

### Selection, and the one line it costs the frame loop

`--gauges` is not a `--layout` value: `--layout` is about the frame's aspect and picks by
shape, so a content value there would either lose that or spawn a balance variant of every
aspect. It is also not `--panels` (see Open items), which needs a superseded outcome in the
engine and a whole selection vocabulary — none of it required to answer one binary question
about one box. It converts to a name set cleanly when `--panels` lands. The mechanism is the
same `Alt` slot `--bottom-band` uses.

It costs `internal/render` exactly one condition, a deliberate exception to the rule that
adding a panel must not touch the frame loop. **It is paid once and never again:** membership
is a type assertion exported from `internal/panel`, never a list of names, so a fifth balance
metric costs that filter nothing. This is the mistake `--bottom-band` already paid for.

Selection could not live in `Accepts`. A panel declining because a flag is off would be
reported as "the activity has no impact balance", which is false.

### Two summary lines, and the contradiction the second one found

When the balance branch wins, the four gauges are never asked and appear in neither the
declined nor the omitted list — three panels vanishing with no explanation, a worse case than
the single readout the `Alt` gap was originally written about. One line says pace was kept and
which panels are not shown. When an activity carries none of the four metrics the branch
prunes away entirely and the ordinary gauges draw, which would otherwise look like the flag
being ignored; the other line says so outright.

Building that fallback exposed a contradiction worth recording. Pace is seated in **both**
branches, so when the balance branch loses, its copy declines while the metrics branch's own
pace draws — and the summary printed `pace` under "carries no such data" on the same render
whose `panels:` line listed pace as drawn. Both cannot be true, and the false one is the
decline: a pace panel just drew from this activity, so the activity demonstrably carries pace.

A name that also drew is now filtered out of that heading. The rule is deliberately about the
two lists rather than about pace, so it keeps holding for the next panel seated twice.

## Absent data

The two policies map onto the two phases, and the mapping follows from *when* each kind of
absence can be known.

| Kind | Knowable | Policy |
|---|---|---|
| **Activity-level** — this file has no power at all (indoor ride, pool swim, walk) | `Accepts`, before boxes exist | **Decline.** The panel is pruned and the layout closes up. |
| **Instant-level** — the strap dropped for 90 s; this frame is inside a GPS gap | only per frame, in `Dynamic` | **Placeholder.** The box is assigned and the static layer already drawn. |

So **`Accepts` is the policy.** A panel returning `true` unconditionally has chosen
placeholder; one that inspects the context has chosen decline. There is deliberately no
separate `Policy()` method: a second declaration of the policy is a second thing that can
disagree with the first.

A panel that declines per *frame* is not expressible, and that is intentional. It would
defeat the static layer entirely, and a layout reflowing while the video plays is visual
noise. **The layout closes up once, before frame 0.**

### Absence composes

The render loop guarantees:

> `Frame.HasSample == false` implies `Frame.Sample == fitactivity.Sample{}`

`AtWithGap` already returns a zero sample deep inside a gap, so this is free — but it is
stated as a guarantee and tested, because the tempting "helpful" change is to hold the last
good sample so the dashboard does not flicker. **Holding a stale value is the confident lie
in its purest form**: a heart rate frozen at 148 through a two-minute dropout is
indistinguishable from a live reading.

The payoff is that a panel needs one check, not a compound condition. Because every presence
flag on a zero `Sample` is false, this is already correct inside a dropout:

```go
if f.Sample.HasHeartRate {
	c.Text(fmt.Sprintf("%d", f.Sample.HeartRate), …)
} else {
	c.Text("--", …, c.Theme.Absent)
}
```

The partial case falls out of the same branch: `AtWithGap` sets a field only when both
bracketing samples carry it, so a strap that dropped on one side yields
`HasHeartRate == false` with GPS still present.

### When the placeholder is an area

The `--` above is the easy shape of a placeholder: there is text, and absence replaces it
with different text in `Theme.Absent`. The elevation profile's distance fill is the same
decision taken where there is no text to swap out, and it is worth writing down because
the shape of the answer changed while the rule did not.

**A distance dropout washes the whole axis in the absent colour**, at the fill's own
opacity, and nothing else about the panel changes: the trace, the baseline rule and every
label keep drawing, because they are all still true. What is missing is a *position* on
the profile, not the profile. Both of the answers that look more natural are wrong:

- **Drawing nothing is not neutral.** An empty plot is pixel-identical to a fill of zero
  length, which says the activity is back at the start line — a confident lie told in
  pixels the viewer cannot inspect, and simultaneously indistinguishable from the panel
  having crashed. Absence has to be marked *positively* or it is indistinguishable from a
  claim.
- **Holding the previous frame's fill is not merely discouraged, it is unavailable.**
  `Dynamic` must not mutate its `Painter`, so there is no last-frame extent to hold on to;
  the panel contract forbids the wrong answer before anyone gets the chance to choose it.
  That rule was written for parallel frame rendering, and this is the first place it pays
  for itself in honesty instead.

Washing the axis says "the extent of your progress is unknown" without inventing an
extent, which is exactly what the readout it replaced said with `--`, in the fill's own
vocabulary rather than a new one.

**How far that distinguishability is actually guaranteed is worth stating precisely,
because the obvious claim is too strong.** `Theme.Absent` is contractually distinct from
`Foreground` and `Background`, and the absent wash derives its own alpha — separately from
the covered fill's — so that it clears the same 1.5:1 floor against the background that
`cmd/render.go` already enforces on a user's `background=`. That much holds for every
shipped theme, and a test measures it rather than asserting it.

What does **not** hold is the tempting extension of it: that the two fills must therefore
stay distinguishable from each other whatever the background beneath them. Under
`--highlight-style wash` the background is whatever the user typed, and a wash set at or
near `Theme.Absent` collapses the difference entirely — the dropout then renders
indistinguishable from a fill of zero. That case is not defended here; it is what the
`background=` contrast warning exists to tell the user, and this panel relies on that
warning rather than re-deriving the check. Saying "distinguishable by the theme contract
rather than by luck" without that qualification would be a guarantee this design does not
make.

**The structurally missing stretch uses the same treatment as the momentary one**, and
that is deliberate. The elevation model keeps only samples carrying both distance and
altitude, so the metres between distance zero and the first altitude reading — a barometer
settling after the distance stream has started — are real distance with no terrain
attached to them, permanently rather than for one frame. That region draws in the same
absent wash, advancing with distance so the opening of an activity never reads as no
movement at all, but as a plain full-height band rather than a shape under a curve,
because there is no curve there to follow and this project does not invent one. It carries
no position dot for the same reason. A reader who sees the absent colour learns "no
terrain data here", which is the fact that matters; *which* of the two mechanisms produced
it is not something the pixels should be trying to say.

### The pre-data trap: a confident answer that has to be refused

Every absent case above announces itself. A missing heart rate arrives as
`HasHeartRate == false`; a distance dropout arrives as a zero `Sample`. The panel has to
check, but it cannot fail to notice that there is something to check.

**The elevation model's distance queries are not like that, and this is the sharpest edge
in the whole render path.** `ElevationModel.AtDistance` and `GradeAtDistance` both *clamp*
their argument to the profile's own ends. Asked for a distance before `StartDistance()` —
the metres between the distance stream starting and the barometer's first usable reading,
which is an ordinary opening on a real recording rather than a corner case — they return
the profile's first values: gain 0, loss 0, grade 0. No error, no second return value, no
flag. A cumulative-gain reading of zero at the start of an activity is also exactly what a
*correct* answer looks like there, which is what makes the trap a trap: the wrong number
and the right number are the same number, and only the distance the caller passed in
distinguishes them.

The clamp is not a defect. It is the right behaviour for a plotting query that must not
panic on an out-of-range x, and it is the right answer at the *other* end, past
`TotalDistance()`, where the clamped values are the activity's own finished totals and its
last measured slope. All three panels keep drawing live readings past the axis end for
exactly that reason.

**What makes the near end different is that a second panel is already saying otherwise in
the same frame.** The profile washes that opening stretch in `Theme.Absent` and refuses its
position dot (see "The structurally missing stretch" above). If the climb bars printed
`0 m` and the gradient printed `+0.0%` over that same span, the frame would contain two
panels contradicting each other about one instant — one saying "no terrain data here", the
others stating terrain facts with a decimal place — with nothing on screen to adjudicate
and nothing in the code that failed. So both new panels compare `f.Sample.Distance`
against the model's own `StartDistance()` **before** calling into the model at all, and
take their placeholder branch when it is below: the climb bars wash their full length,
both readings show `--`, and the gradient draws no line and reads `--`.

**The generalisation, which is why this sits in the absent-data chapter rather than in a
panel's own comments: absence policy is not only about the flags on `Sample`.** A library
call that cannot express "I do not know" pushes that judgement back onto its caller, and a
caller that simply prints what it was handed will produce a confident lie without ever
writing a line of code that looks wrong. Here the honest region is knowable — it is a
comparison against a number the model publishes — but it has to be *written*, because the
default is the lie. Any future panel reading a clamping model owes the same check, and any
future model here that clamps should be read with this paragraph in mind.

### One rule for absence

`Context` carries an `inspect.Report`. `internal/inspect` already computes, per metric,
exactly what `Accepts` needs, and already documents why coverage rather than presence is the
unit.

So fitdash has **one** rule for "does this activity have power", shared by the `inspect`
command and by the panels. A panel that re-derived it by walking the samples would be free
to disagree with the report the user just printed — same file, two answers, and the
disagreement surfaces only as a panel missing when `inspect` said it should be there.

The uniform threshold: **decline at zero coverage, keep above it.** A metric present 4% of
the time is a metric with a very long dropout, and the placeholder path already handles
dropouts honestly. A percentage threshold would need a defensible number and there isn't one.

## Per-render and per-frame state

```go
type Context struct {                  // computed ONCE, identical on every frame
	Track           *fitactivity.Track
	Report          inspect.Report
	Timer           *fitactivity.TimerModel
	Elevation       *fitactivity.ElevationModel
	Splits          *fitactivity.Splits        // not built
	Route           []GeoPoint                 // not built
	Timeline        Timeline
	Width, Height   int
	FontScale       float64
	PowerSource     fitactivity.PowerSource
}

type Frame struct {                    // NOTHING constant across the render
	Index           int
	At              time.Time
	Elapsed, Active time.Duration
	Sample          fitactivity.Sample
	HasSample       bool
	Paused          bool
}
```

**This sketch is partly aspirational, and which parts is worth saying rather than leaving
a reader to discover by compiling.** `Track`, `Report`, `Timer`, `Timeline`, the
dimensions, `FontScale` and `PowerSource` are real and read by panels today. `Elevation`
became real when the climb and gradient panels landed — see "One model, built once, and
one tuning resolved once" above for why the field exists at all, and why `Accepts` was not
a sustainable place to keep building it. The tuning that produced it is deliberately *not*
a field here — see that same section for why it is a plain string threaded through `cmd`'s
own `renderInputs` instead. `Splits` and `Route` are **still not built**: no code
populates them and no panel reads them. They are listed
because the shape of the context is the design's claim about what is per-render, and a
splits panel or a projected route cached on the context would go here rather than being
recomputed per frame — but nothing about them is decided, and neither field should be
treated as an interface anything depends on.

Four things keep the static-layer trap shut, and the first is load-bearing:

1. **`Static` takes no `Frame`.** The trap needs a per-frame value that is readable but
   absent during the static pass. There is none.
2. **`Frame` holds no pointer to `Context`.** videofx's `Frame` carries the render-wide
   context as a field, which is what made "which of these is per-render?" a doc-comment
   question. Here the direction is reversed: the Painter captured what it needs in
   `Prepare`, and `Frame` has no back-channel.
3. **`Canvas` carries nothing per-frame either** — dimensions, theme, base text size, face
   cache. If it carried `Context`, the static pass could reach frame state through the
   drawing surface, which is the same leak in a third costume.
4. **The equality test.** `Renderer` exposes both paths — `Render(img, f)` doing static and
   dynamic together, and `RenderStatic(base)` + `RenderDynamic(img, f)`, which is what the
   loop uses. videofx has both and never compares them. **fitdash asserts they are
   pixel-identical** for a fixture activity at several frame indices. If any panel's static
   content ever depends on something that differs between the paths, the images differ and
   the test fails. It is two small buffers and a byte comparison.

   The claim only holds for panels the test actually resolves. The original version of
   this test built its own mock panels over a synthetic `Layout`, never `panel.LandscapeLayout`
   or `panel.PortraitLayout`, so `ElevationPanel`, `ClimbPanel` and `GradientPanel` — the
   three panels whose own doc comments forbid caching a value across frames, precisely
   *because* they trusted this net to catch a smuggled-in cache — were never exercised by
   it at all. A second case resolves a **real** layout tree over a **real** elevation model
   and runs the identical byte comparison, at frame indices chosen to visit every branch
   those panels' own absence policy would otherwise let a per-frame cache hide behind: the
   first frame, one inside the pre-data region (`elevation.go`), the midpoint, one past the
   elevation model's own axis end, and the last frame. Between the mock-panel case (generic
   frame-loop machinery, the highlight wash) and this one (the panels that actually rely on
   the guarantee), every panel in both shipped layouts is now covered.

### Cost

A 25-minute activity at 30 fps is 45,000 frames, every pixel generated. Redrawing invariant
chrome per frame and recomputing a fixed projection are both eliminated by construction:
chrome is `Static`, called once; the projection is a Painter field. One static base image
and one frame buffer are allocated and reused — a fresh 4K RGBA per frame would allocate
about 33 MB per frame.

**Measured** (`internal/render`'s benchmarks, on the reference machine, with all five
panels):

| | per frame |
|---|---|
| full landscape layout, 1080p | 5.5 ms |
| full landscape layout, 4K | 22.2 ms |
| route panel alone, 1080p | 0.29 ms |
| the static-base copy alone, 1080p / 4K | 0.15 ms / 0.95 ms |

**Text dominates, and it scales with size rather than with panel count.** Removing the
route panel makes a frame *slower* — 19.8 ms against 5.5 — because its column-mate then
grows and the clock is drawn two and a half times larger. Glyph rasterization scales with
area. Any comparison that changes the layout is mostly measuring the layout, and the route,
which looks like the expensive panel, is the cheapest thing on the frame.

### The frame-caching idea, and why it died

Measured after the route panel landed: **97% of consecutive frames were pixel-identical** —
9 of 299 changed. The data arrives at 1 Hz and the clock advances once a second, so at 30
fps about thirty frames in a row showed exactly the same thing. That suggested the largest
optimisation available by far was simply not to draw a frame whose content had not changed.

**Adding the elevation profile took it to 100%.** Measured per layout, over ten seconds at
30 fps:

| layout | consecutive frames that differ |
|---|---|
| text only | 3% |
| route only | 3% |
| elevation only | **100%** |
| all five panels | **100%** |

The playhead is placed from an *interpolated* distance, so it moves a fraction of a pixel
every frame — and it should. A playhead that jumped once a second would look broken beside
a clock that ticks. The route dot snaps to recorded fixes and so inherits the data's own
rate — 1 Hz for the files this targets; the playhead does not, deliberately.

(An earlier version of this paragraph said the dot inherits 1 Hz full stop. It did not:
the position was looked up in the *downsampled* outline, so on a four-hour ride it froze
for 29 seconds at a time. Every test used a fixture below the downsampling cap, where the
two lists are identical and the bug cannot appear. Found by review, not by a test.)

So smooth motion and frame redundancy are in direct tension, and a whole-frame cache is
worth nothing as soon as one panel animates continuously. Anything that recovers the win
would have to work per panel — dirty regions, or a per-Painter cache key — which is a much
larger change than "skip the frame", and it buys back at most the 3% of the frame the text
panels occupy while the elevation strip redraws regardless.

The conclusion is recorded rather than the first measurement, because the first measurement
was taken before the panel that invalidated it, and it would otherwise read as a standing
invitation to build the wrong thing. The measurement is also no longer asserted in a test:
`TestRender_FrameRedundancy` reports the figures per layout and pins none of them, since
which side of that tension a panel picks is a panel's decision and pinning the ratio would
make the next smoothly-animating panel look like a regression.

Parallel frame rendering is not built. The door is held open by one rule, that `Dynamic`
must not mutate the Painter, and nothing more.

### Marking a highlight: one base per colour, and one index rule

Two decisions inside the highlight marks are invisible in the output, cheap to "simplify"
and expensive to put back, so they are recorded here rather than left to be re-derived from
the code.

**The wash is a colour-keyed table of static bases, built eagerly.** Under
`--highlight-style wash` the frame is drawn over a different background while a highlight
plays, and a highlight's own `background=` names that colour outright. The mechanism is one
full static base per *distinct* wash colour, built by `Run` before its loop starts and
looked up per frame. What lives where is the whole of it: `washColors` — each highlight's
resolved colour — and `washBases` — colour → base — are `Renderer` fields, computed once per
render; the only per-frame inputs are `Frame.Interval`, which picks the base, and
`Frame.IntervalWeight`, which says how far to blend toward it. A highlight with no
`background=` is deliberately not a branch: it resolves to the theme's own derived tint and
goes into the same table under the same key, so two highlights naming the same colour share
one buffer and the loop never has to know which *highlight* is playing, only which colour.

The thriftier-looking alternative is two bases, with the alternate rebuilt each time the
render crosses into the next highlight. Highlights cannot overlap, so at most one is active
per frame and two buffers would suffice. It was rejected because the *compute* is identical
either way — one static render per distinct colour — so rebuilding buys only memory, and it
pays for that with mutable cache state inside the frame loop, written by `Run` and not by
`Render`. Two paths that differ in what they cache is precisely the shape of bug the
static/dynamic equality test exists to catch, and here it would catch this one only if the
fixture happened to render two differently-coloured highlights — the same "every fixture was
below the cap" blindness that hid the route-dot bug in the parenthetical above. It would
also close the parallel-frames door that "`Dynamic` must not mutate the Painter" holds
open, for a feature with no performance case. The memory is stated plainly instead:
*k* distinct colours cost *k* full-frame buffers, 8.3 MB each at 1080p and 33.2 MB at 4K, so
three colours at 4K is about 100 MB on a program that renders thirty-second videos. If that
ever stops being acceptable, `washBaseFor` is the seam a lazy implementation drops into
without touching a caller.

`Render`, the simple path, recomputes its own base per call and never reads the table. That
looks like duplicated work waiting to be removed, and removing it would be a mistake: it is
what keeps the equality test a test *of* the table rather than a comparison of the table
with itself.

**The background mask is wrong, and attractively so.** It is named here because it will
otherwise be rediscovered: one 8-bit buffer marking every pixel equal to `Theme.Background`,
recoloured per frame, with no second base at all. Antialiased glyph edges are
`a·ink + (1−a)·bg` — they are *not* equal to the background, so a mask misses them and every
reading and label comes out ringed in an un-washed halo. Blending two *full* bases is exact
at those same edges for a reason the mask cannot inherit:
`blend(a·ink + (1−a)·bg₁, a·ink + (1−a)·bg₂, w) = a·ink + (1−a)·blend(bg₁, bg₂, w)`, so the
ink term passes through the interpolation untouched.

**A highlight's route mark is resolved against the drawn, thinned outline.**
`route.SpanIndices` searches the same downsampled list the outline is drawn from, never the
full fix list — the *opposite* of the rule for the position dot in the parenthetical above,
and the two must not be reconciled into one. The dot answers "where is it right now", which
the thinned list quantises into 29-second freezes; the mark answers "which part of the drawn
line is this", and a span resolved against every fix would name vertices the outline never
draws, leaving a coloured line floating beside the dim one instead of lying on it. Each is
right to use the list the other must not, and a reader who has internalised the dot bug is
the one most likely to "fix" this into it.

The genuine hazard is not which list but what the thinning does to a short highlight. At the
cap of 500 drawn points a four-hour ride puts 29 seconds between consecutive vertices, so
any highlight shorter than that stride contains no drawn vertex at all, and the obvious
implementation strokes a one-vertex polyline, which draws nothing — a highlight the user
asked for, silently unmarked. `SpanIndices` returns the single chord straddling the span
instead: the mark placed at the resolution the outline was actually drawn at, the same
reasoning the marker strip already applies to a block that would otherwise round away to
nothing. A span lying entirely before the first fix or after the last is a different case
and returns `ok = false`; widening there would draw a mark claiming the activity passed
through a place, and this program does not invent claims about position. The panel draws
nothing for such a highlight and `cmd` reports it by name in the summary, through the same
`SpanIndices` call over the same thinned list, so the two cannot come to disagree.

None of it is testable below the cap, where the drawn list and the fix list are identical
and every stride is a single sample. The panel tests therefore use a 3,000-point fixture,
six times the cap, whose drawn vertices land about six seconds apart, and each states in its
own comment that a fixture under the cap could not have failed it — the same sentence the
dot bug's own regression test already carries.

Finally, the mark's geometry is computed in `Prepare` and stroked in `Dynamic`, which reads
like static content stranded in the wrong pass. It is not. `Dynamic` draws the covered
prefix over the outline and wider than it, so a mark drawn into the static base is erased
the moment the playhead passes over it. The coordinates are per-render Painter fields; only
the stroke is per-frame, and `Dynamic` still mutates nothing.

## Timeline: elapsed

```go
func (t Timeline) At(i int) time.Time    // Start + i/FPS
```

The dashboard **freezes through a pause**. Three reasons:

1. **It is the only affine map from frame index to instant.** Active time requires mapping
   frame → active-duration → instant through the pause list, which is a search, and it makes
   `Frame.At` non-affine in `Frame.Index` — so any panel deriving anything from the index
   breaks, and seeking or parallelising becomes a lookup problem rather than arithmetic.
   (This stopped being literally true once named highlight intervals landed — see
   "Timeline: piecewise" below for what was given up, and why elapsed time was still the
   right side of this argument to be on.)
2. **It never fabricates motion.** Cutting a pause splices two instants together. Someone
   who stopped their watch at a trailhead and restarted a kilometre later gets a route dot
   that teleports — a discontinuity that looks exactly like the GPS glitch this project
   spends its care avoiding.
3. **A frozen dashboard is honest; a spliced one is not.** With both clocks on screen, a
   stopped activity reads as stopped. A video where four minutes silently vanish does not
   say so anywhere.

Active time becomes a second `Timeline` implementation and **nothing else changes** — not a
panel, not the loop, not the encoder. That is the point of putting the decision behind one
type: it is reversible at exactly one seam. It was not in v1 because its visual design was
unsettled (does a cut pause get a card? a fade? nothing?) and shipping the flag before
answering that would have baked in the wrong answer. It has since shipped, as the opt-in
`--pauses skip`, with that question answered — see "Timeline: cut" below, which is where
the three objections above are met one at a time rather than waved away.

`--fps` is a `float64` so 29.97 is expressible, and the same number feeds `Timeline.At` and
ffmpeg's `-r`, so the container's timestamps and the dashboard's own clock derive from one
value.

### Both clocks, always

The loop computes elapsed and active for every frame and puts both on `Frame`, once, rather
than N times in N panels. `cmd/inspect.go` already sets this precedent deliberately: it
prints both even when they are equal, because a dashboard has to choose which one its
timeline runs on and a reader should see the pair and the choice.

One honesty requirement carried over from `inspect`: when the file has no timer events,
`Active == Elapsed` is not a measurement, it is the absence of one. The time panel shows
active as `--` in that case rather than a number equal to elapsed. That is an absent-data
policy in the same sense as any other.

### Inside a gap

`AtWithGap` with `fitactivity.DefaultMaxGap`. Within tolerance the sample is interpolated
per field; near an edge it snaps to the nearest real sample; deep in the gap it is absent and
every panel's placeholder branch fires.

Two rules for panels:

- **Never smooth or hold a value across a gap.** The gap is the answer.
- **Discrete quantities come from a model, never from rounding an interpolated one.** The
  current kilometre comes from `Splits.CurrentKm`, which is a step function. Interpolating a
  cumulative quantity is a different act from interpolating an instantaneous one, and
  lerping a *lap number* is a third thing that is simply meaningless.

### Timeline: cut

`--pauses skip` (default `freeze`) removes the activity's paused stretches from the
render, so video time advances only while the timer was running. It is the thing "Timeline:
elapsed" above refused outright, and it is shipped **opt-in, off by default, and announced
on screen** — the freeze behaviour is untouched and a render that does not pass the flag is
byte-identical to one from before it existed.

Three objections were raised above. Two are still true and one stopped being expensive:

| Objection | Status under `--pauses skip` |
|---|---|
| **1. It is the only affine map.** Active time requires mapping frame → active-duration → instant through the pause list, which is a search. | **No longer a cost.** Named highlights already made `Timeline` piecewise, so a pause-skipping timeline is the *same* segment list with the paused stretches left out of it — built by the same builder, searched by the same `segmentFor`. The list grows by the recording's handful of pauses, not by its length. This is not a second implementation of the type; it is one more thing the existing partition can express. |
| **2. It never fabricates motion.** A cut splices two instants; the route dot teleports. | **Still true, and paid for rather than argued away.** The dot does jump. That is what the user asked for, and it is why this is a flag rather than a default. |
| **3. A frozen dashboard is honest; a spliced one is not** — "a video where four minutes silently vanish does not say so anywhere". | **Answered.** Nothing vanishes silently: every seam is recorded as a `Cut`, and the render draws a `PAUSED 0:04:32` card at it for about a second and a half of video, fading in and out on the same ramp a highlight border and a label name use. The summary reports the total besides. The word *silently* was doing all the work in the original objection, and it is what this removes. |

**Where the cut is made.** In the segment builder, *after* the highlights have partitioned
the window, never before. A highlight is a range the user typed in the activity's own
elapsed time; clipping it against the pauses first would leave the rest of the construction
working from bounds that no longer say which stretch was named, and a highlight straddling a
pause would come out as two highlights rather than one interrupted one. Subtracting second
means both halves keep the highlight's index and its rate.

**What is not a seam.** A pause at the very start or the very end of a recording is removed
too, and gets no `Cut` and no card — there is no frame on the other side of it, so nothing
was spliced to anything and announcing a jump would name one that never happens. The render
simply begins later or ends earlier. This is why `Timeline` carries an `origin` distinct
from `Start()`: `IndexAt` must keep measuring the offsets a user types from the *activity's*
start, not from whatever instant frame 0 ended up showing.

**The one combination with no coherent answer** is a `--highlight` lying wholly inside a
pause. Under `freeze` it is rendered and reported (the dashboard is frozen through it, which
is odd but is what the recording says). Under `skip` every instant it names is gone, so it
is refused where the user typed it — `Timeline`'s floor-at-one-frame rule, which keeps a
named highlight from silently occupying no video, cannot save it, because there is no
segment left to floor.

**The card is a render-wide overlay, not a panel**, for the reason the highlight border is
one: it belongs to no box in the layout tree. Unlike the border it does *not* confine itself
to the margin — a duration is text, and the margin is a few pixels — so it is drawn over
whatever occupies the top of the frame. That overlap is deliberate. A reserved box would sit
empty for the whole render to be used for a second and a half; a mark on the marker strip
would be invisible in the common case, since the strip is absorbed into the elevation
profile whenever highlights or labels exist. A notice a viewer can miss is not a notice.

**Which clock the render runs on** is now the user's to say too: `--clock elapsed|active`
chooses which of `ElapsedPanel`'s two rows is the large one. Both are always drawn, so this
changes the order and nothing else — and the absent-data policy travels with the value
rather than with the row, so a file carrying no timer events shows the placeholder *large*
under `--clock active` instead of quietly demoting it. The flag is read in `Prepare`, not in
a keep filter like `--bottom-band` and `--gauges`: those decide whether a panel is placed,
which cannot belong to a panel that may not be in the tree, while this decides what an
unconditionally-placed panel draws inside its own box.

### Timeline: piecewise

Named highlight intervals (`--highlight`) gave up the one property the section above spent
three reasons defending: `Timeline.At` is no longer a single affine map. It is now piecewise
affine — a short list of segments, each running at its own rate, most Timelines carrying
exactly one segment spanning the whole activity. Read this section before "fixing" `At` back
to a single multiply-and-add; the discrepancy with the argument above is deliberate and
recorded here rather than resolved by changing the code back.

**Why this is not the rejected pause-cutting case**, even though both are described as
"cutting into the affine map": the table is objection-by-objection.

| Objection to cutting pauses (above) | Applies to per-highlight pacing? |
|---|---|
| **2. It never fabricates motion.** Cutting a pause splices two instants; the route dot teleports. | **No.** Every segment — highlighted or not — has its own strictly positive rate. The map stays strictly increasing end to end and every recorded instant still shows; some just show for longer or shorter than others. Nothing is skipped, nothing is spliced, the route dot never jumps. |
| **3. A frozen dashboard is honest; a spliced one is not.** | **No.** Nothing vanishes. A highlight makes some seconds of activity fill more seconds of video, or fewer — the opposite of a pause silently disappearing — and it announces itself on screen (the highlight strip, the margin border) rather than doing it invisibly. |
| **1. It is the only affine map.** `IndexAt` becomes a search, and a panel deriving anything from the index breaks. | **Partly — this is the one genuinely given up.** But the search is over a render-wide list of at most a couple of thousand segments (one per `--highlight`, plus the ordinary stretches around them), not over the activity's own timer events, so it stays a handful of comparisons rather than growing with the recording. And no panel reads `Frame.Index` today — that population this argument was protecting is empty, so the part of the cost that would have mattered is not being paid by anyone. |

So cutting pauses breaks the *data* (a splice, a teleport, four minutes that do not admit
they are gone); per-segment pacing breaks only the *arithmetic convenience* of one rate for
the whole render. The first was worth refusing as a default. The second was worth paying
for — and paying for it is what later made the first affordable as an opt-in, since the
segment list this section introduced is exactly what a cut is expressed in. See "Timeline:
cut" above: the splice and the teleport are still real and are the reason `--pauses skip`
is a flag, but "four minutes that do not admit they are gone" was answerable, and was
answered, on screen.

**Where the segments live.** A `[]segment` field private to `Timeline`, built once at
construction by the one function every constructor funnels through — a highlight-free render
is the zero-segment case of the same builder, not a different code path beside it, so
`NewTimeline` and its tests keep working unchanged. The segment list is never handed to a
panel and never will be: it is what would have to be duplicated if a future feature reached
for its own copy of "which rate applies at frame *i*", which is exactly the failure mode
keeping it private prevents.

**The construction trap worth remembering** (found while building this, not obvious in
advance): a segment must be anchored at its own *true* boundary in activity time — the exact
offset a `--highlight from=`/`to=` named — never at wherever the previous segment's *rounded*
frame count happened to land. Chaining segment origins that way accumulates rounding error
across the render, so a highlight late in a long video would start measurably later than the
instant it was asked for, worse the more segments came before it. Anchoring each segment
independently instead means the error cannot accumulate — it can only ever appear once, at
the one seam between that segment and its neighbour.

That seam is the cost, and it is worth being precise about rather than hand-waving "some
rounding": the last frame of one segment and the first frame of the next are still frame
`i` and `i+1` of the render — consecutive — but because each segment rounds its own frame
count independently, the video-time gap between those two frames is not exactly one frame
the way it is everywhere else, only somewhere between half a frame and one and a half. The
map stays *strictly increasing* across the seam (each segment's last frame provably lands
before that segment's own end, and the next segment's first frame sits exactly at it), so
nothing goes backwards or repeats — the irregularity is bounded, does not accumulate, and is
smaller than what a viewer can perceive at any frame rate this project targets. It is pinned
by a test that walks every seam of a render rather than asserted in prose, because it is not
something a future reader would otherwise re-derive by inspection.

## Encoding

```go
type Sink interface {
	WriteFrame(img *image.RGBA) error
	Close() error
}
```

**`internal/encode` knows nothing about panels, activities, layouts or time.** It takes
images and a frame rate. That boundary buys three things: `--frames` is a sink swap and
nothing more, so the fast visual loop runs the identical render path as the real one; every
render test runs without ffmpeg on `PATH`, against a sink that keeps frames in memory; and
the static/dynamic equality test operates on images rather than on a video.

The invocation is an argv slice, never a shell, with the output path separated from the
options so a name beginning with `-` is not parsed as a flag. `Close` closes stdin then
waits, guarded so it happens exactly once, and wraps any error with the captured stderr —
every error path out of the loop must close the encoder or ffmpeg is left blocked on an open
pipe holding the output file.

Departures from videofx, all because there is no input video: rawvideo on stdin is the only
input, there is no audio to map, there is no container metadata to carry, and the codec is
`libx264`/`yuv420p` rather than the Darwin-only `hevc_videotoolbox` — fitdash is a
cross-platform CLI and universal playability matters more than encode speed.

**fitdash writes no metadata into the output container** — no location, no creation time, no
source path. This is a deliberate non-feature. videofx carries those forward because its
output is a re-encode of the user's own clip and losing them would be data loss. fitdash's
output is a *new* file, and stamping the activity's coordinates into it would silently attach
precise location data to something the user is likely about to share. If it is ever wanted,
it ships as an explicit opt-in flag.

Odd output dimensions are rejected with an error naming the constraint rather than rounded —
`yuv420p` requires even, and a render that silently came out 1919 px wide is a surprise.

## Build order

Each step is independently testable. **Step 7 is where it becomes visible in a render**, and
where the render lane gates.

1. `internal/encode` — `Sink`, the argument builder split out for testing, the ffmpeg sink,
   the PNG sink. *First step that produces a video file at all.*
2. `Timeline`, built from a track and its timer model. Pure table test, no pixels.
3. `Box`, `Slot`, `Layout`, `Resolve` — including the closes-up case at three frame sizes.
4. `Canvas`, `Theme`, the face cache. **Adds the drawing libraries, and the `NOTICE`
   obligation below.**
5. `Panel`, `Painter`, `Context`, `Frame`, `Renderer`, the frame loop. **The equality test
   lands here.**
6. The elapsed-time panel.
7. CLI wiring. **← visible in a render.** This is the first vertical slice.
8. A generic metric readout, as heart rate (placeholder) and power (decline). **The test
   that proves closing-up works end to end.**
9. Progress reporting, and a summary naming which panels drew and which declined.
10. `internal/route` — projection, route panel. *videofx's memo cache is not written.*
11. Elevation profile. *The panel that would have hit videofx's axis-origin trap.*

Steps 1–3 need no drawing library, so the layout maths and the timeline land before any
dependency does. Steps 1–6 need no ffmpeg.

### The first vertical slice

> `fitdash ACTIVITY.fit --output-dir DIR` renders a 1920×1080, 30 fps, opaque-background
> H.264 video spanning exactly the activity's elapsed time, containing one panel: an
> elapsed-time readout — the static word `ELAPSED` and a rule above a monospace `H:MM:SS`
> that counts up — filling a single-leaf layout inside its margin.

It exercises every seam exactly once and no seam twice, and it has a checkable answer that
is not "it looks right": the output's duration must equal the activity's elapsed time, and
frame *k* must read *k*/fps. Both are assertable without pixels and cross-checkable against
`fitdash inspect` on the same file.

The static layer is under test from frame one, because `ELAPSED` and its rule are real
static content — a slice whose panel had no chrome would defer the project's central trap
past its first proof.

## Open items

Both items below are **closed**; they are kept here because the reasoning is worth
having when the code they concern is read.

- **`NOTICE` carries a FreeType section.** The drawing stack pulls in
  `github.com/golang/freetype` — not by an import here, but through `github.com/fogleman/gg`,
  which uses its rasterizer and TrueType packages. It is dual-licensed FTL or GPLv2; this
  project elects the FTL, whose advertising clause makes the credit mandatory rather than
  courteous, and this is a public Apache-2.0 repository. Added ahead of the dependency
  because `NOTICE` already listed `gg` and `golang.org/x/image` in anticipation, so
  FreeType's absence was a gap in that list rather than a premature entry.
- **`TimerModel.Paused(at) bool` exists upstream in fitactivity.** `Frame.Paused` reads it.
  **Do not derive it locally** — the available hack is testing whether the moving clock's
  derivative is zero, `Active(at+1s) == Active(at)`. It is not merely drift-prone, it is
  wrong twice: it reports "paused" for every instant outside the activity's window, where
  `Active` clamps and so cannot advance, and it ends a pause a second early for any
  sub-second offset, which at 30 fps is very nearly every frame. Both failures are pinned
  by tests in that repository.

## Not designed here

Map imagery, 3D and TCX/GPX input are future work. What the design must not foreclose:

- **Map imagery.** The route Painter computes its projection in `Prepare` and `Static` draws
  the outline over whatever is beneath. The whole hook is one interface consulted **once, in
  `Prepare`**, defaulting to nil. Two constraints follow now and cost nothing now: the
  projection must be fixed for the render, so a tile fetch cannot re-fit the box mid-render
  and tiles are never fetched per frame; and a tile failure must degrade to no basemap
  *inside* the route panel's own `Prepare`, which is what makes "offline must keep working"
  structural rather than aspirational. Everything else — opt-in, off by default, visible when
  it happens, credentials never reaching a commit or a log or a frame, and the attribution
  obligations that attach to the rendered video — is already in `CLAUDE.md` and `NOTICE` and
  is not re-decided here.
- **3D** is a different projection and almost certainly a different panel. A panel already
  owns its box and its projection, so there is nothing to do now.
- **GPX/TCX input** is a fitactivity change, not a fitdash one. The requirement this design
  imposes: nothing outside the single `Decode` call may assume the input was FIT. No panel
  reads the source path or its extension.
