package cmd

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
)

// parseHighlight parses one --highlight value into a panel.Highlight.
//
// Grammar: comma-separated key=value fields, order-insensitive. from and to
// are required, Go duration syntax (12m30s) into the activity's ELAPSED
// time -- fitdash has already committed to Go durations for --frame-at and
// --video-duration, and two time grammars in one CLI would be a worse cost
// than differing from videofx's three-form parser. name is optional free
// text. video and speedup are optional and mutually exclusive: how much
// VIDEO time this stretch should occupy, or its own compression factor.
// Neither set is a real use too -- "mark it, do not re-pace it", calling out
// a climb without stretching the video around it. background is optional,
// hex only (#RGB or #RRGGBB, opaque only -- see parseBackgroundColor), and is
// resolved here without regard to --highlight-style: the refusal for a style
// under which it has no meaning happens in resolveHighlights, which is the
// first place both the highlight and the style are in scope together.
//
// Escaping: fields split on UNESCAPED commas, so a comma inside a name needs
// \, and a literal backslash needs \\. The first = in a field separates the
// key from the value, so a value may contain = freely -- only a key never
// can. A repeated key is an error, not last-wins: silently preferring one of
// two contradictory values for the same field is the same failure class as
// silently substituting a default. See parseFields and splitFields
// (cmd/fields.go) for the shared lexer this grammar is built on.
//
// Bounds against the activity itself -- from at or past the activity's own
// length, or two highlights that overlap -- are NOT checked here, because
// this function sees one value in isolation and knows nothing about the
// activity or about any other --highlight. See resolveHighlights.
func parseHighlight(raw string) (panel.Highlight, error) {
	const flag = "--highlight"
	fields, err := parseFields(raw, flag)
	if err != nil {
		return panel.Highlight{}, err
	}

	var h panel.Highlight
	var hasFrom, hasTo, hasVideo, hasSpeedup bool
	for key, value := range fields {
		switch key {
		case "from":
			d, err := parseDurationField(flag, raw, key, value)
			if err != nil {
				return panel.Highlight{}, err
			}
			h.From, hasFrom = d, true
		case "to":
			d, err := parseDurationField(flag, raw, key, value)
			if err != nil {
				return panel.Highlight{}, err
			}
			h.To, hasTo = d, true
		case "name":
			h.Name = value
		case "video":
			d, err := parseDurationField(flag, raw, key, value)
			if err != nil {
				return panel.Highlight{}, err
			}
			if d <= 0 {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: video must be positive, got %v", raw, d)
			}
			h.Video, hasVideo = d, true
		case "speedup":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: speedup %q is not a number: %w", raw, value, err)
			}
			if f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: speedup must be a positive finite number, got %v", raw, f)
			}
			h.RateFactor, hasSpeedup = f, true
		case "background":
			col, err := parseBackgroundColor(flag, raw, value)
			if err != nil {
				return panel.Highlight{}, err
			}
			h.Background, h.HasBackground = col, true
		default:
			return panel.Highlight{}, fmt.Errorf("render: --highlight %q: unknown field %q", raw, key)
		}
	}

	if !hasFrom || !hasTo {
		return panel.Highlight{}, fmt.Errorf("render: --highlight %q: from and to are both required", raw)
	}
	if hasVideo && hasSpeedup {
		return panel.Highlight{}, fmt.Errorf("render: --highlight %q: video and speedup are mutually exclusive", raw)
	}
	if h.From < 0 {
		return panel.Highlight{}, fmt.Errorf("render: --highlight %q: from %v is negative", raw, h.From)
	}
	if h.To <= h.From {
		return panel.Highlight{}, fmt.Errorf("render: --highlight %q: to (%v) must be after from (%v); a zero-length or reversed highlight has nothing to occupy and nothing to name",
			raw, h.To, h.From)
	}
	return h, nil
}

// splitHighlightFields splits raw on --highlight's own unescaped commas.
//
// A thin wrapper over the shared splitFields lexer (see cmd/fields.go) rather
// than a second copy of it: --highlight was the first flag to need this
// grammar, kept under its own name so existing callers -- and this file's own
// tests -- need no change now that the lexer itself lives in fields.go with
// the flag name threaded through.
func splitHighlightFields(raw string) ([]string, error) {
	return splitFields(raw, "--highlight")
}

