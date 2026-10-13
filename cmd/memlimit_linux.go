package cmd

import "syscall"

// physicalMemory is the machine's memory in bytes, or 0 if it cannot be
// read.
func physicalMemory() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}
	return uint64(info.Totalram) * uint64(info.Unit)
}
