package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/effort"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// The line a card and the summary give, in the render's units.
func TestWeatherLine(t *testing.T) {
	m, _ := units.Of(units.Metric)
	s := weather.Summary{TemperatureMin: 14.2, TemperatureMax: 16.4, DewPoint: 9, WindSpeed: 4.2, WindFrom: 250, Gusts: 9}
	if got, want := weatherLine(s, m), "14–16°C, dew point 9°C, wind 15 km/h from WSW, gusts 32"; got != want {
		t.Errorf("metric: %q, want %q", got, want)
	}
	imp, _ := units.Of(units.Imperial)
	s = weather.Summary{TemperatureMin: 20, TemperatureMax: 20.2, DewPoint: 10, WindSpeed: 0.1}
	if got := weatherLine(s, imp); !strings.HasPrefix(got, "68°F,") || !strings.HasSuffix(got, "calm") {
		t.Errorf("imperial: %q", got)
	}
}

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

func TestFormatCost(t *testing.T) {
	for d, want := range map[time.Duration]string{5 * time.Second: "5 s", 72 * time.Second: "1 min 12 s", 14*time.Minute + 20*time.Second: "14 min"} {
		if got := formatCost(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

// Each cost is said with what it rests on; one the athlete was
// acclimatised to is left out and said to be; an altitude below where
// oxygen uptake falls is not mentioned.
func TestWriteCostsNote(t *testing.T) {
	c := conditionCosts{exposure: effort.Suburban, wind: 72 * time.Second, hasWind: true,
		heat: 3*time.Minute + 20*time.Second, wbgtLo: 21.2, wbgtHi: 23.6, hasHeat: true,
		altitude: 0, highest: 120, hasAltitude: true}
	var b strings.Builder
	writeCostsNote(&b, c, true)
	for _, want := range []string{"the wind cost about 1 min 12 s", "suburban exposure (0.38", "the heat cost about 3 min 20 s, at WBGT 21–24 °C in the shade"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "altitude") {
		t.Errorf("an altitude of 120 m mentioned:\n%s", b.String())
	}

	c.acclim, c.highest, c.altitude = acclimatisation{heat: true}, 1800, 4*time.Minute
	b.Reset()
	writeCostsNote(&b, c, true)
	for _, want := range []string{"the heat (WBGT 21–24 °C in the shade) is left out: you were acclimatised", "the altitude cost about 4 min, up to 1800 m, where 9.4% of oxygen uptake"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
	if _, err := parseAcclimatised([]string{"heat", "humidity"}); err == nil {
		t.Error("humidity accepted")
	}
}