// resolveHighlights parses every --highlight value and resolves the whole
// set against the activity's own window: sorting by start (argv order is a
// typing convenience, not a statement), clipping a highlight that runs past
// the activity's end, marking one that lies wholly inside a paused stretch,
// and refusing what has no defined answer -- a highlight starting at or
// past the activity's own end, two highlights that overlap, or a background=
// under a style that gives it no meaning (see backgroundStyleError).
//
// style is passed in -- rather than resolved after this call, as it used to
// be -- because that last refusal needs it: --highlight-style is validated by
// parseHighlightStyle, which runRender now calls BEFORE resolveHighlights
// specifically so the style is in scope here, the first place both a
// highlight and the style it will be drawn under exist together.
//
// Returns nil, nil when raw is empty: no --highlight given at all is not an
// error, and a nil Context.Highlights is exactly what a highlight panel's
// Accepts declines on.
func resolveHighlights(raw []string, timer *fitactivity.TimerModel, style string) ([]panel.Highlight, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if timer == nil {
		return nil, fmt.Errorf("render: --highlight given but there is no timer model to resolve it against")
	}
	start, end := timer.Window()
	if start.IsZero() || end.IsZero() {
		return nil, fmt.Errorf("render: --highlight given but the activity has no resolved time window")
	}
	duration := end.Sub(start)

	highlights := make([]panel.Highlight, 0, len(raw))
	for _, r := range raw {
		h, err := parseHighlight(r)
		if err != nil {
			return nil, err
		}
		if h.HasBackground && style != panel.HighlightStyleWash {
			return nil, backgroundStyleError(r, style)
		}
		// from at or past the end has no obvious intended meaning the way a
		// "to" running past the end does -- there is nothing sensible to
		// clip it TO -- so it is refused rather than adjusted, the same
		// precedent frameIndices already follows for --frame-at.
		if h.From >= duration {
			return nil, fmt.Errorf("render: --highlight %q: from %v is at or past the activity's end, which runs %s",
				r, h.From, panel.FormatClock(duration))
		}
		if h.To > duration {
			h.To, h.Clipped = duration, true
		}
		h.PausedThroughout = highlightLiesInPause(timer, start, h.From, h.To)
		highlights = append(highlights, h)
	}

	sort.Slice(highlights, func(i, j int) bool { return highlights[i].From < highlights[j].From })

	for i := 1; i < len(highlights); i++ {
		prev, cur := highlights[i-1], highlights[i]
		if cur.From < prev.To {
			return nil, fmt.Errorf("render: --highlight %q overlaps %q; touching endpoints (one ending exactly where the next begins) are fine, overlapping ranges are not",
				highlightLabel(prev), highlightLabel(cur))
		}
	}

	return highlights, nil
}

// highlightLabel names a highlight for an error message: its own name, or
// its bounds when it has none.
func highlightLabel(h panel.Highlight) string {
	if h.Name != "" {
		return h.Name
	}
	return fmt.Sprintf("%v-%v", h.From, h.To)
}

// highlightLiesInPause reports whether a highlight's whole span sits inside
// one of the activity's paused stretches, using TimerModel.Paused rather
// than deriving pause status some other way -- see that method's own doc
// comment for why there is exactly one rule for it.
//
// It SAMPLES rather than checking exhaustively: fitactivity exposes only a
// point-in-time query, not the pause list itself, and adding one would be a
// change to fitactivity, out of scope here (see this repository's CLAUDE.md
// on accessors belonging upstream). The two cases this warning exists for
// both answer correctly from a handful of evenly spaced samples: a highlight
// marking a genuine rest stop is paused at every one of them, and a
// highlight over a real effort is unpaused at the first.
func highlightLiesInPause(timer *fitactivity.TimerModel, activityStart time.Time, from, to time.Duration) bool {
	const samples = 9
	span := to - from
	if span <= 0 {
		return false
	}
	for i := 0; i < samples; i++ {
		offset := from + span*time.Duration(i)/time.Duration(samples-1)
		if i == samples-1 {
			// The interval is half-open: the instant AT "to" belongs to
			// whatever comes after it, so step back one nanosecond to stay
			// inside the highlight itself.
			offset--
		}
		if !timer.Paused(activityStart.Add(offset)) {
			return false
		}
	}
	return true
}

// backgroundStyleError refuses a background= under a --highlight-style other
// than wash.
//
// USER-APPROVED: refused rather than the two friendlier alternatives. Not
// auto-upgraded to wash for that one highlight -- the user may have typed
// --highlight-style border or none deliberately, and silently overriding it
// for one highlight contradicts a flag they set on purpose. Not silently
// ignored either, which is this project's own rule against a flag that
// quietly does nothing: left unruled, background= under border would have no
// effect at all, this project's characteristic bug in flag form.
func backgroundStyleError(raw, style string) error {
	msg := fmt.Sprintf("render: --highlight %q: background is set but --highlight-style is %q; background only has meaning under --highlight-style %s",
		raw, style, panel.HighlightStyleWash)
	if style == panel.HighlightStyleNone {
		// NOT "none means do not mark on screen" -- that was true when this
		// message was first written and is not true now that the marker
		// strip and the route mark both still draw under "none"; only the
		// FRAME ITSELF is left unmarked, which is the one thing background=
		// would have coloured. Say that instead of a sentence that is
		// simply wrong about what "none" does.
		msg += `; "none" leaves the frame itself unmarked -- the marker strip and the route mark still draw`
	}
	return fmt.Errorf("%s", msg)
}

