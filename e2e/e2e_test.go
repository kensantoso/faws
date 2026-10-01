// Package e2e drives the real `faws` binary against a hermetic AWS config:
// AWS_CONFIG_FILE points at a rendered copy of e2e/aws/config, whose
// SSO-style profiles carry `credential_process = .../fake-credential-process
// <name>` (e2e/fake-credential-process). The real AWS CLI (`aws configure
// export-credentials`) runs that fake vendor script instead of contacting
// AWS — a genuine code path, fake tokens, no network, no real credentials.
//
// Every test also points HOME at its own t.TempDir(), so a bug that fell
// back to "~/.aws" instead of honoring AWS_CONFIG_FILE/
// AWS_SHARED_CREDENTIALS_FILE would show up here rather than silently
// touching the operator's real AWS config.
package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// requireAWS skips the test when the aws CLI is not on PATH, so `go test
// ./...` stays green on a machine without it — every test in this package
// depends on it, since faws shells out to `aws configure
// export-credentials` for every vend.
func requireAWS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws CLI not found on PATH; skipping e2e suite")
	}
}

// binary builds the faws binary once, shared across every test in this
// package, into a process-wide temp dir (not t.TempDir(), so it survives
// across subtests instead of being rebuilt for each one).
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "faws-e2e-bin")
		if err != nil {
			buildErr = err
			return
		}
		out := filepath.Join(dir, "faws")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = ".." // repo root: this package's directory is e2e/
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("build faws: %w: %s", err, buf.String())
			return
		}
		binPath = out
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return binPath
}

// fixture is one hermetic, per-test copy of the e2e AWS config/credentials,
// with HOME and the AWS env vars already pointed at it.
type fixture struct {
	configPath string
	credsPath  string
	vendDir    string // scratch dir for -o targets; not the fixture itself
}

// setupFixture renders e2e/aws/config (substituting __FAWS_E2E_DIR__ for
// this package's own directory, so credential_process resolves
// e2e/fake-credential-process) and copies e2e/aws/credentials, both into
// t.TempDir(). It points AWS_CONFIG_FILE, AWS_SHARED_CREDENTIALS_FILE and
// HOME at that temp dir so nothing here can read or write the real ~/.aws.
func setupFixture(t *testing.T) fixture {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	awsDir := filepath.Join(root, "aws")
	vendDir := filepath.Join(root, "vend")
	for _, d := range []string{home, awsDir, vendDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	e2eDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := os.ReadFile(filepath.Join(e2eDir, "aws", "config"))
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(tmpl), "__FAWS_E2E_DIR__", e2eDir)
	configPath := filepath.Join(awsDir, "config")
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := os.ReadFile(filepath.Join(e2eDir, "aws", "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(awsDir, "credentials")
	if err := os.WriteFile(credsPath, creds, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credsPath)
	t.Setenv("HOME", home) // never the real ~/.aws

	// Safe by construction, not by subtlety: faws always passes --profile,
	// so an ambient AWS_PROFILE/AWS_ACCESS_KEY_ID/etc never actually gets
	// used today — but that's a property of faws's current code, not of
	// this fixture. Clear them explicitly so hermeticity doesn't quietly
	// depend on that.
	for _, name := range []string{
		"AWS_PROFILE",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_REGION",
		"AWS_DEFAULT_REGION",
	} {
		t.Setenv(name, "")
	}

	return fixture{configPath: configPath, credsPath: credsPath, vendDir: vendDir}
}

// result is one faws invocation's combined stdout+stderr and exit code.
type result struct {
	out  string
	code int
}

func run(t *testing.T, bin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run faws %v: %v", args, err)
		}
	}
	return result{out: buf.String(), code: code}
}

func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("want %q in output:\n%s", substr, s)
	}
}

func mustNotContain(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("did not want %q in output:\n%s", substr, s)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("want %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("want %s to not exist, stat err = %v", path, err)
	}
}

// Case 01: list-all — bare faws lists everything in both AWS files, merged.
func TestCase01ListAll(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-dev/ReadOnly")
	mustContain(t, r.out, "globex-prod/Admin")
	mustContain(t, r.out, "legacy-chain")           // visible even though never vended
	mustContain(t, r.out, "shouty-static")          // visible by name; never matched by filters
	mustContain(t, r.out, "legacy-base")            // from the credentials file
	mustContain(t, r.out, "quoted space/Ops")       // item 5 fixture: quoted name read back unquoted
	mustContain(t, r.out, "nested-settings/Viewer") // item 3 fixture: nested s3 settings
	mustContain(t, r.out, "creds-only-profile")
	mustContain(t, r.out, "14 profiles")
}

