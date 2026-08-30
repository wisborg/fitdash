# fitdash

Render a recorded exercise as a dashboard video: read a Garmin FIT activity, and
produce a video in which the metrics animate as the activity progresses — the route
drawing itself, an elevation profile with a moving playhead, pace, heart rate, power,
splits.

```
fitdash activity.fit --video-duration 3m
```

Eight panels ship today: the route, an elapsed/active clock, distance, heart rate, pace,
power, cadence, and an elevation profile — plus a ninth, the marker strip, which appears
only when `--highlight` or `--label` gives it something to mark. Panels that have no data in
a given activity decline, and the layout closes up around them — an indoor ride simply has
no route panel, and the summary says so by name rather than leaving an unexplained gap.

`--layout` picks the arrangement (`auto`, which follows the frame's shape, or `landscape`
or `portrait` forced) and `--theme` the palette (`dark` or `light`).

## What it is

There is no input video. fitdash generates every pixel of every frame from the
activity alone, which is what separates it from [videofx][videofx] — that composites
a telemetry HUD onto footage you shot, this one needs no footage at all.

The two projects share their FIT decoding through
[`github.com/wisborg/fitactivity`][fitactivity].

[videofx]: https://github.com/wisborg/videofx
[fitactivity]: https://github.com/wisborg/fitactivity

## Design sketch

**Panels are pluggable.** A pool swim has no GPS, an indoor ride has no elevation, a
walk has no power — different activities carry different metrics, so the dashboard is
assembled from panels that can be added, removed and rearranged, each placed by
fractional offsets so a layout scales across output resolutions instead of pinning
pixels.

**Every panel decides what "no data" looks like.** It draws a placeholder, or it
declines to draw and lets the layout close up. What it must never do is silently
render a missing heart rate as `0 bpm`: FIT distinguishes absent from zero, and this
is the program that either preserves that distinction or throws it away in pixels
nobody can inspect.

**The route starts as an outline** — an equirectangular projection fitted to its box,
covered portion bright, current position marked. Map imagery (Mapbox, Google,
OpenStreetMap) and a 3D view come later, behind an interface and strictly opt-in:
requesting tiles tells a third party where you ran, and tile providers impose
attribution and terms-of-use obligations that attach to the rendered video, not just
to the source.

**A video need not run as long as the activity did.** `--speedup 60` turns an hour into a
minute; `--video-duration 3m` works out whatever factor that takes. The dashboard still
reads *activity* time either way — the video is compressed, the data is not relabelled —
so the clock advances that much faster, which is what a time-lapse should look like. What
it costs is that each frame samples the activity further on, so at high compression a
short spike can fall between frames; nothing is averaged over the gap, because averaging
would invent a reading nobody recorded.

**One stretch can be given more of the video than its share.** `--highlight
'from=10m,to=11m,video=10s,name=Hill climb'` marks a range of the activity and paces it on
its own terms, so a minute of climbing that the base compression would have flashed past in
about a second fills ten seconds of video instead. The flag is repeatable, and a highlight's
video time is added *on top of* `--video-duration` rather than taken out of it: a 30-second
base plus that climb is a 39-second video, and the summary prints the decomposition rather
than leaving the discrepancy to be noticed. Re-solving the base so the total held at 30
seconds was the alternative, and it was rejected — adding one highlight would then silently
change the pace of everything else in the render.

`--highlight-style` chooses how the *frame* is marked while a highlight plays: `border` (the
default) draws an accent border in the frame's margin, which is the one band the layout
guarantees is empty; `wash` tints the whole background instead; `none` re-paces without
marking the frame at all. Two marks are drawn whatever the style says, because they are
about *where* the highlights are rather than about the frame you are looking at: the marker
strip lights each highlight's block on a ribbon of the video's own timeline, and the route
draws that stretch of the outline in the same colour. Both are lit from the first frame and
brighten while the highlight plays, so you can see where a highlight lies before the render
reaches it. A highlight whose range contains no GPS fix at all cannot be placed on the
route, and the summary names it and says it went unmarked rather than leaving a mark
silently missing. `--highlight-transition` sets how long a mark takes to ramp in and out —
and a `--label`'s too — measured in video time rather than activity time, because how fast a
fade reads depends on the video's clock and nothing else.

