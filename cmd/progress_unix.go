//go:build !windows

package cmd

import (
	"io"
	"syscall"
	"unsafe"
)

// terminalSize returns w's columns and rows when w is a terminal.
func terminalSize(w io.Writer) (cols, rows int, ok bool) {
	f, isTTY := isTerminal(w)
	if !isTTY {
		return 0, 0, false
	}
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}
