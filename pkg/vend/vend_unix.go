//go:build !windows

package vend

import (
	"os/exec"
	"syscall"
)

// setpgid puts cmd in its own process group, so killGroup can later signal
// the whole tree (the aws CLI plus any credential_process grandchild) rather
// than only the direct child. Vend calls this only for a non-interactive
// profile: a background process group that reads its controlling terminal
// gets SIGTTIN and stops, which silently kills MFA prompting. See Vend's doc
// comment.
func setpgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup sends SIGKILL to cmd's whole process group. Falls back to
// killing just the direct process if the group can't be resolved (e.g. the
// process never started).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
