# fitdash

Render a recorded exercise as a dashboard video: read a Garmin FIT activity, and
produce a video in which the metrics animate as the activity progresses — the route
drawing itself, an elevation profile with a moving playhead, pace, heart rate, power,
splits.

> **Status: scaffolding.** The repository layout, licensing and working conventions
> are in place; the renderer is not built yet. See `CLAUDE.md` for how the pieces are
> meant to fit together.

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

## Requirements

- Go 1.25+
- `ffmpeg` on `PATH` (the only external program; it encodes the rendered frames)

```
scripts/fd check-deps
scripts/fd gates
```

## Privacy

A rendered dashboard is a video of where you were, minute by minute, and what your
body was doing. That is the whole point of the program, and it is yours to publish or
not — but nothing here should make that choice quietly on your behalf. No sample
activity is committed to this repository; tests generate synthetic ones.

## License

Apache-2.0. See `LICENSE`, and `NOTICE` for third-party attribution.
