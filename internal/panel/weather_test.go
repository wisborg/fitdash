package panel

import (
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/effort"
	"github.com/wisborg/fitactivity/weather"
)

// A stop keeps the heading the athlete stopped with: the nearest sample's
// that has one.
func TestWeather_HeadingAtAStop(t *testing.T) {
	t0 := time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC)
	tr := &fitactivity.Track{}
	for i := 0; i < 60; i++ {
		lat := 10 + float64(min(i, 30))*3/111195 // north for 30 s, then still
		tr.Samples = append(tr.Samples, fitactivity.Sample{Time: t0.Add(time.Duration(i) * time.Second), HasGPS: true, Lat: lat, Lon: 20})
	}
	w := NewWeather(tr, &weather.Report{}, effort.Suburban)
	for _, at := range []time.Duration{10 * time.Second, 50 * time.Second} {
		h, ok := w.HeadingAt(t0.Add(at))
		if !ok || h > 0.01 && h < 359.99 {
			t.Errorf("at %v: %v, %v", at, h, ok)
		}
	}
	if (WeatherPanel{}).Accepts(&Context{}) || !(WeatherPanel{}).Accepts(&Context{Weather: w}) {
		t.Error("Accepts is not whether there is weather")
	}
}

// Into a headwind the panel says what it costs on the pace, behind it
// what it gives, and a crosswind nothing.
func TestWeatherPainter_Cost(t *testing.T) {
	p := &weatherPainter{units: (&Context{}).units()}
	s := fitactivity.Sample{HasSpeed: true, Speed: 3}
	c := weather.Conditions{Temperature: 15, Pressure: 1013.25}
	head, tail := p.cost(s, c, 3), p.cost(s, c, -3)
	if !strings.HasPrefix(head, "+") || !strings.HasSuffix(head, " s/km") || !strings.HasPrefix(tail, "-") {
		t.Errorf("head %q, tail %q", head, tail)
	}
	if got := p.cost(s, c, 0.05); got != "" {
		t.Errorf("near calm: %q", got)
	}
}
