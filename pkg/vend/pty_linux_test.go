//go:build linux

package vend

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// TIOCGPTN/TIOCSPTLCK are Linux's glibc-free equivalents of ptsname(3)/
// unlockpt(3): stable, well-known ioctl numbers from
// include/uapi/asm-generic/ioctls.h, unchanged for decades across
// architectures and kernel versions.
const (
	tiocgptn   = 0x80045430
	tiocsptlck = 0x40045431
)

// openPTY opens a fresh pseudo-terminal master/slave pair using only the
// stdlib (syscall + unsafe), so the pty regression test in
// pty_unix_test.go needs no third-party dependency (no cgo, so no
// grantpt/unlockpt/ptsname libc calls — their ioctl equivalents directly).
func openPTY() (master *os.File, slavePath string, err error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	var unlock int32 // 0 unlocks the slave
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("TIOCSPTLCK: %w", errno)
	}
	var n int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocgptn, uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("TIOCGPTN: %w", errno)
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), "/dev/pts/" + strconv.Itoa(int(n)), nil
}
