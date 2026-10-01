//go:build !windows

package cmd

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _, _ = c.Process.Wait() })
	return c
}

// exited reports whether c ends within d.
func exited(c *exec.Cmd, d time.Duration) bool {
	done := make(chan struct{})
	go func() { _, _ = c.Process.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestRouterBeforeExecLetsVendHandleSignals(t *testing.T) {
	r := &sigRouter{}
	if r.forward(syscall.SIGTERM) {
		t.Fatal("with no exec yet, the vend path must own the signal")
	}
}

func TestRouterForwardsSIGTERMToChild(t *testing.T) {
	r := &sigRouter{}
	c := startSleeper(t)
	r.startExec(c.Process)
	if !r.forward(syscall.SIGTERM) {
		t.Fatal("exec must own the signal")
	}
	if !exited(c, 3*time.Second) {
		t.Fatal("SIGTERM must reach the child")
	}
}

func TestRouterDoesNotForwardTerminalSignals(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGHUP} {
		r := &sigRouter{}
		c := startSleeper(t)
		r.startExec(c.Process)
		if !r.forward(sig) {
			t.Fatalf("%v: exec must own the signal", sig)
		}
		if exited(c, 300*time.Millisecond) {
			t.Fatalf("%v must not be forwarded; the terminal already delivers it", sig)
		}
	}
}

func TestRouterSwallowsSignalsAfterChildExits(t *testing.T) {
	r := &sigRouter{}
	c := startSleeper(t)
	r.startExec(c.Process)
	r.endExec()
	if !r.forward(syscall.SIGTERM) {
		t.Fatal("after the child exits, a signal must not reach the vend path")
	}
}
