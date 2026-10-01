package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const threeProfileConfig = `[profile a/ReadOnly]
sso_account_id = 111111111111
[profile b/ReadOnly]
sso_account_id = 222222222222
[profile c/ReadOnly]
sso_account_id = 333333333333
`

func withConfigText(t *testing.T, text string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", p)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
}

// writeFakeAWSSelective is writeFakeAWSFullCLI plus two knobs: fail makes the
// named profile's vend fail with AccessDenied, and slow makes it sleep 0.5s.
// Every vend touches a marker in the returned dir.
func writeFakeAWSSelective(t *testing.T, fail, slow string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	dir, logDir := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "aws-cli/2.15.0 Python/3.11.6 Darwin/23.0.0"; exit 0; fi
profile=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--profile" ]; then profile="$2"; fi
  shift
done
touch "` + logDir + `/$(echo "$profile" | tr / _)"
if [ "$profile" = "` + slow + `" ]; then sleep 0.5; fi
if [ "$profile" = "` + fail + `" ]; then
  printf '\033[0;31maws: [ERROR]: An error occurred (AccessDenied) when calling GetRoleCredentials\033[0m\n' >&2
  exit 253
fi
echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"s","SessionToken":"t","Expiration":"2099-01-01T00:00:00Z"}'
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logDir
}

func vended(t *testing.T, logDir, profile string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(logDir, strings.ReplaceAll(profile, "/", "_")))
	return err == nil
}

func TestSkipsFailedProfileWithWarning(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "b/ReadOnly", "")
	path := filepath.Join(t.TempDir(), "agent.config")
	out, err := run(t, "-o", path)
	if err != nil {
		t.Fatalf("one failed profile must not fail the vend: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped b/ReadOnly: An error occurred (AccessDenied)") {
		t.Fatalf("want a clean skip warning with the reason:\n%s", out)
	}
	if strings.Contains(out, "\033[") {
		t.Fatalf("the reason must not carry the resolver's colour codes:\n%q", out)
	}
	if !strings.Contains(out, "2 profile(s) · 1 skipped") {
		t.Fatalf("the summary must count the skip:\n%s", out)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "b/ReadOnly") || !strings.Contains(string(data), "a/ReadOnly") || !strings.Contains(string(data), "c/ReadOnly") {
		t.Fatalf("the file must hold only the vended profiles:\n%s", data)
	}
}

func TestStrictAbortsOnFailure(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "b/ReadOnly", "")
	path := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "--strict", "-o", path)
	wantExit(t, err, 6)
	if !strings.Contains(err.Error(), "b/ReadOnly") {
		t.Fatalf("the error must name the failed profile: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("--strict must write nothing on a failure")
	}
}

func TestCanaryFailureAbortsBeforeTheRest(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	logDir := writeFakeAWSSelective(t, "a/ReadOnly", "")
	path := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "-o", path)
	wantExit(t, err, 6)
	if vended(t, logDir, "b/ReadOnly") || vended(t, logDir, "c/ReadOnly") {
		t.Fatal("a failed canary must stop the run before any other vend starts")
	}
}

func TestDefaultThatFailsIsAnError(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "b/ReadOnly", "")
	path := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "--default", "b/ReadOnly", "-o", path)
	wantExit(t, err, 6)
}