// Case 02: by-name.
func TestCase02ByName(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "acme-dev")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-dev/ReadOnly")
	mustContain(t, r.out, "acme-dev/PowerUser")
	mustNotContain(t, r.out, "acme-prod")
	mustContain(t, r.out, "2 profiles")
}

// Case 03: by-org.
func TestCase03ByOrg(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "acme")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "6 profiles")
	mustNotContain(t, r.out, "globex")
}

// Case 04: groups-or — repeated/--any groups OR together, so a profile
// matching either group is listed. That's spec-correct, not a filter bug.
func TestCase04GroupsOr(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--any", "acme,globex", "--all", "readonly")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-dev/ReadOnly")
	mustContain(t, r.out, "globex-prod/Admin") // matches the globex group only, not readonly
	mustContain(t, r.out, "8 profiles")
}

// Case 05: groups-or-2 — same OR semantics as case 04, a different
// combination of groups.
func TestCase05GroupsOr2(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--any", "readonly,poweruser", "--all", "acme")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-dev/PowerUser")
	mustContain(t, r.out, "acme-prod/Admin") // matches --all acme, not readonly/poweruser
	mustContain(t, r.out, "7 profiles")
}

// Case 06: org-and-role — a single --all with comma-separated terms ANDs them.
func TestCase06OrgAndRole(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "globex,readonly")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "globex-dev/ReadOnly")
	mustNotContain(t, r.out, "globex-prod/Admin")
	mustContain(t, r.out, "1 profile")
}

// Case 07: by-account-number.
func TestCase07ByAccountNumber(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "333333333333")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-security/PowerUser")
	mustContain(t, r.out, "1 profile")
}

// Case 08: per-account-roles — repeated --all groups OR together.
func TestCase08PerAccountRoles(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "222222222222,readonly", "--all", "333333333333,poweruser")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-prod/ReadOnly")
	mustContain(t, r.out, "acme-security/PowerUser")
	mustContain(t, r.out, "2 profiles")
}

// Case 09: exclude — subtracted last, always wins.
func TestCase09Exclude(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "readonly", "--exclude", "prod")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustNotContain(t, r.out, "acme-prod/ReadOnly")
	mustContain(t, r.out, "acme-dev/ReadOnly")
	mustContain(t, r.out, "3 profiles")
}

// Case 10: substring-gotcha — "readonly" also matches ReadOnlyPlus.
func TestCase10SubstringGotcha(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "readonly")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "acme-data/ReadOnlyPlus")
	mustContain(t, r.out, "4 profiles")
}

// Case 11: secrets-never-matched — the regression guard for the
// case-insensitive secret-exclusion fix. shouty-static's secret VALUE
// contains "readonly" and its keys are UPPERCASE; a correct faws must never
// match it, so filtering on that exact string finds nothing.
func TestCase11SecretsNeverMatched(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "uppercase-secret-containing-readonly-bait")
	if r.code != 4 {
		t.Fatalf("want exit 4, got %d:\n%s", r.code, r.out)
	}
	mustNotContain(t, r.out, "shouty-static")
}

// Case 12: vend-config — the default `config` format, [profile NAME] headers.
func TestCase12VendConfig(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "agent.config")
	r := run(t, bin, "--all", "111111111111,readonly", "-o", out)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustExist(t, out)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustContain(t, content, "[profile acme-dev/ReadOnly]")
	mustContain(t, content, "aws_access_key_id = ASIAFAKE")
	mustContain(t, content, "aws_secret_access_key = fake-secret-")
	mustContain(t, content, "aws_session_token = fake-session-")
	// FIX 1 regression guard: acme-dev/ReadOnly's own `region = us-east-1`
	// (set in e2e/aws/config) must be carried into the vended block. Before
	// the fix, faws dropped it entirely, so a consumer of this file failed
	// every AWS call with "You must specify a region" despite faws already
	// having parsed it.
	mustContain(t, content, "region = us-east-1")
	mustNotContain(t, content, "granted_sso_start_url")
	mustNotContain(t, content, "credential_process")
}

