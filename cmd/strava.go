package cmd

import (
	"github.com/spf13/cobra"

	"github.com/wisborg/cobrakit/strava"
	"github.com/wisborg/fitactivity"
)

// stravaAttach is --strava: give a file Strava's details from the athlete's
// activity that started when it did.
var stravaAttach bool

// addStrava gives fitdash "fitdash strava" -- login, logout, status and
// list, from cobrakit -- and --strava on every command that reads an
// activity, beside cobrakit's --strava-config and --strava-refresh.
func addStrava(root *cobra.Command) {
	strava.Add(root)
	root.PersistentFlags().BoolVar(&stravaAttach, "strava", false,
		"give a file Strava's title, from your activity there that started when it did; see \"fitdash strava\"")
}

// readActivity reads one of a command's activities: a file as fitactivity
// reads it, and strava:ID -- or the activity's page -- from your own
// activities on Strava. Strava is asked only then, or with --strava, so a
// file alone never needs an application set up.
func readActivity(cmd *cobra.Command) func(string) (*fitactivity.Track, error) {
	return strava.Loader(cmd, fitactivity.Read, strava.Fetch{Attach: stravaAttach})
}
