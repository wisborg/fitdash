package cmd

import (
	"os"
	"runtime/debug"
)

// limitMemory sets Go's soft memory limit to half the machine's memory,
// unless GOMEMLIMIT says otherwise or the machine's memory is not known.
//
// A flyover holds a few gigabytes it means to -- the map tiles drawn, the
// vector tiles decoded, the frames being drawn -- and makes garbage fast,
// and Go's collector lets the heap grow to twice what is live before it
// collects. On a 16 GB Mac a full-frame flight's render took 13 GB that
// way, the machine compressed and swapped, and five of eight cores were
// busy. With the limit the same render held 9 GB and finished a sixth
// sooner, for no more work collecting: the collector works harder only
// near the limit, and a render there is better off collecting than
// swapping.
//
// It is a soft limit: a render that needs more than half the machine gets
// it, collecting as it goes, and fails for want of memory no sooner than
// it would have.
func limitMemory() {
	if _, set := os.LookupEnv("GOMEMLIMIT"); set {
		return
	}
	if total := physicalMemory(); total > 0 {
		debug.SetMemoryLimit(int64(total / 2))
	}
}
