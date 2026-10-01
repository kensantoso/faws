//go:build darwin || linux

// TestVendSetpgidDependsOnInteractive is the pty regression guard for the
// Setpgid-breaks-MFA bug: it drives Vend's real "aws" child under an actual
// pseudo-terminal (opened with raw syscalls — no third-party pty library),
// not just a pipe, because the bug is specifically about job-control signals
// a pipe can never deliver.
//
// Mechanism: a background process group that tries to read its controlling
// terminal is sent SIGTTIN and stops (state T) — that's the whole bug. To
// reproduce it for real we need a process that (a) is a session leader
// owning the pty as its controlling terminal, so the pty's foreground
// process group is fixed at that process's own pgid, and then (b) calls the
// real vend.Vend to spawn a child that tries to read that same terminal.
// That process can't be the top-level `go test` binary itself: setsid(2)
// fails if the caller is already a process group leader, which a `go test`
// binary run from an interactive shell usually is. So this test re-execs
// itself (TestMain intercepts via FAWS_PTY_HELPER) as a child whose
// SysProcAttr requests Setsid+Setctty — that child becomes the session
// leader/foreground process group, and it is the one that actually calls
// Vend. A fake "aws" on its PATH records its own pid, then blocks on
// `read` from stdin (the inherited controlling terminal):
//
//   - non-interactive (Setpgid applied): the fake aws lands in a NEW
//     process group, not the terminal's foreground one -> SIGTTIN -> the
//     process must be observed in state T.
//   - interactive (no Setpgid): the fake aws stays in the leader's own
//     (foreground) process group -> its read succeeds once the test writes
//     a line to the pty master, exactly like a human answering an MFA
//     prompt -> it must never be observed in state T, and Vend must
//     succeed.
package vend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const ptyHelperEnv = "FAWS_PTY_HELPER"

// TestMain lets this binary re-exec itself as the pty session-leader helper
// described above. Every other test in this package is unaffected: when
// FAWS_PTY_HELPER isn't set (the normal case), this is exactly `os.Exit(m.Run())`.
func TestMain(m *testing.M) {
	if os.Getenv(ptyHelperEnv) == "1" {
		runPTYHelper()
		return
	}
	os.Exit(m.Run())
}

// runPTYHelper is the re-exec'd child. It never returns: it calls the real
// Vend (the exact code under test) against the fake "aws" already placed on
// its PATH by the parent, then reports the outcome on fd 3 (a pipe the
// parent supplied via ExtraFiles — fd 0/1/2 are the pty itself).
func runPTYHelper() {
	report := os.NewFile(3, "report")
	interactive := os.Getenv("FAWS_PTY_INTERACTIVE") == "1"
	timeoutMS, _ := strconv.Atoi(os.Getenv("FAWS_PTY_TIMEOUT_MS"))

	_, err := Vend(context.Background(), "pty-test-profile", os.Stdin, time.Duration(timeoutMS)*time.Millisecond, interactive)
	if err != nil {
		fmt.Fprintf(report, "ERR %v\n", err)
	} else {
		fmt.Fprintln(report, "OK")
	}
	_ = report.Close()
	os.Exit(0)
}

// fakeAWSScript records its pid to $FAWS_PTY_PIDFILE, then blocks reading a
// line from stdin (the controlling terminal) before emitting a valid
// export-credentials payload — a stand-in resolver that would, in real life,
// be prompting for an MFA code on that same read.
const fakeAWSScript = "#!/bin/sh\n" +
	"echo $$ > \"$FAWS_PTY_PIDFILE\"\n" +
	"read line\n" +
	"cat <<'JSON'\n" +
	"{\"Version\":1,\"AccessKeyId\":\"ASIAFAKE\",\"SecretAccessKey\":\"s\",\"SessionToken\":\"t\",\"Expiration\":\"2099-01-01T00:00:00Z\"}\n" +
	"JSON\n"

// processState returns the value ps(1) reports for STATE (S/R/T/Z/...),
// which is the only portable-without-cgo way from a test to distinguish "a
// process stopped by SIGTTIN" from "a process blocked in a normal read" on
// both Darwin and Linux.
func processState(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "" // process may already be gone
	}
	return strings.TrimSpace(string(out))
}

func waitForPIDFile(t *testing.T, path string, deadline time.Duration) int {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake aws never recorded its pid at %s", path)
	return 0
}

// runHelper starts the re-exec'd session-leader child described in the
// package doc comment above, with the pty slave wired as its controlling
// terminal via Setsid+Setctty, and returns it (already started) plus the
// pipe end that will carry its "OK"/"ERR ..." report line.
func runHelper(t *testing.T, slave *os.File, pidFile string, interactive bool, timeoutMS int) (*exec.Cmd, *os.File) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(fakeAWSScript), 0o755); err != nil {
		t.Fatal(err)
	}

	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	interactiveFlag := "0"
	if interactive {
		interactiveFlag = "1"
	}

	cmd := exec.Command(self)
	cmd.Env = []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		ptyHelperEnv + "=1",
		"FAWS_PTY_INTERACTIVE=" + interactiveFlag,
		"FAWS_PTY_TIMEOUT_MS=" + strconv.Itoa(timeoutMS),
		"FAWS_PTY_PIDFILE=" + pidFile,
	}
	cmd.Stdin = slave
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = []*os.File{reportWrite}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true, // new session, so it can acquire a fresh controlling terminal
		Setctty: true, // ...and TIOCSCTTY the pty slave (fd 0, see Ctty below) as it
		Ctty:    0,    // index into stdin(0)/stdout(1)/stderr(2)/ExtraFiles(3+)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start pty helper: %v", err)
	}
	_ = reportWrite.Close() // the child's copy is what matters now

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	return cmd, reportRead
}

