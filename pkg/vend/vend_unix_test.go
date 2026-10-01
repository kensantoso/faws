//go:build !windows

package vend

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestVendKillsOrphanedGrandchildOnTimeout is the regression guard for FIX 3:
// exec.CommandContext alone kills only the direct "aws" process on timeout,
// leaving a credential_process grandchild running under PPID 1 (confirmed
// externally with a real sh -c / sleep pair). Vend now puts the child in its
// own process group (setpgid) and kills the whole group on timeout
// (killGroup), so the grandchild must not survive.
//
// The fake "aws" here plays the same role: it backgrounds a "sh -c" child
// that itself runs "sleep 30" — a stand-in for a credential_process
// grandchild — then blocks itself, forcing Vend's short timeout to fire.
func TestVendKillsOrphanedGrandchildOnTimeout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	script := "#!/bin/sh\n" +
		"sh -c 'echo $$ > " + pidFile + "; exec sleep 30' &\n" +
		"sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Vend(context.Background(), "whatever-profile", strings.NewReader(""), 1500*time.Millisecond, false)
	if err == nil {
		t.Fatal("want a timeout error")
	}

	var pid int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, rerr := os.ReadFile(pidFile)
		if rerr != nil {
			continue
		}
		if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && p > 0 {
			pid = p
			break
		}
	}
	if pid == 0 {
		t.Fatal("grandchild never recorded its pid; test setup is broken")
	}

	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH (or similar): the grandchild is gone. The fix works.
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d is still alive after the timeout; process-group kill did not reach it", pid)
		}
	}
}
