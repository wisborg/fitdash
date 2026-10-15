package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// complete runs the shell's completion request for args, as the completion
// script does, and returns the offers, without the trailing directive.
func complete(t *testing.T, args ...string) []string {
	t.Helper()
	// Flags given in one request stay set on plotCmd for the next, as they
	// do in runPlotCmd; every one is put back to its default first.
	plotCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, list := f.Value.(pflag.SliceValue); !list {
			if err := f.Value.Set(f.DefValue); err != nil {
				t.Fatalf("restoring the default for --%s: %v", f.Name, err)
			}
		}
		f.Changed = false
	})
	plotOpts.y, plotOpts.unitEach, plotOpts.fonts = nil, nil, nil
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"__complete"}, args...))
	defer root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var offers []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(l, ":") {
			offers = append(offers, l)
		}
	}
	return offers
}

// --y offers the metrics the activity carries -- each with its unit, its
// source where it has one, and its coverage -- and not those it lacks.
func TestCompletePlotY(t *testing.T) {
	path := writePlotActivity(t)
	offers := strings.Join(complete(t, "plot", path, "--y", ""), "\n")
	for _, want := range []string{"heart-rate\tbpm, 100%", "power\tW, Stryd, 100%", "Power\t"} {
		if !strings.Contains(offers, want) {
			t.Errorf("no offer %q in:\n%s", want, offers)
		}
	}
	if strings.Contains(offers, "temperature") {
		t.Errorf("temperature offered, which the activity lacks:\n%s", offers)
	}
	if offers := strings.Join(complete(t, "plot", path, "--power-source", "native", "-y", ""), "\n"); !strings.Contains(offers, "power\tW, native, 50%") {
		t.Errorf("under --power-source native, power not offered as the watch's half:\n%s", offers)
	}
}

// After a comma, the metrics are offered after the ones already chosen,
// which are not offered again.
func TestCompletePlotYAfterAComma(t *testing.T) {
	offers := complete(t, "plot", writePlotActivity(t), "--y", "power,he")
	if len(offers) == 0 {
		t.Fatal("nothing offered after power,")
	}
	for _, o := range offers {
		if !strings.HasPrefix(o, "power,") || strings.HasPrefix(o, "power,power\t") {
			t.Errorf("offer %q: want the next metric after power, and not power again", o)
		}
	}
}

// Without an activity there is nothing to offer for --y; the flags with a
// fixed set of values offer it.
func TestCompletePlotFlags(t *testing.T) {
	if offers := complete(t, "plot", "--y", ""); len(offers) != 0 {
		t.Errorf("--y with no activity offered %q", offers)
	}
	for flag, want := range map[string]string{"--x": "distance", "--terminal": "kitty", "--power-source": "stryd", "--theme": "night"} {
		if offers := strings.Join(complete(t, "plot", flag, ""), "\n"); !strings.Contains(offers, want) {
			t.Errorf("%s offered %q, without %s", flag, offers, want)
		}
	}
}
