// Package vend resolves static credentials for a profile by shelling out to
// `aws configure export-credentials`. This inherits whatever resolver the
// profile configures — SSO, granted, aws-vault, credential_process, or static
// keys — so faws contains no credential logic and no AWS SDK.
package vend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Timeout bounds a single export-credentials call for a profile that will
// not prompt interactively. A profile whose resolver wants an interactive
// login would otherwise block indefinitely; --timeout overrides this for
// slow corporate proxies or cold SSO caches.
const Timeout = 20 * time.Second

// MFATimeout is the deadline used instead of Timeout for a profile carrying
// mfa_serial. 20s is not enough time for a human to reach their phone and
// type a code — that used to SIGKILL the CLI mid-prompt (potentially leaving
// terminal echo disabled) and blame "an interactive login" for exactly the
// interactive login it had just killed. 5 minutes is generous enough for a
// slow MFA app without being effectively unbounded.
const MFATimeout = 5 * time.Minute

// killProcess kills only the direct child, never a process group. Used for
// an interactive vend on timeout, where the child must NOT have been put in
// its own process group (see Vend) — Getpgid/killGroup would be meaningless
// (and, worse, could reach faws's own foreground group) if called on a
// process that was never given one.
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Creds are static credentials for one profile.
type Creds struct {
	Profile         string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      string

	// SourceKeys/SourceOrder are every key/value the source profile carried
	// in its config/credentials section — including its own credential and
	// acquisition keys (sso_*, role_arn, mfa_serial, and so on). Vend and
	// VendAll never populate these; the caller (cmd/run.go) attaches them
	// from the awsconfig.Profile it already parsed, since vend has no reason
	// to know about awsconfig. pkg/output filters out the deny-listed
	// acquisition/secret keys at render time — see output.effectiveKeys —
	// so nothing needs to pre-filter this field before setting it.
	SourceKeys  map[string]string
	SourceOrder []string

	// SourceNested/SourceNestedOrder mirror SourceKeys/SourceOrder one level
	// down, for a botocore-nested setting (item 3): a key in SourceKeys with
	// an indented block under it, e.g. `s3` (empty top-level value) whose
	// children are `max_concurrent_requests`/`addressing_style`.
	// SourceNested["s3"] holds the child key/value map, SourceNestedOrder["s3"]
	// their written order. These are deliberately plain
	// map[string]string/[]string, not an awsconfig type — see the SourceKeys
	// comment above on why vend has no reason to know about awsconfig.
	SourceNested      map[string]map[string]string
	SourceNestedOrder map[string][]string
}

// processOutput mirrors the credential_process JSON contract that
// `--format process` emits.
type processOutput struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

// Parse reads the JSON produced by
// `aws configure export-credentials --format process`.
func Parse(data []byte) (Creds, error) {
	var out processOutput
	if err := json.Unmarshal(bytes.TrimSpace(data), &out); err != nil {
		return Creds{}, fmt.Errorf("parse export-credentials output: %w", err)
	}
	if out.AccessKeyID == "" || out.SecretAccessKey == "" {
		return Creds{}, errors.New("export-credentials output missing access key or secret key")
	}
	return Creds{
		AccessKeyID:     out.AccessKeyID,
		SecretAccessKey: out.SecretAccessKey,
		SessionToken:    out.SessionToken,
		Expiration:      out.Expiration,
	}, nil
}

// minMajor, minMinor and minPatch are the lowest AWS CLI v2 version faws
// supports: export-credentials landed in 2.9.0.
const (
	minMajor = 2
	minMinor = 9
	minPatch = 0
)

// cliVersionRE matches the `aws-cli/X.Y.Z ...` line `aws --version` prints
// (to stdout on v2, stderr on v1 — CheckCLI captures both).
var cliVersionRE = regexp.MustCompile(`aws-cli/(\d+)\.(\d+)\.(\d+)`)

