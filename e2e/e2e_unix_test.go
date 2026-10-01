//go:build !windows

package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCtrlCKillsInFlightResolverAndOrphanedGrandchild is the regression
// guard for item 2 of the review-2 brief: terminal SIGINT is delivered only
// to the foreground process group, so a resolver faws put in its own
// process group (setpgid, for a non-interactive profile) used to survive a
// Ctrl-C entirely — confirmed externally: aws, its credential_process shell,
// and the shell's own sleep all survived with PPID 1. faws must now trap
// SIGINT, kill the in-flight child's whole group, and exit non-zero, well
// before slow-credential-process's own 300s sleep would ever return on its
// own. Unix-only: sending SIGINT to an arbitrary process and checking
// liveness with a bare signal 0 are both POSIX job-control concepts, same as
// item 1's Setpgid gating.
func TestCtrlCKillsInFlightResolverAndOrphanedGrandchild(t *testing.T) {
	requireAWS(t)
	bin := binary(t)

	e2eDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	awsDir := filepath.Join(root, "aws")
	for _, d := range []string{home, awsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pidBase := filepath.Join(root, "grandchild.pid")
	cfg := fmt.Sprintf("[profile slow]\ncredential_process = %s/slow-credential-process %s\n",
		e2eDir, pidBase)
	configPath := filepath.Join(awsDir, "config")
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(root, "agent.config")
	cmd := exec.Command(bin, "--all", "slow", "-o", outPath)
	cmd.Env = append(os.Environ(),
		"AWS_CONFIG_FILE="+configPath,
		"AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(awsDir, "credentials"),
		"HOME="+home,
		"AWS_PROFILE=", "AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=",
		"AWS_SESSION_TOKEN=", "AWS_REGION=", "AWS_DEFAULT_REGION=",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start faws: %v", err)
	}

	grandchildPidFile := pidBase + ".grandchild"
	var grandchildPid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, rerr := os.ReadFile(grandchildPidFile)
		if rerr == nil {
			if p, perr := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &grandchildPid); perr == nil && p == 1 && grandchildPid > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grandchildPid == 0 {
		t.Fatal("slow-credential-process's grandchild never recorded its pid; test setup is broken")
	}

	start := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		elapsed := time.Since(start)
		if elapsed > 5*time.Second {
			t.Fatalf("faws took %s to exit after Ctrl-C; want well under the 20s vend timeout:\n%s", elapsed, out.String())
		}
		var ee *exec.ExitError
		if err == nil || !errors.As(err, &ee) || ee.ExitCode() == 0 {
			t.Fatalf("want a non-zero exit after Ctrl-C, got %v:\n%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("faws did not exit within 15s of Ctrl-C:\n%s", out.String())
	}

	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatal("no output file should exist: a run interrupted mid-vend must never write one")
	}

	// The grandchild must not survive the interrupt as an orphan.
	deadline = time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(grandchildPid, 0); err != nil {
			break // gone
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d is still alive after Ctrl-C; the whole process group must be killed", grandchildPid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