// highlightStyles enumerates --highlight-style's legal values. The values
// themselves live in panel (HighlightStyleBorder etc.) because internal/render
// compares Context.HighlightStyle against them -- to decide whether to draw
// the margin border, and whether to build the wash's colour-keyed table of
// static bases (one per DISTINCT wash colour, not one per highlight and not
// a second base overall -- see internal/render.washBaseFor) -- and a string
// literal repeated across two packages is one typo away from a style that
// validates here and silently does nothing there. The SET belongs here
// because only the CLI's own validation and its error message need it.
var highlightStyles = []string{panel.HighlightStyleBorder, panel.HighlightStyleWash, panel.HighlightStyleNone}

// parseHighlightStyle validates --highlight-style, refusing an unknown value
// where the user typed it rather than silently falling back to the default
// -- the same precedent SelectLayout and SelectTheme follow: a typo should
// not cost a whole render by rendering with a style nobody asked for.
func parseHighlightStyle(style string) (string, error) {
	for _, s := range highlightStyles {
		if s == style {
			return style, nil
		}
	}
	return "", fmt.Errorf("render: --highlight-style %q is invalid; use %s", style, strings.Join(highlightStyles, ", "))
}

// parseBackgroundColor parses a background= value: hex only, opaque only.
//
// A leading "#" is required, so background=blue is refused rather than
// silently parsed against some named-colour table this program does not
// have and is not adding. #RGB and #RRGGBB are accepted, case-insensitively
// (strconv.ParseUint's base-16 parsing already accepts both). #RRGGBBAA is
// refused with its own message, not folded into "not a valid colour": a
// background is opaque by construction, because internal/render's blendBases
// lerps two static bases byte for byte and that lerp is an EXACT colour
// blend only because both bases are fully opaque (see that function's own
// doc comment) -- a translucent background would silently turn an exact
// blend into an approximation of one.
//
// The escaping cmd/fields.go already handles needs nothing new for this:
// only "," and "\" are special, and the first "=" splits a field, so "#"
// requires no further care here.
//
// The underlying strconv.ParseUint error is wrapped with %w rather than
// discarded, the same discipline parseDurationField (cmd/fields.go) already
// applies to time.ParseDuration's own error: a hex digit's actual complaint
// ("invalid syntax" on a stray "g", say) is more specific than this
// function's own "is not valid hex" and costs nothing to keep. errors.Join
// collapses up to three per-channel errors into the one %w this message
// carries, since a caller has one string to read, not three.
func parseBackgroundColor(flag, raw, value string) (color.NRGBA, error) {
	if !strings.HasPrefix(value, "#") {
		return color.NRGBA{}, fmt.Errorf("render: %s %q: background %q must start with \"#\" (e.g. #1B2A4A)", flag, raw, value)
	}
	hex := value[1:]
	switch len(hex) {
	case 3:
		r, err1 := strconv.ParseUint(hex[0:1]+hex[0:1], 16, 8)
		g, err2 := strconv.ParseUint(hex[1:2]+hex[1:2], 16, 8)
		b, err3 := strconv.ParseUint(hex[2:3]+hex[2:3], 16, 8)
		if err := errors.Join(err1, err2, err3); err != nil {
			return color.NRGBA{}, fmt.Errorf("render: %s %q: background %q is not valid hex: %w", flag, raw, value, err)
		}
		return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xFF}, nil
	case 6:
		r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
		g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
		b, err3 := strconv.ParseUint(hex[4:6], 16, 8)
		if err := errors.Join(err1, err2, err3); err != nil {
			return color.NRGBA{}, fmt.Errorf("render: %s %q: background %q is not valid hex: %w", flag, raw, value, err)
		}
		return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xFF}, nil
	case 8:
		return color.NRGBA{}, fmt.Errorf("render: %s %q: background %q is opaque only; there is nothing behind the frame for alpha to reveal", flag, raw, value)
	default:
		return color.NRGBA{}, fmt.Errorf("render: %s %q: background %q is not #RGB or #RRGGBB", flag, raw, value)
	}
}
