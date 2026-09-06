package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	const rev = "49133d80a4e4c0ffee1234567890abcdef123456"
	for _, c := range []struct {
		name string
		main string
		rev  string
		dirt bool
		want string
		why  string
	}{{
		name: "released build names its tag",
		main: "v1.4.0", rev: rev,
		want: "v1.4.0 (49133d80a4e4)",
		why:  "a real tag says something the revision does not, so it survives",
	}, {
		name: "pseudo-version collapses to devel",
		main: "v0.0.0-20260906024537-49133d80a4e4", rev: rev,
		want: "devel (49133d80a4e4)",
		why:  "the pseudo-version only restates the revision printed beside it",
	}, {
		name: "dirty pseudo-version collapses and still says dirty",
		main: "v0.0.0-20260906024537-49133d80a4e4+dirty", rev: rev, dirt: true,
		want: "devel (49133d80a4e4, dirty)",
		why:  "a dirty build is not the commit it names and must say so once",
	}, {
		name: "dirty at a tag says dirty once, not twice",
		main: "v0.1.0+dirty", rev: rev, dirt: true,
		want: "v0.1.0 (49133d80a4e4, dirty)",
		why:  "+dirty is build metadata repeating what the dirty flag already reports",
	}, {
		name: "checkout with no module version",
		main: "(devel)", rev: rev,
		want: "devel (49133d80a4e4)",
	}, {
		name: "dirty with no revision still says dirty",
		main: "(devel)", dirt: true,
		want: "devel (dirty)",
		why:  "a stamped modification must never pass for a clean build",
	}, {
		name: "no vcs stamp at all",
		main: "v2.0.1",
		want: "v2.0.1",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got := formatVersion(c.main, c.rev, "", "", c.dirt)
			if got != c.want {
				t.Errorf("formatVersion(%q, rev=%t, dirty=%t) = %q, want %q\n%s",
					c.main, c.rev != "", c.dirt, got, c.want, c.why)
			}
		})
	}
}

// TestFormatVersion_AlwaysNamesTheBuildAndTheToolchain keeps the two trailing
// facts from being dropped as noise. They are what a bug report needs and
// cannot be recovered from a binary afterwards.
func TestFormatVersion_AlwaysNamesTheBuildAndTheToolchain(t *testing.T) {
	got := formatVersion("(devel)", "abc123def456789", "2026-09-06T02:45:37Z", "go1.26.5", false)
	for _, want := range []string{"2026-09-06T02:45:37Z", "go1.26.5", "abc123def456"} {
		if !strings.Contains(got, want) {
			t.Errorf("version %q does not report %q", got, want)
		}
	}
}

// TestVersionFlag_NeedsNoActivity pins cobra's ordering rather than this
// project's own code, deliberately.
//
// The root command IS the render and declares ExactArgs(1), so --version
// works only because cobra answers it before it validates arguments. That is
// cobra's behaviour, not a guarantee this package makes, and a version bump
// that reordered the two would turn `fitdash --version` into a complaint
// about a missing activity file -- which is the kind of breakage nobody
// thinks to check after a dependency update.
func TestVersionFlag_NeedsNoActivity(t *testing.T) {
	if root.Version == "" {
		t.Fatal("root.Version is empty, so cobra adds no --version flag at all")
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--version"})
	t.Cleanup(func() { root.SetArgs(nil) })

	if err := root.Execute(); err != nil {
		t.Fatalf("fitdash --version: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "fitdash version ") {
		t.Errorf("--version printed %q, want it to name the program and its version", got)
	}
}
