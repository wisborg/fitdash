package panel

import (
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
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
	w := NewWeather(tr, &weather.Report{})
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
