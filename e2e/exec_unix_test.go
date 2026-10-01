//go:build !windows

package e2e

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGTERM sent to faws itself, not the child, must reach the child, and faws
// must still delete the vended dir and exit with the child's status.
func TestExecSIGTERMToFawsIsForwardedAndCleansUp(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	cmd := exec.Command(bin, "--all", "111111111111,readonly", "--", "sh", "-c",
		`trap 'echo got-term; exit 143' TERM; echo "PATH=$AWS_CONFIG_FILE"; while :; do sleep 0.1; done`)
	// Own process group, so a timeout can kill faws and any orphaned child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killGroup := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	defer killGroup()

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(10 * time.Second)
	path := ""
	for path == "" {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("child exited before printing its config path")
			}
			if strings.HasPrefix(l, "PATH=") {
				path = strings.TrimPrefix(l, "PATH=")
			}
		case <-deadline:
			t.Fatal("child never printed its config path")
		}
	}
	mustExist(t, path)
	// Signal faws only; the child must get SIGTERM from faws, not from us.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	rest := ""
	for open := true; open; {
		select {
		case l, ok := <-lines:
			if !ok {
				open = false
				break
			}
			rest += l + "\n"
		case <-deadline:
			killGroup()
			t.Fatal("faws did not forward SIGTERM and exit")
		}
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 143 {
		t.Fatalf("want exit 143, got %v", err)
	}
	mustContain(t, rest, "got-term")
	mustNotExist(t, path)
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("faws left %d entries in TMPDIR", len(entries))
	}
}