// Case 13/16: vend-credentials-format then refresh — the regression guard
// for the silent-corruption bug. A file written with `--format credentials
// --region ap-southeast-2 --default globex-prod/Admin` must refresh back
// STILL in credentials format (bare [name] headers, no "profile " prefix),
// STILL carrying the region, and STILL carrying the [default] block —
// --default used to be silently dropped by --refresh, since it wasn't among
// the flags invocationLine/parseInvocationLine recorded.
//
// --all globex-prod (rather than no filter at all) is a deliberate
// deviation from a fully unfiltered vend: this fixture's legacy-chain
// profile is a role_arn/source_profile chain that a real `aws configure
// export-credentials` would resolve via an actual sts:AssumeRole network
// call, which would break hermeticity. Restricting to one profile avoids it
// while still exercising the same format+region+default round-trip.
func TestCase13And16VendCredentialsFormatThenRefresh(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "agent.credentials")
	r := run(t, bin, "--all", "globex-prod", "--format", "credentials", "--region", "ap-southeast-2",
		"--default", "globex-prod/Admin", "-o", out)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustContain(t, content, "[globex-prod/Admin]")
	mustNotContain(t, content, "[profile globex-prod/Admin]")
	mustContain(t, content, "region = ap-southeast-2")
	mustContain(t, content, "[default]")

	// --refresh: re-vend using the filter/format/region/default recorded in
	// the file.
	r = run(t, bin, "--refresh", out)
	if r.code != 0 {
		t.Fatalf("refresh: want exit 0, got %d:\n%s", r.code, r.out)
	}
	data, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content = string(data)
	mustContain(t, content, "[globex-prod/Admin]")
	mustNotContain(t, content, "[profile globex-prod/Admin]")
	mustContain(t, content, "region = ap-southeast-2")
	mustContain(t, content, "[default]")
}

// Case 14: vend-env — env format requires exactly one profile and writes to
// stdout with -o -.
func TestCase14VendEnv(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "acme-dev,readonly", "--format", "env", "-o", "-")
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, "export AWS_ACCESS_KEY_ID=ASIAFAKE")
	mustContain(t, r.out, "export AWS_SECRET_ACCESS_KEY=fake-secret-")
	mustContain(t, r.out, "export AWS_SESSION_TOKEN=fake-session-")
}

// Case 15: vend-json.
func TestCase15VendJSON(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "agent.json")
	r := run(t, bin, "--all", "acme-dev,readonly", "--format", "json", "-o", out)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustContain(t, content, `"Profile": "acme-dev/ReadOnly"`)
	mustContain(t, content, `"AccessKeyId": "ASIAFAKE`)
}

// Case 17: clear — deletes a file faws itself wrote.
func TestCase17Clear(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "agent.config")
	if r := run(t, bin, "--all", "acme-dev,readonly", "-o", out); r.code != 0 {
		t.Fatalf("vend: want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustExist(t, out)

	r := run(t, bin, "--clear", out)
	if r.code != 0 {
		t.Fatalf("clear: want exit 0, got %d:\n%s", r.code, r.out)
	}
	mustNotExist(t, out)
}

// Case 18: clear-refusal — the safety demonstration for the only destructive
// command in the tool. Pointing --clear at a real-looking credentials file
// (no faws-filter marker) must refuse AND leave the file on disk.
func TestCase18ClearRefusal(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	before, err := os.ReadFile(fx.credsPath)
	if err != nil {
		t.Fatal(err)
	}

	r := run(t, bin, "--clear", fx.credsPath)
	if r.code == 0 {
		t.Fatalf("want a non-zero exit refusing to clear a real credentials file, got 0:\n%s", r.out)
	}
	mustExist(t, fx.credsPath)
	after, err := os.ReadFile(fx.credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("--clear must not modify a file it refuses to delete")
	}
}

// Case 19: no-match.
func TestCase19NoMatch(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin, "--all", "nothing-matches-this")
	if r.code != 4 {
		t.Fatalf("want exit 4, got %d:\n%s", r.code, r.out)
	}
}

