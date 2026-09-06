# fitdash

A Go CLI that renders a recorded exercise as a dashboard video: it reads a Garmin
FIT activity and produces a video in which the activity's metrics animate as it
progresses — a route outline that draws itself, an elevation profile with a moving
playhead, pace/heart rate/power readouts, splits.

There is **no input video**. Unlike videofx, which composites a HUD onto footage
someone shot, fitdash generates every pixel of every frame from the activity alone.
Two consequences run through the whole design: the output video's timeline is the
*activity's* timeline (there is no camera clock to sync against, and no clock-skew
problem), and there is no source image to hide behind — a panel that draws nothing
leaves a hole.

## Building and testing

Pure Go. No cgo, no OpenCV, no pkg-config. `ffmpeg` must be on `PATH` — it is the
only external program, used to encode the rendered RGBA frames into a video file.

**Use `scripts/fd` rather than composing the equivalent pipeline by hand**, and do
this even when the ad-hoc version looks shorter. A pipeline typed fresh each time —
a different `grep`, a different redirect, an `echo "exit=$?"` in a new spelling — is
a new command string every time, and under a permission allowlist each variant costs
a fresh approval for a job that was already approved in another dress.

```
scripts/fd check-deps            # ffmpeg + the ../fitactivity checkout
scripts/fd gates                 # gofmt + vet + test, one status line each
scripts/fd test ./internal/panel/ SomePattern
scripts/fd build                 # -> ./fitdash
scripts/fd frames ACTIVITY.fit   # single frames as PNG, the fast visual loop
scripts/fd render ACTIVITY.fit   # end-to-end -> .scratch/
scripts/fd diff                  # working diff (incl. untracked) -> .scratch/
scripts/fd clean                 # empty .scratch/
```

Every subcommand ends with one `fd <command>: ok|FAILED (exit N)` line and exits
with the underlying status, so **never append your own `echo "$?"`**.

There is a `Makefile` too, and every target is a thin delegation to the same
script — `make gates`, `make build`, `make test PKG=./internal/panel/ RUN=Name`,
`make frames FIT=ACT.fit ARGS="--theme light"`. It is the discoverable front
door (`make` on its own lists the targets); `scripts/fd` remains where the work
is actually spelled out. Use either, but **do not add a target that reimplements
a job** — a second spelling of `go test ./...` is exactly what fd exists to
prevent. Note that `make clean` removes the built binary *as well as* emptying
`.scratch/`, which `scripts/fd clean` alone does not.

Anything a session writes — renders, captured diffs, inspected PNG frames — goes in
the gitignored `.scratch/` at the repo root, never a per-session temp directory. It
is the same path in every session, so it can be allowlisted once and cleaned in one
step. `scripts/fd clean` empties it but keeps `.scratch/env`, which is where local
defaults like `FD_FIT` live.

Scripts under `scripts/` ship in the public repo, so they must not embed private
paths: take the activity as an **argument** (or read it from `.scratch/env`) rather
than hardcoding anything under `example_files/`.

## FIT parsing is NOT in this repo

Reading a FIT file, interpolating a track, and the elevation / splits / timer models
all live in **`github.com/wisborg/fitactivity`**, an ordinary tagged dependency. To work
on it and this project at the same time, add a temporary
`replace github.com/wisborg/fitactivity => ../fitactivity` and take it out before
committing — a `replace` on `main` makes the build depend on a checkout nobody else has.

That module is **shared with videofx**. So:

- A change to decoding, `Track.At`, the elevation model, splits or the timer model
  belongs in `../fitactivity`, on its own topic branch, and must be verified against
  **both** consumers before it is considered done. It is not a fitdash commit.
- A change to how a *panel* reads or presents that data belongs here.
- If you find yourself wanting a new accessor on `Sample` or `Track`, that is a
  library change — say so rather than reimplementing it locally. Two copies of FIT
  sentinel handling is the single worst outcome this split exists to prevent.

`fitactivity/fittest` generates synthetic activities, so tests here need no real
recording.

## Absence is not zero

FIT marks an absent field with a type-specific sentinel, not zero, and `fitactivity`
turns every one of those into an explicit presence flag (`Sample.HasHeartRate`,
`HasGPS`, `HasPower`, …; for developer fields, membership in `Sample.DevFields`).