**A highlight can name the colour it washes to.** `--highlight
'from=10m,to=11m,video=10s,background=#1B2A4A'` gives that one highlight its own background,
and the colour arrives exactly as typed: at full strength, not the 22% blend toward the
accent that the uncoloured default uses, because a colour you chose *as* a background should
not come back as a fraction of itself you can neither predict nor match against anything
else in your video. Hex only — `#RGB` or `#RRGGBB`, with the `#` required so that
`background=blue` is an error rather than something quietly parsed — and opaque, since there
is nothing behind the frame for an alpha channel to reveal. The key means something only
under `--highlight-style wash`, so under `border` or `none` it is refused with an error
naming that style, rather than being accepted and doing nothing. A colour that leaves the
dashboard hard to read is warned about and rendered anyway: the readings are checked against
WCAG 2's 4.5:1, and the deliberately recessive chrome and placeholder colours against a
lower floor of their own, since a moody near-black wash may be exactly what you asked for
and the choice is one flag to reverse.

**A single moment can be given a name.** `--label 'at=12m30s,name=Lighthouse,video=3s'`
puts a name on screen and changes nothing else — no re-pacing, no border, no wash. `at` is
an offset into the activity's elapsed time, the same clock as `--highlight from`; `video`
is how long the name stays up, and it is *video* time, defaulting to three seconds,
because at 60× a five-second stretch of activity is two frames and a label timed on the
activity's clock would be invisible at any ordinary compression. The name is required,
which a highlight's is not: a nameless highlight still lights a block and marks the
margin, while a nameless label is nothing at all. The flag is repeatable, each label puts
a tick on the marker strip, and two labels whose on-screen spans would overlap are not
refused the way two overlapping highlights are — the earlier name is truncated to end
where the next begins, and the summary says which. Overlapping highlights have no obvious
composition, but "show each name until the next arrives" is the plain reading of a
sequence of instants, and the collision is in video time between two moments you typed in
activity time, which is arithmetic nobody can do in their head. Two labels at the *same*
instant are still refused: that is a typo, not a range. Marking a label on the route
itself is the obvious next step and is not built.

**Cadence means different things to different sports.** FIT records it as revolutions per
minute — crank revolutions on a bike, which is what a cyclist reads, but revolutions *per
leg* on a run, so the figure a runner recognises is twice it. A run shows `spm`, a ride
shows `rpm`, and a sport fitdash does not recognise keeps the recorded number under its
recorded unit rather than being guessed at.

**Compressed hard, the gauges become unreadable — so they are averaged.** At 480×, a
frame advances sixteen seconds of activity and shows one arbitrary sample out of them,
which makes power flick between 200 and 500 from frame to frame. `--smoothing` averages
the gauges over a window of activity time: `auto` (default) scales with the compression
and switches itself off below about three times real time, `off` shows what was recorded,
or give a duration such as `30s`. Measured on a real run, it cuts power's frame-to-frame
change from 18.7 W to 3.1 W at 311×, while leaving the shape of the effort intact.

Averaging is arguably the *more* faithful choice at compression, not the less: the frame
already stands for a span of activity, so a number describing that span is closer to what
a viewer takes it to mean than one sample plucked from it. Position, distance and
elevation are never averaged — a moving average of latitude cuts corners and puts the
route dot off the path.

**Pace has no value when you stop.** A speed of zero is a real reading whose reciprocal
does not exist — standing still is not infinitely slow — so a stopped runner sees `--:--`
rather than a `0:00` that would claim the opposite, or the confident multi-hour figure a
near-zero speed divides out to.

**Some activities record power twice.** A footpod such as a Stryd registers its own
reading alongside the standard FIT power field, and the two disagree — on the recording
this was built against, by more than fifty watts at the same instant. `--power-source`
takes `auto` (prefer the footpod, fall back to native), `stryd`, or `native`, using the
same vocabulary as [videofx][videofx]. A forced source the activity lacks shows a
placeholder rather than quietly substituting the other sensor's number.

## Requirements

- Go 1.25+
- `ffmpeg` on `PATH` (the only external program; it encodes the rendered frames)

```
scripts/fd check-deps
scripts/fd gates
```

## Looking at a render without waiting for one

`--frames` writes selected frames as PNGs instead of encoding a video, through the
identical render path — only the output sink differs, so what you see is what the video
would contain. `--frame-at 12m30s` picks a moment by its offset into the *activity*.

```
fitdash activity.fit --frames --frame-at 12m30s
fitdash inspect activity.fit    # what metrics does this file actually carry?
```

## Privacy

A rendered dashboard is a video of where you were, minute by minute, and what your
body was doing. That is the whole point of the program, and it is yours to publish or
not — but nothing here should make that choice quietly on your behalf. No sample
activity is committed to this repository; tests generate synthetic ones.

## License

Apache-2.0. See `LICENSE`, and `NOTICE` for third-party attribution.