// CheckCLI verifies the aws CLI is on PATH and is at least v2.9.0.
// export-credentials landed in that version; an older CLI (in particular any
// v1) doesn't recognise the subcommand and fails with a usage dump that
// doesn't explain why. Checking the real version here, rather than only
// PATH, turns that into one clear message before any profile is vended.
func CheckCLI() error {
	path, err := exec.LookPath("aws")
	if err != nil {
		return fmt.Errorf("aws CLI not found on PATH; faws needs AWS CLI v2 (>= 2.9.0): %w", err)
	}
	out, err := exec.Command(path, "--version").CombinedOutput() // #nosec G204 -- "aws" resolved via LookPath above
	if err != nil {
		return fmt.Errorf("run %s --version: %w", path, err)
	}
	m := cliVersionRE.FindStringSubmatch(string(out))
	if m == nil {
		return fmt.Errorf(
			"could not read a version from %s --version (got %q); faws needs AWS CLI v2 (>= 2.9.0)",
			path, strings.TrimSpace(string(out)))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	if !cliVersionOK(major, minor, patch) {
		return fmt.Errorf(
			"aws CLI %d.%d.%d found, but faws needs AWS CLI v2 >= 2.9.0 (export-credentials landed in 2.9.0)",
			major, minor, patch)
	}
	return nil
}

func cliVersionOK(major, minor, patch int) bool {
	if major != minMajor {
		return major > minMajor
	}
	if minor != minMinor {
		return minor > minMinor
	}
	return patch >= minPatch
}

// Vend resolves credentials for one profile. stdin is wired through so a
// profile with mfa_serial can prompt; pass an empty reader for profiles that
// must not prompt. timeout bounds the single export-credentials call; a
// value <= 0 uses Timeout. Callers pass MFATimeout for a profile carrying
// mfa_serial, since Timeout is nowhere near long enough for a human to reach
// their phone.
//
// ctx is the caller's parent context — pass context.Background() if nothing
// else applies. Vend derives its own timeout from it (context.WithTimeout),
// so an external cancellation (a caller trapping Ctrl-C/SIGTERM, say) tears
// down the in-flight "aws" child exactly the same way a timeout does: same
// cmd.Cancel, same interactive-vs-not choice of killProcess/killGroup below.
// That reuse is deliberate — job-control correctness for one is job-control
// correctness for the other.
//
// interactive must be true for (and only for) a profile that may prompt —
// today, one carrying mfa_serial — and it changes how the child is managed;
// see the comment on setpgid/killGroup below for why. It is a caller-supplied
// fact about the profile, not something Vend infers from timeout: the two
// used to be accidentally coupled (only MFA profiles got the long timeout,
// so the timeout value came to stand in for "might prompt"), and that
// coupling is exactly what let the Setpgid regression ship — the moment
// either fact can vary independently of the other, inferring one from the
// other silently breaks.
func Vend(ctx context.Context, profile string, stdin io.Reader, timeout time.Duration, interactive bool) (Creds, error) {
	if timeout <= 0 {
		timeout = Timeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "aws", "configure", "export-credentials",
		"--profile", profile, "--format", "process")
	var stdout, stderr bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// exec.CommandContext, left alone, kills only the "aws" process itself
	// on timeout; a credential_process grandchild it spawned (confirmed
	// externally: a `sh -c` wrapper and the `sleep` beneath it survive with
	// PPID 1) keeps running. Putting the child in its own process group and
	// killing the whole group on timeout fixes that cleanup — but it has a
	// fatal side effect for a profile that prompts: a background process
	// group that tries to read its controlling terminal receives SIGTTIN and
	// STOPS (confirmed under a pty: the "aws" child sat in state T, the MFA
	// prompt never appeared, and the run eventually failed by timeout,
	// blaming "an interactive login" that this exact mechanism had itself
	// prevented). So:
	//
	//   - interactive (may prompt): do NOT setpgid. The child stays in
	//     faws's own process group so it can read the controlling terminal
	//     normally. On timeout we can only kill the direct "aws" process, so
	//     a credential_process grandchild it spawned may survive as an
	//     orphan. That is a deliberate, accepted trade-off: a working MFA
	//     prompt beats perfect cleanup of a prompt nobody answered.
	//   - non-interactive (never prompts): setpgid as before, and kill the
	//     whole group on timeout — there is no terminal-reading downside for
	//     these, so the fuller cleanup is free.
	//
	// WaitDelay is a backstop either way: if the process (or group) somehow
	// doesn't exit promptly after the kill, Wait still returns rather than
	// blocking forever on an open pipe.
	if interactive {
		cmd.Cancel = func() error { killProcess(cmd); return nil }
	} else {
		setpgid(cmd)
		cmd.Cancel = func() error { killGroup(cmd); return nil }
	}
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Creds{}, fmt.Errorf(
				"vend %q timed out after %s — its resolver is probably waiting for an interactive login; refresh the session and retry",
				profile, timeout)
		}
		if ctx.Err() == context.Canceled {
			return Creds{}, fmt.Errorf("vend %q: interrupted", profile)
		}
		// Accepted risk: the AWS CLI writes diagnostics (not the credential
		// JSON, which goes to stdout) to stderr, so embedding it here is
		// normally safe. But a misconfigured custom credential_process
		// wrapper could in principle echo a secret to its own stderr and have
		// the CLI relay it into this error. This is inherent to shelling out
		// and cannot be mitigated without adding our own credential logic,
		// which the no-SDK constraint forbids.
		return Creds{}, fmt.Errorf("vend %q: %w: %s", profile, err, strings.TrimSpace(stderr.String()))
	}
	creds, err := Parse(stdout.Bytes())
	if err != nil {
		return Creds{}, fmt.Errorf("vend %q: %w", profile, err)
	}
	creds.Profile = profile
	return creds, nil
}
