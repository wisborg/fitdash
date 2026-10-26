package cmd

import (
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/weather"
	"github.com/wisborg/fitactivity/workout"
)

// The description as JSON and YAML: all of the analysis, in metres,
// seconds since the activity's start, and the effort's own unit, which
// effort_unit names. What a format reader computes with is the numbers,
// not the words the text view makes of them.
type describeJSON struct {
	Title      string             `json:"title,omitempty" yaml:"title,omitempty"`
	StravaURL  string             `json:"strava_url,omitempty" yaml:"strava_url,omitempty"`
	Signal     string             `json:"signal,omitempty" yaml:"signal,omitempty"`
	EffortUnit string             `json:"effort_unit,omitempty" yaml:"effort_unit,omitempty"`
	Stops      []describeStopJ    `json:"stops" yaml:"stops"`
	Segments   []describeSegJ     `json:"segments" yaml:"segments"`
	Levels     []describeLevelJ   `json:"levels" yaml:"levels"`
	Sets       []describeSetJ     `json:"sets" yaml:"sets"`
	Climbs     []describeClimbJ   `json:"climbs" yaml:"climbs"`
	Descents   []describeDescentJ `json:"descents" yaml:"descents"`
	Weather    *describeWeatherJ  `json:"weather,omitempty" yaml:"weather,omitempty"`
}

// describeWeatherJ is the weather over the activity, with --weather: the
// wind at 10 m, from the direction it blew from.
type describeWeatherJ struct {
	TemperatureMinC float64 `json:"temperature_min_c" yaml:"temperature_min_c"`
	TemperatureMaxC float64 `json:"temperature_max_c" yaml:"temperature_max_c"`
	DewPointC       float64 `json:"dew_point_c" yaml:"dew_point_c"`
	HumidityPct     float64 `json:"humidity_pct" yaml:"humidity_pct"`
	WindSpeedMS     float64 `json:"wind_speed_ms" yaml:"wind_speed_ms"`
	WindFromDeg     float64 `json:"wind_from_deg" yaml:"wind_from_deg"`
	GustsMS         float64 `json:"gusts_ms" yaml:"gusts_ms"`
	RadiationWM2    float64 `json:"radiation_w_m2" yaml:"radiation_w_m2"`
	Source          string  `json:"source" yaml:"source"`
}

type describeSpanJ struct {
	StartS    float64  `json:"start_s" yaml:"start_s"`
	DurationS float64  `json:"duration_s" yaml:"duration_s"`
	DistanceM *float64 `json:"distance_m,omitempty" yaml:"distance_m,omitempty"`
}

type describeStopJ struct {
	describeSpanJ `yaml:",inline"`
	Paused        bool `json:"paused" yaml:"paused"`
}

type describeSegJ struct {
	describeSpanJ `yaml:",inline"`
	Effort        float64 `json:"effort" yaml:"effort"`
	// Level is a segment's level's index in levels; an interval, which
	// may span segments of more than one, has none.
	Level        *int     `json:"level,omitempty" yaml:"level,omitempty"`
	PowerW       *float64 `json:"power_w,omitempty" yaml:"power_w,omitempty"`
	SpeedMS      *float64 `json:"speed_m_s,omitempty" yaml:"speed_m_s,omitempty"`
	HeartRateBPM *float64 `json:"heart_rate_bpm,omitempty" yaml:"heart_rate_bpm,omitempty"`
}

type describeLevelJ struct {
	Effort    float64 `json:"effort" yaml:"effort"`
	DurationS float64 `json:"duration_s" yaml:"duration_s"`
}

type describeSetJ struct {
	Intervals  []describeSegJ  `json:"intervals" yaml:"intervals"`
	Recoveries []describeSpanJ `json:"recoveries" yaml:"recoveries"`
}

// describeDescentJ is a descent; its grade is downhill, and positive.
type describeDescentJ struct {
	describeSpanJ  `yaml:",inline"`
	StartDistanceM float64 `json:"start_distance_m" yaml:"start_distance_m"`
	LossM          float64 `json:"loss_m" yaml:"loss_m"`
	Grade          float64 `json:"grade" yaml:"grade"`
}

