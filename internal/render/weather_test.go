package render

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// The weather panel is placed with --weather's answer and left out,
// unreported, without it: not missing from the activity, not asked for.
func TestNew_WeatherPanelOnlyWithWeather(t *testing.T) {
	for _, l := range []panel.Layout{panel.LandscapeLayout(), panel.PortraitLayout()} {
		ctx := buildContext(t, shortOptions(), 640, 360, 10)
		r, err := New(ctx, l, panel.DefaultTheme())
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(names(r), "weather") || slices.Contains(r.Declined(), "weather") {
			t.Errorf("%s without --weather: placed %v, declined %v", l.Name, names(r), r.Declined())
		}

		// Invented weather at the start, a westerly every hour of it.
		start := ctx.Track.Samples[0]
		var hours []weather.Conditions
		for h := -1; h <= 3; h++ {
			hours = append(hours, weather.Conditions{Time: start.Time.Truncate(time.Hour).Add(time.Duration(h) * time.Hour),
				Temperature: 12, DewPoint: 6, WindSpeed: 5, WindFrom: 270})
		}
		report := &weather.Report{Places: []weather.Place{{Lat: start.Lat, Lon: start.Lon, Hours: hours}}}
		ctx.Weather = panel.NewWeather(ctx.Track, report)
		if r, err = New(ctx, l, panel.DefaultTheme()); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(names(r), "weather") {
			t.Errorf("%s with --weather: placed %v", l.Name, names(r))
		}
		for _, i := range []int{0, r.Frames() / 2} {
			if err := r.Render(image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height)), r.Frame(i)); err != nil {
				t.Fatalf("%s frame %d: %v", l.Name, i, err)
			}
		}
	}
}

func names(r *Renderer) []string {
	var n []string
	for _, p := range r.Placed() {
		n = append(n, p.Panel.Name())
	}
	return n
}
