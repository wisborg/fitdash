package panel

import (
	"fmt"
	"strings"
)

// --speed-readout's legal values, and the two a render resolves to.
const (
	// SpeedReadoutAuto shows pace for a sport read in pace and speed for
	// every other: see ResolveSpeedReadout.
	SpeedReadoutAuto = "auto"
	// SpeedReadoutPace shows speed as time per distance, the way a runner
	// reads it.
	SpeedReadoutPace = "pace"
	// SpeedReadoutSpeed shows speed as distance per hour, the way a cyclist,
	// a sailor or a pilot reads it.
	SpeedReadoutSpeed = "speed"
)

// SpeedReadouts are --speed-readout's legal values, in the order its help
// lists them.
var SpeedReadouts = []string{SpeedReadoutAuto, SpeedReadoutPace, SpeedReadoutSpeed}

// ResolveSpeedReadout is which readout --speed-readout mode asks for on an
// activity of sport: pace or speed as given, or for auto, pace for running,
// walking and hiking and speed for everything else.
//
// The sports are those fitactivity.CadenceUnit counts in steps, and an
// unknown sport -- a GPX or KML file names none -- gets speed for the reason
// it gets revolutions there: speed is what was recorded, and shown as
// recorded it cannot be the wrong reading, where a pace on a flight or a
// ride would be. A run from a file with no sport shows speed; --speed-readout
// pace is the answer to that, and the summary says which was chosen.
func ResolveSpeedReadout(mode, sport string) (string, error) {
	switch mode {
	case SpeedReadoutPace, SpeedReadoutSpeed:
		return mode, nil
	case SpeedReadoutAuto, "":
		switch strings.ToLower(sport) {
		case "running", "walking", "hiking":
			return SpeedReadoutPace, nil
		}
		return SpeedReadoutSpeed, nil
	}
	return "", fmt.Errorf("--speed-readout %q is invalid; use %s", mode, strings.Join(SpeedReadouts, ", "))
}

// SpeedReadoutKeeps reports whether p survives ctx.SpeedReadout: false only
// for whichever of the gauge column's Pace and Speed was not chosen. Every
// other panel, and the balance column's own pace -- kept beside running
// dynamics, which only a run records -- is untouched.
func SpeedReadoutKeeps(ctx *Context, p Panel) bool {
	if IsBalancePanel(p) {
		return true
	}
	chosen := ctx.SpeedReadout
	if chosen == "" {
		chosen = SpeedReadoutPace
	}
	switch p.Name() {
	case paceName:
		return chosen == SpeedReadoutPace
	case speedName:
		return chosen == SpeedReadoutSpeed
	}
	return true
}
