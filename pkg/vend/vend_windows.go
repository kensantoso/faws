//go:build windows

package vend

import "os/exec"

// setpgid is a no-op on Windows: there is no POSIX process-group concept to
// join. A timed-out credential_process's grandchildren are not reaped by
// this build; that is a pre-existing platform gap, not a regression.
func setpgid(cmd *exec.Cmd) {}

// killGroup falls back to killing just the direct process on Windows.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