// TestQuotedProfileNameRoundTripsThroughRealCLI is the round-trip regression
// guard for item 5: faws strips the quotes from `[profile "quoted
// space/Ops"]` on read (matching botocore's shlex-based parsing), so it must
// re-quote the name on write too — writing it back bare makes
// `aws configure list-profiles` silently omit the profile entirely (no
// error). This vends the fixture's quoted-name profile, then hands the
// vended file to the REAL AWS CLI and asserts it lists the profile back.
func TestQuotedProfileNameRoundTripsThroughRealCLI(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "quoted.config")
	r := run(t, bin, "--all", "888888888888", "-o", out)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustContain(t, content, `[profile "quoted space/Ops"]`)

	// list-profiles reads AWS_CONFIG_FILE itself; point it at the file faws
	// just wrote, not the fixture, so this proves faws's OWN output
	// round-trips, not just the fixture's input.
	cmd := exec.Command("aws", "configure", "list-profiles")
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE="+out, "AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(t.TempDir(), "none"))
	cliOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aws configure list-profiles: %v:\n%s", err, cliOut)
	}
	mustContain(t, string(cliOut), "quoted space/Ops")
}

// TestNestedSettingsRoundTripThroughRealCLI is the round-trip regression
// guard for item 3: a botocore-nested setting (an indented continuation
// under a parent key, like `s3 =` / `  max_concurrent_requests = 20`) used
// to be flattened into bogus top-level keys, so `aws configure get
// s3.max_concurrent_requests` against the vended file returned nothing —
// the tuning was silently unreachable even though the file "looked" fine.
// This vends the fixture's nested-settings profile and asks the REAL AWS
// CLI for the nested value back.
func TestNestedSettingsRoundTripThroughRealCLI(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "nested.config")
	r := run(t, bin, "--all", "nested-settings", "-o", out)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustContain(t, content, "s3 = \n  max_concurrent_requests = 20\n  addressing_style = path")

	cmd := exec.Command("aws", "configure", "get", "s3.max_concurrent_requests",
		"--profile", "nested-settings/Viewer")
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE="+out, "AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(t.TempDir(), "none"))
	cliOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aws configure get s3.max_concurrent_requests: %v:\n%s", err, cliOut)
	}
	if strings.TrimSpace(string(cliOut)) != "20" {
		t.Fatalf("want the real CLI to read back the nested value 20, got %q", string(cliOut))
	}

	cmd = exec.Command("aws", "configure", "get", "s3.addressing_style",
		"--profile", "nested-settings/Viewer")
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE="+out, "AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(t.TempDir(), "none"))
	cliOut, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aws configure get s3.addressing_style: %v:\n%s", err, cliOut)
	}
	if strings.TrimSpace(string(cliOut)) != "path" {
		t.Fatalf("want the real CLI to read back the nested value path, got %q", string(cliOut))
	}
}

// Case 20: bad-default — must fail (exit 5) without ever vending.
func TestCase20BadDefault(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	out := filepath.Join(fx.vendDir, "x.config")
	r := run(t, bin, "--all", "readonly", "--default", "nope", "-o", out)
	if r.code != 5 {
		t.Fatalf("want exit 5, got %d:\n%s", r.code, r.out)
	}
	mustNotExist(t, out)
}

// The real AWS CLI inside an exec child must resolve credentials from the
// vended file alone, and the file must be gone once the child exits.
func TestExecRealCLIReadsVendedFile(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if r := run(t, bin, "--all", "111111111111,readonly", "--save", "dev-ro"); r.code != 0 {
		t.Fatalf("save: exit %d:\n%s", r.code, r.out)
	}
	r := run(t, bin, "@dev-ro", "--", "sh", "-c",
		`echo "PATH=$AWS_CONFIG_FILE"; aws configure export-credentials --profile acme-dev/ReadOnly --format process; aws configure get credential_process --profile acme-dev/ReadOnly; echo "cp-exit=$?"`)
	if r.code != 0 {
		t.Fatalf("exec: exit %d:\n%s", r.code, r.out)
	}
	mustContain(t, r.out, `"AccessKeyId": "ASIAFAKE`)
	// No resolver survives into the frozen file, so the child cannot re-resolve.
	mustContain(t, r.out, "cp-exit=1")
	path := ""
	for _, line := range strings.Split(r.out, "\n") {
		if strings.HasPrefix(line, "PATH=") {
			path = strings.TrimPrefix(line, "PATH=")
		}
	}
	if path == "" {
		t.Fatalf("child did not print its config path:\n%s", r.out)
	}
	mustNotExist(t, path)
}
