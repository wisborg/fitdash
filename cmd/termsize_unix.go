//go:build darwin || linux

package cmd

import (
	"os"
	"syscall"
	"unsafe"
)

// terminalSize is the terminal f writes to, in characters, and whether f is
// one: asked of the terminal itself, so a window resized since the shell
// started is measured as it is now, which COLUMNS would not.
func terminalSize(f *os.File) (cols, rows int, ok bool) {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), ws.Col > 0 && ws.Row > 0
}
