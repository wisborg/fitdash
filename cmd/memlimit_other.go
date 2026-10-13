//go:build !darwin && !linux

package cmd

// physicalMemory is 0, unknown, where there is no simple way to ask; see
// limitMemory.
func physicalMemory() uint64 { return 0 }
