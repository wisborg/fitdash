# fitdash

Render a recorded exercise as a dashboard video: read a Garmin FIT activity, and
produce a video in which the metrics animate as the activity progresses — the route
drawing itself, an elevation profile with a moving playhead, pace, heart rate, power,
cumulative climb and the gradient underfoot.

```
fitdash activity.fit --video-duration 3m
```

Ten panels ship today: the route, an elapsed/active clock, distance, heart rate, pace,
power, cadence, an elevation profile, cumulative gain and loss, and the current gradient
— plus an eleventh, the marker strip, which appears only when `--highlight` or `--label`
gives it something to mark. The distance readout is
conditional in its own way: the area under the elevation profile fills as the activity
progresses and is itself the distance indicator, so the readout is drawn only on an
activity that has no profile to fill — a rowing machine, or a course flat enough that
there is no trace to draw — and it takes the band the profile would have had. Panels that
have no data in a given activity decline, and the layout closes up around them: an indoor
ride simply has no route panel, and the summary says so by name rather than leaving an
unexplained gap.

`--layout` picks the arrangement (`auto`, which follows the frame's shape, or `landscape`
or `portrait` forced), `--theme` the palette (`dark` or `light`), and `--gauge-style`
whether the four fluctuating readouts draw their reading against a scale (`plain`, `track`
or `dial` — see below). `--gauges` chooses what that block shows at all: `metrics`, the
four readouts, or `balance`, the left/right balance bars with pace alongside.

**Both clocks are always on screen, and `--clock` chooses the order.** The default,
`elapsed`, draws wall-clock time since the activity began as the large readout with
moving time beneath it; `--clock active` swaps them. A file carrying no timer events
cannot measure active time at all and shows a placeholder for it wherever it is drawn
— including as the large readout — rather than a figure equal to elapsed.

**`--pauses` decides what happens where you stopped.** By default the video spans the
activity's *elapsed* time and freezes through a pause: the elapsed clock keeps counting,
the active clock does not, and a stopped activity reads as stopped. `--pauses skip` cuts
the paused stretches out, so video time advances only while the timer was running. That
is a splice — the route dot jumps across whatever ground was covered while the watch was
stopped — so every seam draws a `PAUSED 0:04:32` notice naming what went missing there,
and the summary reports the total. It pairs naturally with `--clock active`, which it
does not turn on for you.

```
fitdash activity.fit --video-duration 3m --pauses skip --clock active
```

**Several files render as one video, for a workout recorded in pieces.** A race started
as its own activity partway through a long run leaves three recordings of one afternoon;
pass them all and fitdash renders the afternoon.

```
fitdash warmup.fit race.fit cooldown.fit --video-duration 3m
```

They are ordered by the start time inside each file, never by the order you typed them,
so `fitdash *.fit` works and the video is named after the file the activity *starts* in.
The stretch between two recordings becomes a pause like any the watch was stopped for —
frozen through by default, cut by `--pauses skip` — and no distance is added across it,
because the ground covered while nothing was recording was never measured. The summary
names every file that went in, **where each one starts in the merged activity**, and the
total unrecorded gap. `fitdash inspect` takes the same list and reports the merged activity.

```
merged 5 files into one activity, ordered by their own start times; each
offset is into the activity's elapsed time, ready for --label at= or --highlight from=:
  0s        morning-01.fit
  19m43s    morning-02.fit
  52m30s    morning-03.fit
  1h17m31s  morning-04.fit
  2h0m52s   morning-05.fit
```

The offsets are Go durations rather than the `h:mm:ss` clock the rest of the summary uses,
because that is what `--label at=` and `--highlight from=` parse — so a leg you want to
mark up can be copied straight into a flag.

Two files covering the same stretch of time — the same file twice, or two watches
recording one run — are refused rather than merged, naming both. Concatenating them would
count that distance twice, and a run that reports 24km instead of 12km looks exactly as
convincing in a rendered frame as a correct one.

