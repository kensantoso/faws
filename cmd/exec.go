package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// Placeholder is replaced, inside every argument, with the vended file's path.
const Placeholder = "{}"

// strippedEnv are ambient variables that would let the child resolve
// credentials other than the vended ones, or point it at another file.
var strippedEnv = map[string]bool{
	"AWS_ACCESS_KEY_ID":                      true,
	"AWS_SECRET_ACCESS_KEY":                  true,
	"AWS_SESSION_TOKEN":                      true,
	"AWS_SECURITY_TOKEN":                     true,
	"AWS_CREDENTIAL_EXPIRATION":              true,
	"AWS_PROFILE":                            true,
	"AWS_DEFAULT_PROFILE":                    true,
	"AWS_CONFIG_FILE":                        true,
	"AWS_SHARED_CREDENTIALS_FILE":            true,
	"AWS_ROLE_ARN":                           true,
	"AWS_ROLE_SESSION_NAME":                  true,
	"AWS_WEB_IDENTITY_TOKEN_FILE":            true,
	"AWS_CONTAINER_CREDENTIALS_FULL_URI":     true,
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": true,
	"AWS_CONTAINER_AUTHORIZATION_TOKEN":      true,
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": true,
	"AWS_EC2_METADATA_DISABLED":              true, // re-set to true below
	// Legacy aliases some SDKs (Java, Go v1) still read.
	"AWS_ACCESS_KEY":               true,
	"AWS_SECRET_KEY":               true,
	"AWS_CREDENTIAL_PROFILES_FILE": true,
}

// strippedPrefixes cover credential families, such as AWS_BEARER_TOKEN_BEDROCK.
var strippedPrefixes = []string{"AWS_BEARER_TOKEN_"}

func stripped(key string) bool {
	k := strings.ToUpper(key)
	if strippedEnv[k] {
		return true
	}
	for _, p := range strippedPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// childEnv returns environ minus strippedEnv, pointed at the vended file.
func childEnv(environ []string, path string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !stripped(k) {
			out = append(out, kv)
		}
	}
	// IMDS off: on an EC2 or ECS host the SDK would otherwise fall back to the
	// instance role for any profile the vended file does not have.
	return append(out, "AWS_CONFIG_FILE="+path, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull,
		"AWS_EC2_METADATA_DISABLED=true")
}

func substitute(argv []string, path string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = strings.ReplaceAll(a, Placeholder, path)
	}
	return out
}

// sigRouter decides what a signal means: before exec it cancels the vend;
// from exec start on, faws owns it and forwards only SIGTERM to the child.
type sigRouter struct {
	mu      sync.Mutex
	execing bool
	child   *os.Process
}

// start runs cmd under the lock, so a signal can never land between Start
// and the router learning about the child.
func (r *sigRouter) start(ctx context.Context, cmd *exec.Cmd) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.execing, r.child = true, cmd.Process
	return nil
}

func (r *sigRouter) startExec(p *os.Process) {
	r.mu.Lock()
	r.execing, r.child = true, p
	r.mu.Unlock()
}

// endExec keeps execing set, so a late signal is swallowed, not treated as a vend stop.
func (r *sigRouter) endExec() {
	r.mu.Lock()
	r.child = nil
	r.mu.Unlock()
}

// forward reports whether exec owns the signal. SIGINT and SIGHUP are not
// forwarded: the terminal already delivers them to the child's process
// group, and a second SIGINT would make tools like claude see two Ctrl-Cs.
func (r *sigRouter) forward(sig os.Signal) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.execing {
		return false
	}
	if r.child != nil && sig == syscall.SIGTERM {
		_ = r.child.Signal(sig)
	}
	return true
}

// runChild runs argv against the vended file at path and returns its exit
// status as a silent coded error, so main prints nothing of its own.
func runChild(ctx context.Context, c *cobra.Command, argv []string, path string, r *sigRouter) error {
	argv = substitute(argv, path)
	child := exec.Command(argv[0], argv[1:]...)
	child.Env = childEnv(os.Environ(), path)
	child.Stdin, child.Stdout, child.Stderr = c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr()
	if err := r.start(ctx, child); err != nil {
		if ctx.Err() != nil {
			return errf(130, "stopped before running %s", argv[0])
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return errf(127, "run %s: %v", argv[0], err)
		}
		return errf(126, "run %s: %v", argv[0], err)
	}
	err := child.Wait()
	r.endExec()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return errf(1, "run %s: %v", argv[0], err)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return codedError{code: 128 + int(ws.Signal()), err: errors.New("")}
	}
	return codedError{code: exitErr.ExitCode(), err: errors.New("")}
}

// execSummary is the one line faws prints before handing over the terminal.
func execSummary(n int, path, skipped, clause, argv0 string) string {
	line := fmt.Sprintf("faws: %d profile(s) in %s%s", n, path, skipped)
	if clause != "" {
		line += " · " + clause
	}
	return line + " · running " + argv0
}
