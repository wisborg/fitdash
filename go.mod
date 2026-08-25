module github.com/wisborg/fitdash

go 1.25.0

require (
	github.com/spf13/cobra v1.8.1
	github.com/wisborg/fitactivity v0.1.0
	github.com/wisborg/output v0.1.0
)

require (
	github.com/clipperhouse/uax29/v2 v2.2.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-runewidth v0.0.28 // indirect
	github.com/muktihari/fit v0.28.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

// output IS published, but its v0.1.0 tag contains only the `table`
// subpackage; the root package (Document, Format) that this program uses for
// --format lives in the working tree at ../output and has no tag yet. Without
// the replacement the build fails with "found (v0.1.0), but does not contain
// package github.com/wisborg/output", which reads like a typo rather than a
// missing tag. It comes out when there is a tag carrying the root package.
//
// fitactivity needs no replacement: it is published and tagged. To work on it
// and this project at once, add one temporarily and take it out before
// committing.
replace github.com/wisborg/output => ../output