**`--dry-run` resolves the whole render and prints the summary, writing nothing.** The
activity is decoded and merged, the timeline is built, and every panel is prepared — so the
layout, the panels that declined, the gauge ranges, the highlights and the labels are all
reported exactly as a real render would report them. Only the frame loop and the encoder
are skipped.

```
fitdash morning-0*.fit --dry-run --label 'at=1h17m31s,name=Last climb'
```

That makes it the quick way to check a `--highlight` or a `--label` lands where you meant:
every refusal — an instant past the end of the activity, two labels at the same moment, a
marker with no distance to sit at on the profile — happens while resolving, so you see it
in a second instead of after an encode. It creates no video, no frames and no output
directory, reports the destination it *would* write rather than piping it to stdout, and
under `--frames` lists the frames it would produce.

The one deliberate exception is `--basemap`: a dry run does fetch imagery, so it reaches
the network and fills the image cache. That is the point — a bad key or an unreachable
service is worth learning in a second rather than after an encode — and the summary
reports it either way.

**A highlight can zoom the route map onto its own stretch of course** with `zoom=true`, off
by default. On a workout merged from several files the whole-course view is useless for
exactly the part worth watching — a 5km race inside a 23km morning is a squiggle a few dozen
pixels across — so a highlight can reframe the map while it plays and ease back out
afterwards.

```
fitdash morning-0*.fit \
  --highlight 'from=19m43s,to=52m30s,name=Race 1,zoom=true' \
  --highlight 'from=1h17m31s,to=2h0m52s,name=Race 2,zoom=true'
```

It is per highlight rather than per render because the useful case is mixed: the race legs
are worth seeing in detail, while a long transit leg's whole point is the distance covered.
The map pans and scales over `--highlight-transition`, the same ramp the highlight's own fade
rides, so the two arrive together. A highlight whose span carries no GPS keeps the
whole-course view, and the summary says so rather than leaving you to wonder.

**The route can be drawn on a real map**, with `--basemap` and your own API key.
Off by default, and it needs a key because fitdash ships none and has no account
of its own:

```
fitdash morning-0*.fit \
  --basemap outdoors \
  --basemap-key-file ~/.config/thunderforest.key
```

`outdoors` and `landscape` draw contours, paths and trail furniture — what a run
or a ride actually happened on; a general-purpose city map renders a forest trail
as blank green. `--basemap-dim` controls how far the imagery is washed back
(default `0.65`): a map at full strength competes with the route line and the
readouts beside it, and the map is context while the activity is the subject.

The key file may be the bare key, or a JSON or YAML mapping of provider name to
key so one file can hold several. It is read once, never logged, never written
into the video, and redacted out of every error — the key travels to the service
as a query parameter, so it ends up inside URLs, and URLs end up inside error
messages.

Imagery is cached on disk, which is a second place — after the rendered video itself —
where roughly where you were persists. It lives under your user cache directory unless
`--basemap-cache` says otherwise, and `--basemap-cache off` keeps nothing.

**Turning this on sends the area of your activity to a third party.** That is
your call to make, but fitdash will not make it quietly: the summary names the
service every time imagery is fetched, and says so only when something actually
was — a run served from cache says that instead. Imagery is cached on disk, so
tuning a `--highlight` does not re-fetch the same pictures, and a second render
of an activity works offline. If a fetch fails for any reason the render
continues with the plain outline and the summary says what happened.

The required credit — `Maps © Thunderforest, Data © OpenStreetMap contributors`
— is drawn into every frame that shows imagery. A video carries no interface to
put attribution in and is distributed on its own, so it goes in the picture.

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

