package cmd

import (
	"encoding/binary"
	"syscall"
)

// physicalMemory is the machine's memory in bytes, or 0 if it cannot be
// read.
func physicalMemory() uint64 {
	s, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0
	}
	// The value is a little-endian uint64; Sysctl drops a trailing zero
	// byte, which in a number is its top byte, so it is put back.
	b := []byte(s)
	for len(b) < 8 {
		b = append(b, 0)
	}
	return binary.LittleEndian.Uint64(b[:8])
}
