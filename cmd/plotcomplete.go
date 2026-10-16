package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity/units"
)

// Completing plot's flags. --y is completed from the activity itself: the
// metrics it carries, each described by its unit and how much of the
// activity carries it, as `fitdash plot ACTIVITY` lists them -- so tab
// offers only what can be charted, and a metric the file lacks is never
// suggested. The flags with a fixed set of values are completed from it.
func init() {
	_ = plotCmd.RegisterFlagCompletionFunc("y", completePlotY)
	_ = plotCmd.RegisterFlagCompletionFunc("shade", completePlotShade)
	fixed := map[string][]string{
		"x":            {"time\telapsed time, pauses included", "distance\tthe distance covered"},
		"terminal":     {termAuto + "\ta picture where the terminal shows one, braille otherwise", termITerm + "\tiTerm2's picture, which WezTerm reads too", termKitty + "\tkitty's picture, which Ghostty reads too", termBraille + "\tbraille dots"},
		"theme":        {themeAuto + "\tlight for a PNG, the terminal's own in the terminal", "light", "dark", "night"},
		"power-source": {"auto\tStryd's where the activity has it, the file's own otherwise", "stryd\tthe footpod's", "native\tthe file's own power field"},
		"units":        {string(units.Metric) + "\tkm, m, km/h, min/km", string(units.Imperial) + "\tmi, ft, mph, min/mi"},
	}
	for flag, values := range fixed {
		_ = plotCmd.RegisterFlagCompletionFunc(flag, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
	}
}

// completePlotY offers the metrics the activities named so far carry, for
// the metric being typed after any already chosen: "power,he" completes to
// "power,heart-rate". A metric already chosen is not offered again, and
// with no activity named yet there is nothing to offer.
func completePlotY(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	track, _, _, metrics, err := plotActivity(cmd, args)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	chosen := map[string]bool{}
	for _, name := range plotOpts.y {
		chosen[name] = true
	}
	prefix := ""
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix = toComplete[:i+1]
		for _, name := range strings.Split(toComplete[:i], ",") {
			chosen[strings.TrimSpace(name)] = true
		}
	}
	var out []string
	for _, m := range metrics {
		c := m.coverage(track)
		if c == 0 || chosen[m.name] {
			continue
		}
		desc := fmt.Sprintf("%.0f%%", 100*c)
		if u := m.unitFrom(); u != "" {
			desc = u + ", " + desc
		}
		out = append(out, prefix+m.name+"\t"+desc)
	}
	// NoSpace after a choice, so another can follow its comma; the shell
	// filters by what is typed.
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// completePlotShade offers the kinds of shade not yet chosen, after any
// already chosen: "intervals," completes to "intervals,climbs" and
// "intervals,descents".
func completePlotShade(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix, chosen := "", map[string]bool{}
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix = toComplete[:i+1]
		for _, name := range strings.Split(toComplete[:i], ",") {
			chosen[strings.TrimSpace(name)] = true
		}
	}
	var out []string
	for _, k := range [][2]string{{shadeIntervals, "each interval of a set"}, {shadeClimbs, "each climb"}, {shadeDescents, "each descent"}} {
		if !chosen[k[0]] {
			out = append(out, prefix+k[0]+"\t"+k[1])
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