type describeClimbJ struct {
	describeSpanJ  `yaml:",inline"`
	StartDistanceM float64 `json:"start_distance_m" yaml:"start_distance_m"`
	GainM          float64 `json:"gain_m" yaml:"gain_m"`
	Grade          float64 `json:"grade" yaml:"grade"`
}

func (d describer) json() describeJSON {
	start, _ := fitactivity.BuildTimerModel(d.track).Window()
	span := func(s workout.Span) describeSpanJ {
		j := describeSpanJ{StartS: s.Start.Sub(start).Seconds(), DurationS: s.Duration().Seconds()}
		if m, ok := s.Distance(); ok {
			j.DistanceM = &m
		}
		return j
	}
	opt := func(v float64, ok bool) *float64 {
		if !ok {
			return nil
		}
		return &v
	}
	j := describeJSON{Signal: string(d.a.Signal), EffortUnit: effortUnit(d.a.Signal),
		Stops: []describeStopJ{}, Segments: []describeSegJ{}, Levels: []describeLevelJ{}, Sets: []describeSetJ{}, Climbs: []describeClimbJ{},
		Descents: []describeDescentJ{}, Title: d.track.Title}
	if d.track.Strava != nil {
		j.StravaURL = d.track.Strava.URL
	}
	if w := d.weather; w != nil {
		j.Weather = &describeWeatherJ{TemperatureMinC: w.TemperatureMin, TemperatureMaxC: w.TemperatureMax,
			DewPointC: w.DewPoint, HumidityPct: w.Humidity, WindSpeedMS: w.WindSpeed, WindFromDeg: w.WindFrom,
			GustsMS: w.Gusts, RadiationWM2: w.Radiation, Source: weather.Credit + " (CC BY 4.0, " + weather.CreditURL + ")"}
	}
	for _, s := range d.a.Stops {
		j.Stops = append(j.Stops, describeStopJ{describeSpanJ: span(s.Span), Paused: s.Paused})
	}
	for _, s := range d.a.Segments {
		level := s.Level
		j.Segments = append(j.Segments, describeSegJ{describeSpanJ: span(s.Span), Effort: s.Effort, Level: &level,
			PowerW: opt(s.Power, s.HasPower), SpeedMS: opt(s.Speed, s.HasSpeed), HeartRateBPM: opt(s.HeartRate, s.HasHeartRate)})
	}
	for _, l := range d.a.Levels {
		j.Levels = append(j.Levels, describeLevelJ{Effort: l.Effort, DurationS: l.Duration.Seconds()})
	}
	for _, s := range d.a.Sets {
		set := describeSetJ{Recoveries: []describeSpanJ{}}
		for _, iv := range s.Intervals {
			set.Intervals = append(set.Intervals, describeSegJ{describeSpanJ: span(iv.Span), Effort: iv.Effort,
				PowerW: opt(iv.Power, iv.HasPower), SpeedMS: opt(iv.Speed, iv.HasSpeed), HeartRateBPM: opt(iv.HeartRate, iv.HasHeartRate)})
		}
		for _, r := range s.Recoveries {
			set.Recoveries = append(set.Recoveries, span(r))
		}
		j.Sets = append(j.Sets, set)
	}
	for _, c := range d.a.Climbs {
		j.Climbs = append(j.Climbs, describeClimbJ{describeSpanJ: span(c.Span), StartDistanceM: c.StartDistance, GainM: c.Gain, Grade: c.Grade})
	}
	for _, c := range d.a.Descents {
		j.Descents = append(j.Descents, describeDescentJ{describeSpanJ: span(c.Span), StartDistanceM: c.StartDistance, LossM: c.Loss, Grade: c.Grade})
	}
	return j
}

// effortUnit names the unit an effort by sig is in.
func effortUnit(sig workout.Signal) string {
	switch sig {
	case workout.SignalPower:
		return "W"
	case workout.SignalGradeAdjustedSpeed, workout.SignalSpeed:
		return "m/s"
	case workout.SignalHeartRate:
		return "bpm"
	}
	return ""
}
