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
	Use:   "fitdash ACTIVITY.fit",
	Short: "Render a recorded exercise as a dashboard video",
	Long: `fitdash reads a Garmin FIT activity and renders it as a dashboard video:
the metrics animate as the activity progresses.

The video spans the activity's ELAPSED time, so it freezes through a pause
rather than cutting it out. Cutting pauses would splice two instants together
and teleport the dashboard across whatever ground was covered while the watch
was stopped.

A rendered dashboard is a video of where you were, minute by minute, and what
your body was doing. It is yours to publish or not -- fitdash writes it to a
file, writes no location metadata into it, and does nothing else with it.

Note that an activity file named exactly like a subcommand -- "inspect" -- is
resolved as the subcommand. Write ./inspect to render it.`,
	// Setting Version is what makes cobra add --version at all, and it lives
	// here rather than in Execute so the command is fully described by its
	// own declaration -- a test, or anything else that reaches for root
	// without going through Execute, gets the same command the user does.
	//
	// Cobra answers --version BEFORE it validates Args, which is what makes
	// the flag usable at all on a root command declaring ExactArgs(1): the
	// render needs an activity and `fitdash --version` has none.
	// TestVersionFlag_NeedsNoActivity pins that ordering, because it is
	// cobra's behaviour rather than a guarantee this package makes.
	Version:       version(),
	Args:          cobra.ExactArgs(1),
	RunE:          runRender,
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
	// cobra creates the `completion` command lazily, during Execute, so it
	// is asked for explicitly here in order to hang `install` beneath it.
	// The alternative -- a top-level command of our own -- would put the
	// install step somewhere nobody looks: a person who wants completion
	// types `fitdash completion` first, and should find the whole story
	// there rather than two commands that do not mention each other.
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.AddCommand(newCompletionInstallCmd(root))
			break
		}
	}

	root.PersistentFlags().Var(&format, "format", "output format: text, csv, json or yaml")
	bindRenderFlags(root)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fitdash: %v\n", err)
		os.Exit(1)
	}
}