// openSubtestPTY opens a fresh master/slave pair for one subtest and
// registers their cleanup with t. The caller must call this BEFORE calling
// runHelper (which registers the helper process's own cmd.Wait cleanup):
// testing.T.Cleanup runs its callbacks last-added-first-called, so
// registering master/slave's Close after runHelper's cmd.Wait guarantees
// they close BEFORE that Wait runs.
//
// That ordering is not just tidiness — it avoids a real Darwin hang: a
// session-leader process that owns a pty as its controlling terminal gets
// stuck in the kernel's "exiting" state (confirmed with `ps`: STAT shows
// "Es" and never progresses) for as long as the pty's OTHER end (the
// master) stays open anywhere else, including in this unrelated test
// process. cmd.Wait() on that stuck helper then blocks forever. Closing
// master (and slave) before Wait is called lets the helper finish exiting
// first.
func openSubtestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, slavePath, err := openPTY()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	slave, err = os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		_ = master.Close()
		t.Fatalf("open pty slave %s: %v", slavePath, err)
	}
	return master, slave
}

func TestVendSetpgidDependsOnInteractive(t *testing.T) {
	t.Run("non-interactive profile is backgrounded and stops on SIGTTIN", func(t *testing.T) {
		master, slave := openSubtestPTY(t)
		pidFile := filepath.Join(t.TempDir(), "aws.pid")
		// 2500ms, not a tighter value: under `go test ./...` running every
		// package's tests concurrently, fork+setsid+setctty+exec for the
		// helper can occasionally take longer than a couple hundred ms
		// purely from CPU contention, and a too-tight timeout here risks
		// Vend's own deadline firing before the fake aws even manages to
		// record its pid — a false failure, not a real regression.
		_, report := runHelper(t, slave, pidFile, false /* interactive */, 2500)
		// Registered after runHelper: see openSubtestPTY's doc comment for
		// why this order (not just declaration order) matters.
		t.Cleanup(func() { _ = master.Close() })
		t.Cleanup(func() { _ = slave.Close() })

		pid := waitForPIDFile(t, pidFile, 5*time.Second)

		stopped := false
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if processState(t, pid) == "T" {
				stopped = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !stopped {
			t.Fatalf("pid %d (non-interactive) never reached state T; Setpgid must background it so a read of the controlling terminal stops it (SIGTTIN)", pid)
		}

		// Vend's own timeout now fires and killGroup reaps the stopped
		// group with SIGKILL (a stopped process can still be SIGKILLed) —
		// confirms killGroup reaches even a job-control-stopped grandchild.
		buf := make([]byte, 256)
		n, _ := report.Read(buf)
		if !strings.Contains(string(buf[:n]), "ERR") {
			t.Fatalf("want Vend to report a timeout error for the stuck non-interactive vend, got %q", string(buf[:n]))
		}
	})

	t.Run("interactive profile stays foreground and its prompt is answered", func(t *testing.T) {
		master, slave := openSubtestPTY(t)
		pidFile := filepath.Join(t.TempDir(), "aws.pid")
		_, report := runHelper(t, slave, pidFile, true /* interactive */, 5000)
		t.Cleanup(func() { _ = master.Close() })
		t.Cleanup(func() { _ = slave.Close() })

		pid := waitForPIDFile(t, pidFile, 5*time.Second)

		// It must never be observed stopped while "answering the prompt".
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			if st := processState(t, pid); st == "T" {
				t.Fatalf("pid %d (interactive) was stopped (state T); an interactive vend must never background its child", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}

		// Answer the "prompt": write a line to the pty master, exactly like
		// a human typing an MFA code and pressing enter.
		if _, err := master.Write([]byte("123456\n")); err != nil {
			t.Fatalf("write to pty master: %v", err)
		}

		buf := make([]byte, 256)
		n, err := readWithTimeout(report, buf, 4*time.Second)
		if err != nil {
			t.Fatalf("read helper report: %v", err)
		}
		if !strings.Contains(string(buf[:n]), "OK") {
			t.Fatalf("want Vend to succeed once the prompt is answered, got %q", string(buf[:n]))
		}
	})
}

// readWithTimeout reads once from f, failing rather than hanging forever if
// nothing arrives within d (a bug that reintroduced backgrounding would
// otherwise hang this test instead of failing it cleanly).
func readWithTimeout(f *os.File, buf []byte, d time.Duration) (int, error) {
	_ = f.SetReadDeadline(time.Now().Add(d))
	return f.Read(buf)
}
