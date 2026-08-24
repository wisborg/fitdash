// Package cmd is fitdash's command-line surface.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/wisborg/output"
)

// format is the --format flag's value, shared by every subcommand that prints
// a result. output.Format refuses an unknown name where the user typed it
// rather than quietly falling back to text -- a program that prints a table
// when its caller asked for JSON has produced output that cannot be parsed and
// no message saying why.
var format = formatFlag{Format: output.Text}

// formatFlag adapts output.Format to pflag. output.Format satisfies the
// standard library's flag.Value (String and Set) but not pflag's, which adds
// Type() for the name shown in help text. The wrapper is three lines and keeps
// the knowledge of which flag package this CLI uses out of the output library,
// which is the right place for it to not be.
type formatFlag struct{ output.Format }

func (formatFlag) Type() string { return "format" }

var root = &cobra.Command{
	Use:   "fitdash",
	Short: "Render a recorded exercise as a dashboard video",
	Long: `fitdash reads a Garmin FIT activity and renders it as a dashboard video:
the metrics animate as the activity progresses.

A rendered dashboard is a video of where you were, minute by minute, and what
your body was doing. It is yours to publish or not -- fitdash writes it to a
file and does nothing else with it.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI, reporting any error on stderr and exiting non-zero.
//
// Cobra's own error printing is suppressed (SilenceErrors) so a failure is
// reported once, here, in one shape -- and SilenceUsage keeps a runtime failure
// from dumping the whole usage text after it, which buries the message that
// actually says what went wrong.
func Execute() {
	root.PersistentFlags().Var(&format, "format", "output format: text, csv, json or yaml")
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fitdash: %v\n", err)
		os.Exit(1)
	}
}