func TestLineProgressKeepsInputOrder(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "", "b/ReadOnly")
	out, err := run(t, "-o", filepath.Join(t.TempDir(), "agent.config"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ia, ib, ic := strings.Index(out, "[1/3] a/ReadOnly"), strings.Index(out, "[2/3] b/ReadOnly"), strings.Index(out, "[3/3] c/ReadOnly")
	if ia < 0 || ib < 0 || ic < 0 || !(ia < ib && ib < ic) {
		t.Fatalf("non-terminal progress must list profiles in input order:\n%s", out)
	}
}

func TestExecSkipsFailedProfile(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "c/ReadOnly", "")
	out, err := run(t, "--", "sh", "-c", `grep -c '^\[profile' "$AWS_CONFIG_FILE"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "\n2\n") || !strings.Contains(out, "1 skipped") {
		t.Fatalf("exec must run with the profiles that vended:\n%s", out)
	}
}

func TestParallelMustBePositive(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	_, err := run(t, "--parallel", "0", "-o", filepath.Join(t.TempDir(), "x"))
	wantExit(t, err, 3)
}

func TestLiveProgressRewritesLinesInPlace(t *testing.T) {
	var buf bytes.Buffer
	p := newLiveProgress(&buf, []string{"a/ReadOnly", "b/ReadOnly", "c/ReadOnly"}, 80)
	p.done(2, "c/ReadOnly", nil)
	p.done(0, "a/ReadOnly", errors.New("boom"))
	out := buf.String()
	for _, want := range []string{"… a/ReadOnly", "… b/ReadOnly", "… c/ReadOnly", "✓ c/ReadOnly", "✗ a/ReadOnly  failed", "\033[3A", "\033[1A"} {
		if !strings.Contains(out, want) {
			t.Fatalf("live output missing %q:\n%q", want, out)
		}
	}
}

func TestLiveProgressTruncatesToWidth(t *testing.T) {
	var buf bytes.Buffer
	long := strings.Repeat("x", 100)
	newLiveProgress(&buf, []string{long}, 40)
	for _, line := range strings.Split(buf.String(), "\n") {
		if n := len([]rune(line)); n > 39 {
			t.Fatalf("a live line must fit the terminal (%d runes):\n%q", n, line)
		}
	}
}

func TestSkipReasonIsCleaned(t *testing.T) {
	err := errors.New("vend \"b/ReadOnly\": exit status 253: \033[0;31maws: [ERROR]: An error occurred (AccessDenied)\033[0m\nmore detail")
	if got := skipReason(err); got != "An error occurred (AccessDenied) more detail" {
		t.Fatalf("got %q", got)
	}
}

func TestRefreshIsAllOrNothing(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	path := filepath.Join(t.TempDir(), "agent.config")
	writeFakeAWSSelective(t, "", "")
	if out, err := run(t, "-o", path); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	before, _ := os.ReadFile(path)
	writeFakeAWSSelective(t, "b/ReadOnly", "")
	_, err := run(t, "--refresh", path)
	wantExit(t, err, 6)
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("a failed refresh must leave the existing file untouched")
	}
}

func TestSkipWarningKeepsSSOSessionHint(t *testing.T) {
	withConfigText(t, `[profile a/ReadOnly]
sso_account_id = 111111111111
[profile b/ReadOnly]
sso_session = two
sso_account_id = 222222222222
`)
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "aws-cli/2.15.0 Python/3.11.6"; exit 0; fi
case "$*" in *b/ReadOnly*) echo "Error: The SSO session associated with this profile has expired or is otherwise invalid." >&2; exit 255;; esac
echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"s","SessionToken":"t","Expiration":"2099-01-01T00:00:00Z"}'
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := run(t, "-o", filepath.Join(t.TempDir(), "agent.config"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "aws sso login --sso-session two") {
		t.Fatalf("a skip warning must keep the sso-session login hint:\n%s", out)
	}
}

func TestMFAOnlySelectionStopsAtFirstFailure(t *testing.T) {
	withConfigText(t, `[profile m1]
mfa_serial = arn:aws:iam::1:mfa/x
[profile m2]
mfa_serial = arn:aws:iam::1:mfa/x
`)
	logDir := writeFakeAWSSelective(t, "m1", "")
	_, err := run(t, "-o", filepath.Join(t.TempDir(), "agent.config"))
	wantExit(t, err, 6)
	if vended(t, logDir, "m2") {
		t.Fatal("with no plain profile, the first MFA profile is the canary")
	}
}

func TestFailedLineSaysFailed(t *testing.T) {
	withConfigText(t, threeProfileConfig)
	writeFakeAWSSelective(t, "b/ReadOnly", "")
	out, _ := run(t, "-o", filepath.Join(t.TempDir(), "agent.config"))
	if !strings.Contains(out, "[2/3] b/ReadOnly  ✗ failed") {
		t.Fatalf("a failed vend line must say failed:\n%s", out)
	}
}

func TestLiveBlockNeedsRoomOnScreen(t *testing.T) {
	if liveFits(12, 5) || !liveFits(12, 40) || liveFits(12, 12) {
		t.Fatal("the live block must fit with a row to spare, or fall back to lines")
	}
}

func TestSkipReasonStripsTimeoutPrefix(t *testing.T) {
	err := errors.New(`vend "b/ReadOnly" timed out after 20s — its resolver is probably waiting`)
	if got := skipReason(err); !strings.HasPrefix(got, "timed out after 20s") {
		t.Fatalf("got %q", got)
	}
}
