//go:build darwin

package vend

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Darwin's pty-unlock triad is three ioctls on the master fd — not the
// grantpt(3)/unlockpt(3)/ptsname(3) libc calls (which would need cgo) and not
// Linux's TIOCGPTN/TIOCSPTLCK pair. Values confirmed against the real
// <sys/ttycom.h> on this machine's SDK (Xcode command line tools), via:
//
//	#include <sys/ioctl.h>
//	#include <sys/ttycom.h>
//	printf("%#x %#x %#x\n", TIOCPTYGRANT, TIOCPTYUNLK, TIOCPTYGNAME);
//
// -> 0x20007454 0x20007452 0x40807453
const (
	tiocptygrant = 0x20007454
	tiocptyunlk  = 0x20007452
	tiocptygname = 0x40807453
)

// openPTY opens a fresh pseudo-terminal master/slave pair using only the
// stdlib (syscall + unsafe), so the pty regression test in
// pty_unix_test.go needs no third-party dependency. No cgo, so no
// grantpt/unlockpt/ptsname libc calls — their ioctl equivalents directly.
func openPTY() (master *os.File, slavePath string, err error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocptygrant, 0); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("TIOCPTYGRANT: %w", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocptyunlk, 0); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("TIOCPTYUNLK: %w", errno)
	}
	var buf [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocptygname, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("TIOCPTYGNAME: %w", errno)
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), string(buf[:n]), nil
}
