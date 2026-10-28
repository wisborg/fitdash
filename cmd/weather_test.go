package cmd

import (
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/effort"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// The line itself, the costs and the flags are cobrakit's weather's, and
// tested there; the cards are fitdash's own.

// The cards' line is the summary's, with the credit beside it; without
// weather there is none.
func TestWeatherCardLine(t *testing.T) {
	m, _ := units.Of(units.Metric)
	t0 := time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC)
	tr := &fitactivity.Track{}
	for i := 0; i <= 600; i++ {
		tr.Samples = append(tr.Samples, fitactivity.Sample{Time: t0.Add(time.Duration(i) * time.Second), HasGPS: true, Lat: 10, Lon: 20})
	}
	hours := []weather.Conditions{
		{Time: t0, Temperature: 10, DewPoint: 5, WindSpeed: 3, WindFrom: 180},
		{Time: t0.Add(time.Hour), Temperature: 10, DewPoint: 5, WindSpeed: 3, WindFrom: 180},
	}
	w := panel.NewWeather(tr, &weather.Report{Places: []weather.Place{{Lat: 10, Lon: 20, Hours: hours}}}, effort.Suburban)
	if got, ok := weatherCardLine(w, tr, m); !ok || got != "10°C, dew point 5°C, wind 11 km/h from S · Open-Meteo.com" {
		t.Errorf("%q, %v", got, ok)
	}
	if _, ok := weatherCardLine(nil, tr, m); ok {
		t.Error("a line without weather")
	}
}
