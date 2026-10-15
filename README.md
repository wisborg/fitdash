# fitdash

Render a recorded exercise as a dashboard video: read a recorded activity, and
produce a video in which the metrics animate as the activity progresses — the route
drawing itself, an elevation profile with a moving playhead, pace, heart rate, power,
cumulative climb and the gradient underfoot.

The activity can be a FIT, GPX, TCX, KML or KMZ file, or a flight exported from
Flightradar24, with its altitude, ground speed and track. Of Flightradar24's two exports,
give the KML: it holds every report the CSV does, and names the flight and its airports,
which the CSV does not. FIT carries the most; the
others carry what their format can, and a panel whose data the file lacks is left
out rather than drawn as zero. A file that records no distance — a GPX, a KML, a
flight — has one summed from its GPS fixes, as a device would, so its elevation
profile, climb and gradient are drawn, and the summary says the distance was summed.
A recording sparser than a watch's — a flight tracker reports every half-minute —
is read at its own rhythm, so its readouts do not go blank between reports. A flight
records no timer, so its time panel shows the time of day in UTC beneath elapsed, in
place of active time. A planned route with no times is refused: there is nothing to
play it by.

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

`--basemap local` draws the map here instead, from OpenStreetMap data in an
[osmbase](https://github.com/wisborg/osmbase) store on your own disk, in colours
derived from the theme so the route always reads against it. What the store lacks
for the activity is offered before it is fetched; afterwards nothing is sent to
anybody. Add `--terrain` to shade the shape of the ground and draw contour lines
(`--contours=false` for the shading alone), from elevation by
[Mapterhorn](https://mapterhorn.com) kept beside the store and offered the same way
— a second host, asked about separately. `fitdash basemap fetch ACTIVITY --terrain`
fetches both ahead of time instead, with one question naming both hosts:

```
fitdash morning.fit --basemap local --terrain
```

Text — the map's names, and a `--label`'s or `--highlight`'s name — is written in the
dashboard's own Go Mono wherever it has the letters, which is Latin, Greek and Cyrillic, so
the readouts look as they always have. For the rest — Thai, Chinese, Devanagari and the
like — each letter comes from the first font that has it: any given with `--font`
(repeatable), then the wide-coverage fonts the operating system ships, the same list course
uses. Nothing is bundled. Letters no font has are drawn as boxes, and the summary names
them. `--lang en` writes the map's names in English wherever the map has an English name,
and in their own script where it does not.

The frame then credits the elevation briefly, and the summary prints its full
notice — for Copernicus GLO-30 a sentence its licence dictates — which **whoever
publishes the video must give with it**, in the description. See [NOTICE](NOTICE).

The route panel marks the start with a green pin and a "play" triangle and the finish with a
red pin with a checkered head — one pin, half of each, where the route ends where it began —
the same pins course draws on its maps; `--route-ends none` leaves them out. The current
position is a dot, or with `--route-position` an `arrow` or a `plane` pointing the way the
route runs on screen at that moment of the video, so it stays steady where the video races
and turns with a taxiway where it slows; standing still, it keeps the heading the file
records, where it has one. `auto`, the default, is a plane for a flight and a dot for
anything else.

`--route-view 3d` draws the route panel in perspective instead: the map draped over the
shape of the ground and seen from a camera in the sky, the route lying on it and filling in
as the activity goes. A stretch behind a hill, and the dot while it is there, are drawn
faintly rather than left out. It needs `--basemap local` and turns `--terrain` on; the
camera sees far beyond the route, so the map and elevation offered are for all the ground
in view, not just the route's. `--route-heading` says which way it looks (`auto`, the
default, takes the bearing that shows the route largest), and `--route-pitch` and
`--route-exaggeration` how steeply and how tall the hills are drawn.

```
fitdash morning.fit --basemap local --route-view 3d
```

`--route-view flyover` flies a camera after the activity instead: it comes down from an
overview of the whole route over the first few seconds, follows from behind and above,
turning with the route's direction over a few seconds of video rather than with every
switchback, and goes back up to the overview at the end. Names stand on the picture —
places upright, streets and contour heights along their lines, read left to right
whichever way the camera faces — and fade in and out as the ground nears. `--route-distance`
says how far behind it follows; by default it is chosen in each frame from how fast the
ground moves in the video there, so it passes at about the same pace whatever the speed-up:
about 800 m for a run squeezed into a minute, a few hundred kilometres for a flight at
cruise and close by on the runway — a GPX, TCX or KML of one flies like any activity — and
closer over a stretch a `--highlight` slows with `speedup=` or `video=`. It changes no
faster than doubling in two seconds; `steady` keeps one distance for the whole video. The
camera turns with the direction of travel where the route has one and holds its course round
laps and turnarounds, swinging round a turnaround over a few seconds. `--route-camera-smoothing` says how much
video its height is smoothed over as the ground rises and falls under it: `0` follows the
ground exactly, the default `1.5s` glides over the banks a road passes. Contour lines are
left out unless `--contours` is given: from a moving camera the shading already shows the
hills, and lines drawn from a different zoom every few seconds only add movement. A flyover draws a
picture every frame, so it is far slower than the rest of the dashboard — about half a
second a frame at 1920×1080, after planning the names' fading over the whole flight — and it
asks for the map and elevation of the ground it sees, at the detail it sees it: a corridor
along the route, finer under the camera than toward the horizon.

```
fitdash morning.fit --basemap local --route-view flyover --video-duration 45s
```

`--layout flyover` is the flyover on its own, filling the whole frame: no dashboard while it
flies, only the map, the route, its pins, the position and the map's credit. It holds the
overview of the whole route for three seconds at the start and at the end, with a card over
it summarising the activity — what it was and on what day, how far and how long, and for a
flight its top speed and cruising level, for anything else its climb and average pace or
speed — which fades as the camera comes down and back as it goes up. The six seconds of
holding are added to the video. It turns `--route-view flyover` on, and needs `--basemap
local` as any flyover does; it suits a portrait frame as well as a wide one. `--title` puts
your own title on the cards in place of the flight's or the sport's, and `--subtitle` a line
under it on the card at the start, saying what the viewer is about to watch.

```
fitdash flight.kml --basemap local --layout flyover --video-duration 60s \
  --title "Going home" --subtitle "Bangkok to Copenhagen on TG950"
```

Frames are drawn several at once, one for each core by default; `--concurrency` sets how
many, for a machine with less memory to spare (each 4K flyover frame in flight holds about
half a gigabyte) or more work to share it with. The video is the same whatever the number. A
render keeps Go's memory use to about half the machine's memory, collecting harder rather
than swapping; `GOMEMLIMIT` sets a different limit.

A flight's airports pass in a frame at the compression its cruise needs, so a flyover finds
them: a track that held 400 km/h for five minutes is a flight, and its take-off and landing are
where it passed 200 km/h. The time the aircraft stood at the gate before it first moved, and
after it last stopped, is left out; the taxi at each end gets about twelve seconds of video,
and from the start of the take-off roll the pace eases to the base rate as the aircraft climbs
away — and back from it as it comes in, to the end of the landing roll — so that the ground
passes under the climb and the descent at the pace it passes under the cruise, the camera
rising and coming down with them. That takes ten to twenty seconds each way, which is added to
the video.

A flight's dashboard reads as a flight's. It is written in nautical miles, feet and knots
unless `--units` says otherwise (a `--unit` still has the last word); the climb panel's gain
and loss and the gradient give way to the altitude and the vertical speed, in feet a minute,
and cadence's box to the track, the direction over the ground as the route runs at that moment
of the video — the line the plane points along — or, standing at a gate, the heading the file
records. The time panel's caption names the
flight where the file does: "TG950 BKK → CPH" from a Flightradar24 KML, the callsign from its
CSV, each leg of a journey in turn; its clock counts from the first pushback, not from when
the tracker began recording. Where the tracker lost the aircraft — an ocean, a country without
receivers — the flight is drawn across the gap as a straight line at a steady speed, the dot
and every reading with it, rather than stopping until the next report and jumping to it; the
summary says how many gaps there were and the longest.

A journey of several flights — one recording, or several merged — is paced at every airport,
the stopovers as much as the two ends, and the time on the ground between flights is left out
with a card saying so as it passes. The video added and the time left out are in the summary,
and `--video-duration` compresses what is left. `--airports on` does the same for any route
view, `off` for none.

Under `3d` and `flyover` the map is drawn in osmbase's **outdoors** palette — fuller greens,
bluer water, deeper hill shading — and the route in the dark navy and crimson made for it,
because there the map is the picture rather than the quiet ground under a small route. The
flat map keeps colours derived from `--theme`, held quiet so the route stands out.
`--basemap-palette theme` or `outdoors` chooses either for any view.

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

**The fluctuating readouts can be drawn against a scale.** Heart rate, pace (or speed),
power and cadence go up and down as an activity progresses, and a bare number says nothing about
whether 148 bpm is this ride's hard effort or its easy one. `--gauge-style track` draws a
labelled axis beneath each of those four with a marker at the current reading;
`--gauge-style dial` draws the identical scale beside the number instead, as a
semicircular arc with a needle. `plain`, the default, is the number alone, unchanged. The
range is the activity's own, snapped outward to round numbers, so a gauge might read
`100`–`190 bpm` or `4:00`–`7:00 min/km`, in whichever units the render is in. It is taken from a smoothed reading rather than
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

**Numbers are written in the units you read.** `--units imperial` writes miles, feet,
mph and min/mi where the default writes kilometres, metres, km/h and min/km, and
`--unit QUANTITY=UNIT` changes one quantity after it — `distance=km|mi|nm`,
`elevation=m|ft`, `speed=km/h|mph|kn|m/s`, `pace=min/km|min/mi` — for the mixtures
some activities are read in: a flight is `--units imperial --unit distance=nm --unit
speed=kn`. The distance readout, the profile's axis and heights, the climb panel and the
pace and speed readouts follow it, and so do `--elevation-gain` and `--elevation-loss`,
read in the elevation unit. Below one distance unit a distance is written in the
elevation unit, feet below a mile as metres below a kilometre. `fitdash inspect` reports
what the file recorded, in the units it recorded it. The flags, and the arithmetic behind
them, are the ones [videofx][videofx] and course use.

**A run is read in pace, a ride in speed.** The gauge column shows one reading of how
fast: pace for running, walking and hiking, speed for every other sport — and for a file
that names no sport, such as most GPX and KML tracks, since speed is what was recorded
and shown as recorded it cannot be the wrong reading, where a pace on a ride or a flight
would be. `--speed-readout pace` or `--speed-readout speed` decides it outright, and the
summary says when speed was shown and why. Unlike pace, speed has a value at a
standstill, so a stopped rider reads `0.0`.

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

## Charting one metric

`fitdash plot` draws one of an activity's metrics against elapsed time or distance,
without rendering anything else: a quick look at a ride's power, a run's heart rate, or a
footpod's developer field.

```
fitdash plot activity.fit                                 # what can it chart?
fitdash plot activity.fit --y power --smooth 30s          # power over time, in the terminal
fitdash plot activity.fit --y pace --x distance           # pace by kilometre
fitdash plot activity.fit --y heart-rate --out hr.png     # a PNG instead
fitdash plot activity.fit --y power,heart-rate,cadence    # several, one above the other
```

Without `--y` it lists the metrics the activity carries and how much of it carries each
(and with shell completion installed, Tab after `--y` offers the same list);
the names are `inspect`'s, so the two never disagree about what a file holds. A developer
field is charted under its own name, matched exactly first: Stryd records a `Power` of
its own beside the watch's `power`, and `--y Power` is Stryd's. `--y power` itself is
read as the dashboard reads it, from `--power-source`: by default a Stryd footpod's where
the activity has one and the file's own power field otherwise, `native` for that field
whichever device recorded it — a watch, a power meter, an exercise machine; the chart
says which. Several metrics are
charted in strips one above the other, each on its own scale and sharing the x axis, so
a moment in one is directly above the same moment in the next.

In iTerm2, WezTerm, kitty and Ghostty the chart is a picture in the terminal, the width
of the window. Anywhere else it is drawn in braille dots, two by four to a character —
coarse, but readable, and it works over ssh and in a log. Output into a pipe or a file,
or through tmux or screen, is braille too, since a picture's escape sequence would
otherwise arrive as a page of base64; `--terminal iterm|kitty|braille` chooses outright,
for a terminal the detection misses or a tmux set to pass pictures through. `--out`
writes a PNG, 1600 by 600 unless `--width` and `--height` say otherwise, in the
dashboard's type and `--theme`, with a faint band behind the line from each moment's
lowest reading to its highest. A PNG is light by default; a picture in the terminal
follows the terminal's background where it says (`COLORFGBG`) and is dark where it
does not.

A missing reading is never drawn as zero: the line breaks where the readings stop for
longer than the recording's own rhythm and the chart's resolution can hide, and a metric
the activity carries only part of the time is drawn with a note saying how much. The y
axis is set from where most readings lie, leaving the highest and lowest hundredth out,
so a power meter's dropouts and sprints do not flatten the rest of the ride; a reading
beyond it is drawn at its edge. Pace runs faster-up, and units follow `--units` and
`--unit` as the render's do, a flight's in nautical miles, feet and knots.

## Privacy

A rendered dashboard is a video of where you were, minute by minute, and what your
body was doing; a chart from `fitdash plot` is the same data in another shape. That is the whole point of the program, and it is yours to publish or
not — but nothing here should make that choice quietly on your behalf. No sample
activity is committed to this repository; tests generate synthetic ones.

## License

Apache-2.0. See `LICENSE`, and `NOTICE` for third-party attribution.
