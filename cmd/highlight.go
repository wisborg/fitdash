package cmd

import (
	"fmt"
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
// a climb without stretching the video around it.
//
// Escaping: fields split on UNESCAPED commas, so a comma inside a name needs
// \, and a literal backslash needs \\. The first = in a field separates the
// key from the value, so a value may contain = freely -- only a key never
// can. A repeated key is an error, not last-wins: silently preferring one of
// two contradictory values for the same field is the same failure class as
// silently substituting a default.
//
// Bounds against the activity itself -- from at or past the activity's own
// length, or two highlights that overlap -- are NOT checked here, because
// this function sees one value in isolation and knows nothing about the
// activity or about any other --highlight. See resolveHighlights.
func parseHighlight(raw string) (panel.Highlight, error) {
	fields, err := splitHighlightFields(raw)
	if err != nil {
		return panel.Highlight{}, err
	}

	var h panel.Highlight
	var hasFrom, hasTo, hasVideo, hasSpeedup bool
	seen := map[string]bool{}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return panel.Highlight{}, fmt.Errorf("render: --highlight %q: field %q has no \"=\"", raw, field)
		}
		if seen[key] {
			return panel.Highlight{}, fmt.Errorf("render: --highlight %q: %q is repeated", raw, key)
		}
		seen[key] = true

		switch key {
		case "from":
			d, err := time.ParseDuration(value)
			if err != nil {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: from %q is not a duration: %w", raw, value, err)
			}
			h.From, hasFrom = d, true
		case "to":
			d, err := time.ParseDuration(value)
			if err != nil {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: to %q is not a duration: %w", raw, value, err)
			}
			h.To, hasTo = d, true
		case "name":
			h.Name = value
		case "video":
			d, err := time.ParseDuration(value)
			if err != nil {
				return panel.Highlight{}, fmt.Errorf("render: --highlight %q: video %q is not a duration: %w", raw, value, err)
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

// splitHighlightFields splits raw on unescaped commas. \, is a literal
// comma and \\ a literal backslash; no other escape is recognised, so a
// stray backslash is refused rather than silently swallowed or passed
// through with a meaning nobody asked for.
func splitHighlightFields(raw string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	escaped := false
	for _, r := range raw {
		if escaped {
			switch r {
			case ',', '\\':
				cur.WriteRune(r)
			default:
				return nil, fmt.Errorf("render: --highlight %q: unknown escape \\%c", raw, r)
			}
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case ',':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if escaped {
		return nil, fmt.Errorf("render: --highlight %q ends with a trailing backslash", raw)
	}
	fields = append(fields, cur.String())
	return fields, nil
}

// resolveHighlights parses every --highlight value and resolves the whole
// set against the activity's own window: sorting by start (argv order is a
// typing convenience, not a statement), clipping a highlight that runs past
// the activity's end, marking one that lies wholly inside a paused stretch,
// and refusing what has no defined answer -- a highlight starting at or
// past the activity's own end, or two highlights that overlap.
//
// Returns nil, nil when raw is empty: no --highlight given at all is not an
// error, and a nil Context.Highlights is exactly what a highlight panel's
// Accepts declines on.
func resolveHighlights(raw []string, timer *fitactivity.TimerModel) ([]panel.Highlight, error) {
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

// highlightStyles enumerates --highlight-style's legal values. The values
// themselves live in panel (HighlightStyleBorder etc.) because internal/render
// compares Context.HighlightStyle against them -- to decide whether to draw
// the margin border, and whether to build the wash's second static base --
// and a string literal repeated across two packages is one typo away from a
// style that validates here and silently does nothing there. The SET belongs
// here because only the CLI's own validation and its error message need it.
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
