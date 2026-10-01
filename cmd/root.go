// Package cmd implements the faws command.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// version is stamped at build time; "dev" in local builds.
var version = "dev"

// resolveVersion falls back to the module version, so a `go install` build
// reports its real version instead of "dev".
func resolveVersion(stamped string, bi *debug.BuildInfo, ok bool) string {
	if stamped != "dev" || !ok || bi == nil {
		return stamped
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return stamped
}

// ExitCoder is an error that carries a process exit code. See the exit code
// table in the spec.
type ExitCoder interface {
	error
	ExitCode() int
}

type codedError struct {
	code int
	err  error
}

func (e codedError) Error() string { return e.err.Error() }
func (e codedError) ExitCode() int { return e.code }
func (e codedError) Unwrap() error { return e.err }

// errf returns an error carrying the given process exit code.
func errf(code int, format string, a ...any) error {
	return codedError{code: code, err: fmt.Errorf(format, a...)}
}

// NewRootCmd builds the faws command.
func NewRootCmd() *cobra.Command {
	var o opts
	root := &cobra.Command{
		Use:   "faws",
		Short: "Filter your AWS roles, freeze the credentials into a file",
		Long: "faws filters the profiles in your AWS config and freezes their credentials into a static file, " +
			"for consumers that cannot run a credential resolver. With no --out it lists what the filter selected " +
			"and vends nothing.",
		Version: func() string { bi, ok := debug.ReadBuildInfo(); return resolveVersion(version, bi, ok) }(),
		// faws has no subcommands, so cobra.NoArgs's default message
		// ("unknown command \"readonly\" for \"faws\"") is actively
		// misleading: readonly isn't a command it almost had, it's a filter
		// term. Point at --all instead of describing what faws lacks. The
		// only positional allowed before -- is one @selector.
		Args: func(c *cobra.Command, args []string) error {
			before := args
			if dash := c.ArgsLenAtDash(); dash >= 0 {
				before = args[:dash]
			}
			selectors := 0
			for _, a := range before {
				if !strings.HasPrefix(a, "@") {
					return fmt.Errorf(
						"faws has no subcommands; did you mean --all %s? (e.g. faws --all %s)",
						a, a)
				}
				selectors++
			}
			if selectors > 1 {
				return errf(3, "only one @selector per invocation, got %d", selectors)
			}
			return nil
		},
		// Runtime errors are printed once by main(); don't dump usage.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if dash := c.ArgsLenAtDash(); dash >= 0 {
				o.exec, o.execArgv = true, args[dash:]
				args = args[:dash]
			}
			if len(args) == 1 {
				o.selector = strings.TrimPrefix(args[0], "@")
				o.listSelectors = o.selector == ""
			}
			return runInterruptible(c, o)
		},
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	addRunFlags(root, &o)
	return root
}

// runInterruptible wraps runSelection with a context that's canceled on
// SIGINT/SIGTERM. Terminal SIGINT is delivered only to the foreground
// process group; a child faws put in its own group via setpgid (see
// pkg/vend) never sees it and would otherwise survive as an orphan (item 2
// of the review-2 brief: confirmed externally that aws, its
// credential_process shell, and its sleep all survived a Ctrl-C with PPID
// 1). Catching the signal here and canceling ctx drives the exact same
// cmd.Cancel path vend.Vend already uses for a timeout — killGroup when
// Setpgid was used, killProcess when it wasn't (an interactive/MFA vend) —
// so a Ctrl-C now reaches whichever child is actually running, then faws
// exits non-zero instead of leaving it behind.
//
// No output file is at risk either way: output.WriteFile only renders and
// renames into place after every profile has already vended successfully,
// so a run interrupted mid-vend never reaches that step at all.
func runInterruptible(c *cobra.Command, o opts) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Notify stays registered until return, so a signal while an exec child
	// runs never kills faws before it deletes the vended file.
	r := &sigRouter{}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	done := make(chan struct{})
	defer close(done)
	go func() {
		stopping := false
		for {
			select {
			case sig := <-sigCh:
				if r.forward(sig) || stopping {
					continue
				}
				stopping = true
				fmt.Fprintf(c.ErrOrStderr(), "faws: received %s; stopping the in-flight vend...\n", sig)
				cancel()
			case <-done:
				return
			}
		}
	}()

	return runSelection(ctx, c, o, r)
}
