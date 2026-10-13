package cmd

import (
	"runtime"
	"testing"
)

// The machine's memory reads as a plausible amount where it can be read: a
// byte dropped or read the wrong way round is off by powers of 256, and
// would set a limit of nothing or of everything.
func TestPhysicalMemoryIsPlausible(t *testing.T) {
	got := physicalMemory()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if got != 0 {
			t.Errorf("physicalMemory on %s is %d, want 0 (unknown)", runtime.GOOS, got)
		}
		return
	}
	if got < 512<<20 || got > 64<<40 {
		t.Errorf("physicalMemory is %d bytes, not a machine's memory", got)
	}
}
