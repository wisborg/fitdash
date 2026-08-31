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

### What panel number eight costs

One new file in `internal/panel/`, and one line in each layout that wants it. Nothing in
`internal/render` changes: the loop iterates the resolved placements and calls two methods,
and it does not know what a panel is beyond that. The neighbours shrink automatically
because the weight denominator grew.

That gives the panel contract a reviewable tripwire with a yes/no answer: **did the diff
touch `internal/render`?** If adding a panel did, the contract has sprung a leak.

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

### One band, two candidates: the elevation profile or the distance readout

One full-width band runs across the bottom of both trees, in the row directly above the
marker strip, and it shows **either** the elevation profile **or** the live distance
readout — never both, and never neither while the activity carries the data for one of
them. The profile used to have that band to itself and the readout used to sit beside the
clock. Both moved for the same reason: **the filled area under the profile's trace is now
this project's distance indicator**, so a separate readout drawn anywhere on the frame is
the same quantity said twice.

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
accept test cannot see it. So a future `--no-elevation`, which is meant to be one line in
a `keep` filter rejecting the profile by name, would remove the profile *and* leave that
variant declining on its own account, since the elevation panel still accepts the
activity. The band would prune and distance would vanish from the render entirely: the
feature the arrangement exists to make possible would be broken by the arrangement.
Under `Alt` the same flag composes correctly and stays one line — `keep` rejects the
profile, the slot falls through, and the readout takes the band.

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
comment so it is a known cost rather than a surprise.

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

### The marker strip carries no caption

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
	Track       *fitactivity.Track
	Report      inspect.Report
	Timer       *fitactivity.TimerModel
	Elevation   *fitactivity.ElevationModel
	Splits      *fitactivity.Splits
	Route       []GeoPoint
	Timeline    Timeline
	Width, Height int
	FontScale   float64
	PowerSource fitactivity.PowerSource
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
   the test fails. It is two small buffers and a byte comparison, and it is the only thing
   that catches this class of bug automatically.

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
type: it is reversible at exactly one seam. It is not in v1 because its visual design is
unsettled (does a cut pause get a card? a fade? nothing?) and shipping the flag before
answering that would bake in the wrong answer.

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
the whole render. The first was worth refusing outright. The second was worth paying for.

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
