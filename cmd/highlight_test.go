package cmd

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
)

var highlightEpoch = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestParseHighlight_ParsesTheGrammar pins the comma-separated key=value
// grammar, including the two escaping rules and that only the FIRST "="
// separates a key from its value -- so a value may contain "=" freely.
func TestParseHighlight_ParsesTheGrammar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want panel.Highlight
	}{
		{
			name: "every field",
			in:   "from=12m30s,to=16m10s,video=10s,name=Hill climb",
			want: panel.Highlight{From: 12*time.Minute + 30*time.Second, To: 16*time.Minute + 10*time.Second, Video: 10 * time.Second, Name: "Hill climb"},
		},
		{
			name: "speedup instead of video",
			in:   "from=20m,to=21m,speedup=5,name=Sprint 1",
			want: panel.Highlight{From: 20 * time.Minute, To: 21 * time.Minute, RateFactor: 5, Name: "Sprint 1"},
		},
		{
			name: "from and to only -- mark it, do not re-pace it",
			in:   "from=21m,to=22m30s,name=Recovery",
			want: panel.Highlight{From: 21 * time.Minute, To: 22*time.Minute + 30*time.Second, Name: "Recovery"},
		},
		{
			name: "field order does not matter",
			in:   "name=Recovery,to=22m30s,from=21m",
			want: panel.Highlight{From: 21 * time.Minute, To: 22*time.Minute + 30*time.Second, Name: "Recovery"},
		},
		{
			name: "an escaped comma is literal inside the name",
			in:   `from=1m,to=2m,name=Hills\, and more hills`,
			want: panel.Highlight{From: time.Minute, To: 2 * time.Minute, Name: "Hills, and more hills"},
		},
		{
			name: "an escaped backslash is literal",
			in:   `from=1m,to=2m,name=C:\\temp`,
			want: panel.Highlight{From: time.Minute, To: 2 * time.Minute, Name: `C:\temp`},
		},
		{
			name: "the first = splits the field; a value may contain = freely",
			in:   "from=1m,to=2m,name=Pace=Sub 4",
			want: panel.Highlight{From: time.Minute, To: 2 * time.Minute, Name: "Pace=Sub 4"},
		},
		{
			name: "no name is legal",
			in:   "from=1m,to=2m",
			want: panel.Highlight{From: time.Minute, To: 2 * time.Minute},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseHighlight(c.in)
			if err != nil {
				t.Fatalf("parseHighlight(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseHighlight(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// TestParseHighlight_RejectsEveryFatalCase pins section D's "fatal at parse
// time" rules: each is a value that cannot mean what it appears to, refused
// at the flag rather than producing a Highlight nobody asked for.
func TestParseHighlight_RejectsEveryFatalCase(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"a field with no equals sign", "from=1m,to=2m,broken", `has no "="`},
		{"an unknown key", "from=1m,to=2m,colour=red", "unknown field"},
		{"a repeated key", "from=1m,to=2m,from=3m", "repeated"},
		{"an unparseable from", "from=soon,to=2m", "not a duration"},
		{"an unparseable to", "from=1m,to=later", "not a duration"},
		{"missing from", "to=2m,name=x", "both required"},
		{"missing to", "from=1m,name=x", "both required"},
		{"missing both", "name=x", "both required"},
		{"video and speedup both set", "from=1m,to=2m,video=10s,speedup=5", "mutually exclusive"},
		{"video is zero", "from=1m,to=2m,video=0s", "video must be positive"},
		{"video is negative", "from=1m,to=2m,video=-10s", "video must be positive"},
		{"speedup is zero", "from=1m,to=2m,speedup=0", "positive finite"},
		{"speedup is negative", "from=1m,to=2m,speedup=-3", "positive finite"},
		{"speedup is infinite", "from=1m,to=2m,speedup=Inf", "positive finite"},
		{"speedup is NaN", "from=1m,to=2m,speedup=NaN", "positive finite"},
		{"to before from", "from=5m,to=2m", "must be after"},
		{"to equal to from", "from=5m,to=5m", "must be after"},
		{"from is negative", "from=-1m,to=2m", "negative"},
		{"a trailing backslash", `from=1m,to=2m,name=x\`, "trailing backslash"},
		{"an unrecognised escape", `from=1m,to=2m,name=x\y`, "unknown escape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseHighlight(c.in)
			if err == nil {
				t.Fatalf("parseHighlight(%q) accepted it", c.in)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parseHighlight(%q) error = %v, want it to mention %q", c.in, err, c.wantErr)
			}
		})
	}
}

// TestSplitHighlightFields_HonoursTheTwoEscapes pins the splitter in
// isolation from the field parsing built on top of it.
func TestSplitHighlightFields_HonoursTheTwoEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"no escapes", "a=1,b=2,c=3", []string{"a=1", "b=2", "c=3"}},
		{"an escaped comma stays inside its field", `a=1\,2,b=3`, []string{`a=1,2`, "b=3"}},
		{"an escaped backslash is literal", `a=1\\2,b=3`, []string{`a=1\2`, "b=3"}},
		{"a single field with no commas at all", "a=1", []string{"a=1"}},
		{"an empty string is one empty field", "", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := splitHighlightFields(c.in)
			if err != nil {
				t.Fatalf("splitHighlightFields(%q): %v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("splitHighlightFields(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("splitHighlightFields(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}

	if _, err := splitHighlightFields(`a=1\`); err == nil {
		t.Error("a trailing backslash was accepted")
	}
	if _, err := splitHighlightFields(`a=1\x`); err == nil {
		t.Error("an unrecognised escape was accepted")
	}
}

// syntheticTimer builds a TimerModel directly from an ActivityTiming rather
// than through fittest + Decode, which is unnecessary weight for tests that
// only need a window and a pause -- neither reads a real recording or an
// example file, satisfying the same "no real coordinates" rule fittest
// exists for by construction (there is no GPS data here at all).
func syntheticTimer(totalElapsed time.Duration, pauses ...[2]time.Duration) *fitactivity.TimerModel {
	events := []fitactivity.TimerEvent{{Time: highlightEpoch, Start: true}}
	for _, p := range pauses {
		events = append(events,
			fitactivity.TimerEvent{Time: highlightEpoch.Add(p[0]), Start: false},
			fitactivity.TimerEvent{Time: highlightEpoch.Add(p[1]), Start: true},
		)
	}
	events = append(events, fitactivity.TimerEvent{Time: highlightEpoch.Add(totalElapsed), Start: false})
	track := &fitactivity.Track{Timing: fitactivity.ActivityTiming{
		Start: highlightEpoch, TotalElapsed: totalElapsed, HasTotals: true, Events: events,
	}}
	return fitactivity.BuildTimerModel(track)
}

// TestResolveHighlights_NoneGivenIsNotAnError pins that an empty --highlight
// set resolves to nil rather than an error -- a highlight panel's Accepts
// declines on exactly this, and it is a fact about the flags, not a failure.
func TestResolveHighlights_NoneGivenIsNotAnError(t *testing.T) {
	got, err := resolveHighlights(nil, syntheticTimer(30*time.Minute), panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveHighlights(nil, ...): %v", err)
	}
	if got != nil {
		t.Errorf("resolveHighlights(nil, ...) = %v, want nil", got)
	}
}

// TestResolveHighlights_SortsByStart pins that argv order is a typing
// convenience, not a statement: out-of-order highlights come back sorted
// ascending by From rather than being refused.
func TestResolveHighlights_SortsByStart(t *testing.T) {
	raw := []string{
		"from=20m,to=21m,name=Second",
		"from=5m,to=6m,name=First",
	}
	got, err := resolveHighlights(raw, syntheticTimer(30*time.Minute), panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveHighlights: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolveHighlights returned %d highlights, want 2", len(got))
	}
	if got[0].Name != "First" || got[1].Name != "Second" {
		t.Errorf("resolveHighlights did not sort by From: got %q then %q", got[0].Name, got[1].Name)
	}
}

// TestResolveHighlights_ClipsToTheActivitysEnd pins the "adjusted, and
// reported" case: a to running past the activity's own end is pulled back to
// it and marked Clipped, rather than refused or silently dropped.
func TestResolveHighlights_ClipsToTheActivitysEnd(t *testing.T) {
	timer := syntheticTimer(25*time.Minute + 53*time.Second)
	got, err := resolveHighlights([]string{"from=25m,to=40m,name=Final push"}, timer, panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveHighlights: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("resolveHighlights returned %d highlights, want 1", len(got))
	}
	h := got[0]
	if !h.Clipped {
		t.Error("a highlight running past the activity's end was not marked Clipped")
	}
	if want := 25*time.Minute + 53*time.Second; h.To != want {
		t.Errorf("h.To = %v, want the activity's own end %v", h.To, want)
	}
}

// TestResolveHighlights_RejectsFromAtOrPastTheEnd follows the precedent
// frameIndices already sets for --frame-at: a POINT past the end has no
// well-defined nearest frame, so it is refused rather than clamped.
func TestResolveHighlights_RejectsFromAtOrPastTheEnd(t *testing.T) {
	timer := syntheticTimer(25 * time.Minute)
	if _, err := resolveHighlights([]string{"from=25m,to=26m"}, timer, panel.HighlightStyleBorder, panel.PausesFreeze); err == nil {
		t.Fatal("a highlight starting exactly at the activity's end was accepted")
	}
	if _, err := resolveHighlights([]string{"from=40m,to=41m"}, timer, panel.HighlightStyleBorder, panel.PausesFreeze); err == nil {
		t.Fatal("a highlight starting well past the activity's end was accepted")
	}
}

// TestResolveHighlights_RejectsOverlapButAllowsTouchingEndpoints pins both
// halves of the overlap rule: two ranges that share time have no defined
// composition and are refused, but back-to-back reps -- one ending exactly
// where the next begins -- are a real use and must be allowed.
func TestResolveHighlights_RejectsOverlapButAllowsTouchingEndpoints(t *testing.T) {
	timer := syntheticTimer(30 * time.Minute)

	if _, err := resolveHighlights([]string{
		"from=5m,to=10m,name=A",
		"from=9m,to=12m,name=B",
	}, timer, panel.HighlightStyleBorder, panel.PausesFreeze); err == nil {
		t.Fatal("overlapping highlights were accepted")
	} else if !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "B") {
		t.Errorf("the overlap error should name both highlights; got: %v", err)
	}

	got, err := resolveHighlights([]string{
		"from=5m,to=10m,name=A",
		"from=10m,to=12m,name=B",
	}, timer, panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("touching endpoints were refused: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolveHighlights returned %d highlights, want 2", len(got))
	}
}

// TestResolveHighlights_MarksWhatLiesInAPause pins the warning-worthy case:
// a highlight wholly inside one of the activity's paused stretches is
// marked, using TimerModel.Paused, so a later summary can say so rather than
// leaving the reader to wonder why the dashboard froze for ten seconds.
func TestResolveHighlights_MarksWhatLiesInAPause(t *testing.T) {
	// Paused from 10m to 11m.
	timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 11 * time.Minute})

	got, err := resolveHighlights([]string{
		"from=10m10s,to=10m50s,name=Water stop",
		"from=20m,to=21m,name=Sprint",
	}, timer, panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveHighlights: %v", err)
	}
	byName := map[string]panel.Highlight{}
	for _, h := range got {
		byName[h.Name] = h
	}
	if !byName["Water stop"].PausedThroughout {
		t.Error("a highlight entirely inside a pause was not marked PausedThroughout")
	}
	if byName["Sprint"].PausedThroughout {
		t.Error("a highlight over a real effort was marked PausedThroughout")
	}
}

// TestResolveHighlights_StraddlingAPauseBoundaryIsNotPausedThroughout
// exercises the nine-point sampling against the case
// TestResolveHighlights_MarksWhatLiesInAPause never tries: both of that
// test's highlights sit cleanly on one side of the pause (wholly inside,
// wholly outside). This one starts well before the pause and ends well
// after it, so the MAJORITY of its own span is not frozen at all -- the
// straddling case highlightLiesInPause's own doc comment says the sampling
// answers correctly "from a handful of evenly spaced samples", but which had
// no test actually landing a sample on both sides of the boundary until now.
func TestResolveHighlights_StraddlingAPauseBoundaryIsNotPausedThroughout(t *testing.T) {
	// Paused from 10m to 11m.
	timer := syntheticTimer(30*time.Minute, [2]time.Duration{10 * time.Minute, 11 * time.Minute})

	got, err := resolveHighlights([]string{
		// 9m to 12m: a minute before the pause starts, a minute after it
		// ends. Two of this highlight's three minutes are real, unfrozen
		// activity.
		"from=9m,to=12m,name=Straddling",
	}, timer, panel.HighlightStyleBorder, panel.PausesFreeze)
	if err != nil {
		t.Fatalf("resolveHighlights: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("resolveHighlights returned %d highlights, want 1", len(got))
	}
	if got[0].PausedThroughout {
		t.Error("a highlight straddling the pause boundary was marked PausedThroughout; most of its own span is not frozen at all")
	}
}

// TestParseHighlightStyle_RefusesUnknownValues follows the precedent
// SelectLayout and SelectTheme set: a typo in --highlight-style is refused
// where it was typed, not silently rendered with the default.
func TestParseHighlightStyle_RefusesUnknownValues(t *testing.T) {
	for _, style := range []string{panel.HighlightStyleBorder, panel.HighlightStyleWash, panel.HighlightStyleNone} {
		got, err := parseHighlightStyle(style)
		if err != nil {
			t.Errorf("parseHighlightStyle(%q): %v", style, err)
		}
		if got != style {
			t.Errorf("parseHighlightStyle(%q) = %q", style, got)
		}
	}
	if _, err := parseHighlightStyle("glow"); err == nil {
		t.Error("parseHighlightStyle accepted an unknown value")
	}
}

// TestParseHighlight_ParsesBackground pins background='s hex grammar: leading
// "#" required, #RGB and #RRGGBB accepted case-insensitively, and both
// stored as an opaque color.NRGBA with HasBackground set.
func TestParseHighlight_ParsesBackground(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want color.NRGBA
	}{
		{
			name: "six-digit hex",
			in:   "from=1m,to=2m,background=#1B2A4A",
			want: color.NRGBA{R: 0x1B, G: 0x2A, B: 0x4A, A: 0xFF},
		},
		{
			name: "six-digit hex, lower case",
			in:   "from=1m,to=2m,background=#1b2a4a",
			want: color.NRGBA{R: 0x1B, G: 0x2A, B: 0x4A, A: 0xFF},
		},
		{
			name: "three-digit hex expands each digit",
			in:   "from=1m,to=2m,background=#1AF",
			want: color.NRGBA{R: 0x11, G: 0xAA, B: 0xFF, A: 0xFF},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseHighlight(c.in)
			if err != nil {
				t.Fatalf("parseHighlight(%q): %v", c.in, err)
			}
			if !got.HasBackground {
				t.Fatalf("parseHighlight(%q).HasBackground = false, want true", c.in)
			}
			if got.Background != c.want {
				t.Errorf("parseHighlight(%q).Background = %+v, want %+v", c.in, got.Background, c.want)
			}
		})
	}

	// No background= at all is legal and leaves HasBackground false -- the
	// project's own presence-flag rule, not a zero colour standing in for
	// "unset".
	got, err := parseHighlight("from=1m,to=2m")
	if err != nil {
		t.Fatalf("parseHighlight: %v", err)
	}
	if got.HasBackground {
		t.Error("no background= field set HasBackground true")
	}
	if got.Background != (color.NRGBA{}) {
		t.Errorf("no background= field left Background = %+v, want the zero value", got.Background)
	}
}

// TestParseHighlight_RejectsMalformedBackground pins the refusals a naive
// reading of "hex colour" would not think to add: no bare named colour, no
// alpha channel, no wrong-length hex.
func TestParseHighlight_RejectsMalformedBackground(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"a named colour has no table to look it up in", "from=1m,to=2m,background=blue", `must start with "#"`},
		{"no leading #", "from=1m,to=2m,background=1B2A4A", `must start with "#"`},
		{"an alpha channel is refused, not silently dropped", "from=1m,to=2m,background=#1B2A4A80", "opaque only"},
		{"the wrong digit count", "from=1m,to=2m,background=#1B2A", "not #RGB or #RRGGBB"},
		{"non-hex digits", "from=1m,to=2m,background=#GGGGGG", "not valid hex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseHighlight(c.in)
			if err == nil {
				t.Fatalf("parseHighlight(%q) accepted it", c.in)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parseHighlight(%q) error = %v, want it to mention %q", c.in, err, c.wantErr)
			}
		})
	}
}

// TestResolveHighlights_RefusesBackgroundUnderNonWashStyles pins the
// USER-APPROVED ruling: background= has no meaning outside
// --highlight-style wash, and is refused there rather than silently ignored
// (this project's characteristic bug in flag form) or auto-upgraded to wash
// (which would override a style the user may have typed on purpose).
func TestResolveHighlights_RefusesBackgroundUnderNonWashStyles(t *testing.T) {
	timer := syntheticTimer(30 * time.Minute)
	raw := []string{"from=5m,to=10m,name=Climb,background=#1B2A4A"}

	if _, err := resolveHighlights(raw, timer, panel.HighlightStyleWash, panel.PausesFreeze); err != nil {
		t.Fatalf("background= under --highlight-style wash was refused: %v", err)
	}

	for _, style := range []string{panel.HighlightStyleBorder, panel.HighlightStyleNone} {
		_, err := resolveHighlights(raw, timer, style, panel.PausesFreeze)
		if err == nil {
			t.Fatalf("background= under --highlight-style %s was accepted", style)
		}
		if !strings.Contains(err.Error(), "--highlight-style wash") {
			t.Errorf("the error should name --highlight-style wash; got: %v", err)
		}
		if style == panel.HighlightStyleNone && !strings.Contains(err.Error(), "leaves the frame itself unmarked") {
			t.Errorf("the --highlight-style none error should also explain what none itself does; got: %v", err)
		}
	}
}

// TestParseHighlight_Zoom covers the new field's three states: asked for,
// refused explicitly, and -- the one that matters most -- not mentioned.
//
// The default has to be false, and has to stay false. A render that reframed
// its map because a highlight existed would be answering a question the user
// did not ask, and the whole-course view is what a route panel is for.
func TestParseHighlight_Zoom(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "from=1m,to=2m,name=Leg", false},
		{"true", "from=1m,to=2m,zoom=true", true},
		{"false", "from=1m,to=2m,zoom=false", false},
		{"1 is true, as ParseBool reads it", "from=1m,to=2m,zoom=1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := parseHighlight(c.raw)
			if err != nil {
				t.Fatalf("parseHighlight(%q): %v", c.raw, err)
			}
			if h.Zoom != c.want {
				t.Errorf("Zoom = %v, want %v", h.Zoom, c.want)
			}
		})
	}
}

// TestParseHighlight_ZoomRejectsANonBoolean keeps a typo from being read as
// "off". zoom=yes silently parsing to false would leave a user watching a
// render that never zooms, with nothing anywhere saying why -- the same
// silent-substitution failure the repeated-key and unknown-field rules in
// this grammar already refuse.
func TestParseHighlight_ZoomRejectsANonBoolean(t *testing.T) {
	_, err := parseHighlight("from=1m,to=2m,zoom=yes")
	if err == nil {
		t.Fatal("accepted zoom=yes")
	}
	if !strings.Contains(err.Error(), "zoom") {
		t.Errorf("error should name the field; got: %v", err)
	}
}