**The fluctuating readouts can be drawn against a scale.** Heart rate, pace, power and
cadence go up and down as an activity progresses, and a bare number says nothing about
whether 148 bpm is this ride's hard effort or its easy one. `--gauge-style track` draws a
labelled axis beneath each of those four with a marker at the current reading;
`--gauge-style dial` draws the identical scale beside the number instead, as a
semicircular arc with a needle. `plain`, the default, is the number alone, unchanged. The
range is the activity's own, snapped outward to round numbers, so a gauge might read
`100`–`190 bpm` or `4:00`–`7:00 min/km`. It is taken from a smoothed reading rather than
the raw samples, so the marker never pins at an end, and neither a stopped sample — whose
pace is undefined — nor a one-sample power spike can take the whole axis for a value the
activity visited once. Both ends are always
labelled: a scale derived from the activity is only honest if the screen says what it is.
A reading past either end draws an off-scale mark rather than being hidden or clamped, and
the printed number is never clipped — so a genuine `0 W` while coasting still reads `0 W`.
The scale carries a green-to-red ramp and the marker takes its colour from its own
position, which is emphasis rather than information: the position says the same thing, so
nothing is lost if the colours are hard to tell apart.

**An activity with a footpod can show left/right balance instead.** `--gauges balance`
replaces those four readouts with pace and a centre-anchored bar for each balance metric
the file carries — ground contact time, and a footpod's own impact, stiffness and
oscillation balance — filling outward from a tick at even. Pace stays because balance
varies with effort and the two are meant to be read together. Every bar uses the **same
fixed scale**, deliberately, so the four can be compared with each other and with the same
person's other activities; a derived scale would make the same fill mean a different
asymmetry in a different video. A reading past either end draws an off-scale mark and the
printed magnitude is still the true one. No foot is named yet: the magnitude is printed and
the bar's side carries the direction, because which side these fields report is not
something this project is willing to guess at. An activity carrying none of these metrics
falls back to the ordinary gauges and the summary says so — `fitdash inspect` lists the
developer fields a file actually carries, which is where to look when a device names them
differently.

**Compressed hard, the gauges become unreadable — so they are averaged.** At 480×, a
frame advances sixteen seconds of activity and shows one arbitrary sample out of them,
which makes power flick between 200 and 500 from frame to frame. `--smoothing` averages
the gauges over a window of activity time: `auto` (default) scales with the compression
and switches itself off below about three times real time, `off` shows what was recorded,
or give a duration such as `30s`. At high compression it cuts power's frame-to-frame
change by roughly a factor of six, while leaving the shape of the effort intact.

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
reading alongside the standard FIT power field, and the two can disagree substantially at
the same instant — enough to change what the gauge says. `--power-source`
takes `auto` (prefer the footpod, fall back to native), `stryd`, or `native`, using the
same vocabulary as [videofx][videofx]. A forced source the activity lacks shows a
placeholder rather than quietly substituting the other sensor's number.

**GPS and barometric elevation overcount climbing badly**, so the profile, the gain and
loss bars and the gradient are all read from a smoothed model rather than from the raw
trace. By default the smoothing is tuned so the computed totals match whatever the watch
itself recorded, and the render summary now names the value it settled on and where it
came from. `--elevation-gain` and `--elevation-loss` tune it against figures you trust
instead — an official course total is usually the most reliable target there is — and
`--elevation-smoothing` sets the width directly when you would rather say it outright.
The names and the reasoning are [videofx][videofx]'s.

## Requirements

- Go 1.25+
- `ffmpeg` on `PATH` (the only external program; it encodes the rendered frames)

```
scripts/fd check-deps
scripts/fd gates
```

## Shell completion

```
fitdash completion install
```

Writes the completion script into a directory your shell searches, creating it if
needed, and then says whether that directory is one your shell actually reads. If it
is not, it prints the line to add and where to add it — a completion script in an
unsearched directory does nothing and says nothing about it, which is the failure
worth reporting rather than reproducing.

It picks the destination for you (`--shell zsh|bash|all`, `--dir` to override,
`--dry-run` to see the choice first) and never edits your startup files; it only
quotes the line you would add. `fitdash completion zsh` still prints the script to
stdout if you would rather place it yourself.

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
