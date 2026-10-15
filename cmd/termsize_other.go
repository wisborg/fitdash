//go:build !darwin && !linux

package cmd

import "os"

// terminalSize is not asked of the terminal here; the caller falls back to
// COLUMNS and LINES, and then to its own defaults.
func terminalSize(*os.File) (cols, rows int, ok bool) { return 0, 0, false }
