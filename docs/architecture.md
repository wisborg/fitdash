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

A 25-minute activity at 30 fps is 45,000 frames, every pixel generated. The two things that
dominate are redrawing invariant chrome per frame and recomputing a fixed projection, and
both are eliminated by construction: chrome is `Static`, called once; the projection is a
Painter field. One static base image and one frame buffer are allocated and reused — a fresh
4K RGBA per frame would allocate about 33 MB per frame.

Parallel frame rendering is not built. The door is held open by one rule, that `Dynamic`
must not mutate the Painter, and nothing more.

## Timeline: elapsed

```go
func (t Timeline) At(i int) time.Time    // Start + i/FPS
```

The dashboard **freezes through a pause**. Three reasons:

1. **It is the only affine map from frame index to instant.** Active time requires mapping
   frame → active-duration → instant through the pause list, which is a search, and it makes
   `Frame.At` non-affine in `Frame.Index` — so any panel deriving anything from the index
   breaks, and seeking or parallelising becomes a lookup problem rather than arithmetic.
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