**This project is where that care gets thrown away or preserved.** A panel that
renders a missing heart rate as `0 bpm`, or plots a `0,0` GPS point at the start of
a route, has converted "we do not know" into a confident lie, and it does it in
pixels the viewer cannot inspect. Check the flag. Every time.

## Different activities carry different metrics

A pool swim has no GPS. An indoor ride has no elevation. A walk has no power. This
is the normal case, not an edge case, which is why panels are pluggable at all —
and why **every panel needs a decided policy for its data being absent**, chosen
deliberately rather than falling out of the code:

- draw a placeholder (keeps the layout stable, says "no data" honestly), or
- decline to draw and let the layout close up around it.

A panel that silently draws nothing when its data is missing is the third option
and is a bug: it leaves an unexplained hole and looks identical to a panel that
crashed.

Which of the two a panel gets is decided by *when* the absence is knowable, and the
mechanism is in `docs/architecture.md`: an activity that never carried the metric is
knowable before the layout is resolved, so the panel declines and its siblings grow;
a dropout mid-activity is only knowable per frame, by which point the box is already
assigned, so it must be a placeholder.

## Layout

**`docs/architecture.md` is the design this is being built to.** Read it before planning
or implementing anything in the render path — it carries the reasoning behind the panel
contract, the layout model, the absent-data policy and the timeline choice, including the
alternatives that were rejected and why. The summary below is only the map.

- `cmd/` — the CLI. The render is the ROOT command (`fitdash ACTIVITY.fit`); `inspect`
  is a subcommand.
- `internal/panel/` — the `Panel` contract and the layout model. One panel per file.
  Panels are placed by weight in a tree of nested rows and columns, so a layout scales
  across resolutions and closes up around a panel that declines to draw.
- `internal/render/` — the frame loop: activity timeline → per-frame state → RGBA.
- `internal/encode/` — the ffmpeg rawvideo pipe. Knows nothing about panels or activities.
- `internal/route/` — GPS projection and route drawing. The map-service integration
  (Mapbox / Google / OSM tiles, and later a 3D view) lands here, behind an interface,
  and is **opt-in**: see the note on third-party map services below.
- `internal/inspect/` — the coverage report, and the single source of truth for whether
  an activity carries a given metric at all. Panels ask it rather than walking the
  samples themselves, so a panel cannot disagree with what `fitdash inspect` just printed.

## Renders are personal data

The whole output is a rendering of where somebody was, minute by minute. That is the
product and the user's call to make — but it must never be made silently, and it must
never be made by this repository on the user's behalf.

- **`example_files/` is gitignored and holds a real recording.** It is a real
  activity with real coordinates, dates and physiological data. It never enters a
  commit, a fixture, a doc example, a README, a log paste, or an issue. Use it to
  test; publish nothing derived from it.
- **No real coordinates in fixtures or documentation.** `fitactivity/fittest`
  generates a synthetic activity for exactly this reason.
- **A third-party map service is data exfiltration.** Sending tile requests to
  Mapbox, Google or an OSM host tells that host the route. That may be entirely fine,
  but it must be opt-in, visible, off by default, and documented — and any tile
  provider's attribution and terms-of-use obligations have to be checked against this
  project's Apache-2.0 licence before the dependency lands, not after.

## This repository is public

Published at `wisborg/fitdash` under Apache-2.0. Every dependency must be
licence-compatible; check a new one before adding it, and record it in `NOTICE`.
ffmpeg is executed as a separate program through its documented CLI, never linked,
so its licence does not propagate — which is also why no prebuilt binary should ever
be shipped from a machine whose ffmpeg is GPL.

## Git

Follow the global branch-first rule: create the topic branch *before* the first edit,
and do not move `main` until the user gives the go-ahead.

```
git checkout -b <topic>     # before editing anything
git commit ...              # as the work lands
```

Then stop and report which branch the work is on. Once the user says to go ahead,
`main` here wants a linear history with no merge commits:

```
git checkout main && git merge --ff-only <topic> && git branch -d <topic>
```

Do not push; that is the user's step. Commit messages are long and explain *why*: an
imperative subject line, then several paragraphs on what was wrong, what was
considered and rejected, and what a reader might otherwise mistakenly "fix". Do not
reference other commits by hash.
