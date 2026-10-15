//go:build !darwin && !linux

package cmd

import "os"

// terminalSize is not asked of the terminal here; the caller falls back to
// COLUMNS and LINES, and then to its own defaults, and draws in braille.
func terminalSize(*os.File) (termSize, bool) { return termSize{}, false }
