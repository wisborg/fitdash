# fitdash

Render a recorded exercise as a dashboard video: read a Garmin FIT activity, and
produce a video in which the metrics animate as the activity progresses — the route
drawing itself, an elevation profile with a moving playhead, pace, heart rate, power,
splits.

```
fitdash activity.fit --video-duration 3m
```

Eight panels ship today: the route, an elapsed/active clock, distance, heart rate, pace,
power, cadence, and an elevation profile. Panels that have no data in a given activity
decline, and the layout closes up around them — an indoor ride simply has no route panel,
and the summary says so by name rather than leaving an unexplained gap.

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

**Cadence means different things to different sports.** FIT records it as revolutions per
minute — crank revolutions on a bike, which is what a cyclist reads, but revolutions *per
leg* on a run, so the figure a runner recognises is twice it. A run shows `spm`, a ride
shows `rpm`, and a sport fitdash does not recognise keeps the recorded number under its
recorded unit rather than being guessed at.

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
