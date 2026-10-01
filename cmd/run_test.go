package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testConfig = `[profile acme-dev/ReadOnly]
sso_account_id = 111111111111
sso_role_name  = ReadOnly

[profile acme-prod/Admin]
sso_account_id = 222222222222
sso_role_name  = Admin
`

func withConfig(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", p)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c := NewRootCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

func TestListsEverythingWithNoFilter(t *testing.T) {
	withConfig(t)
	out, err := run(t)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "acme-dev/ReadOnly") || !strings.Contains(out, "2 profiles") {
		t.Fatalf("bare faws must list everything:\n%s", out)
	}
}

func TestListsFiltered(t *testing.T) {
	withConfig(t)
	out, err := run(t, "--all", "readonly")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "acme-prod/Admin") {
		t.Fatalf("filter not applied:\n%s", out)
	}
	if !strings.Contains(out, "1 profile") {
		t.Fatalf("want 1 profile:\n%s", out)
	}
}

func TestNoMatchIsExitCode4(t *testing.T) {
	withConfig(t)
	_, err := run(t, "--all", "nothing-matches-this")
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 4 {
		t.Fatalf("want exit code 4, got %v", err)
	}
}

func TestMalformedFilterIsExitCode3(t *testing.T) {
	withConfig(t)
	_, err := run(t, "--all", "readonly,")
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v", err)
	}
}

// TestBadDefaultIsExitCode5WithoutInvokingAWS covers FIX 1: --default must be
// validated against the resolved set before vend.VendAll runs, not after —
// both so a full disk (a genuine write-time failure) doesn't overload exit 5,
// and so a typo'd --default fails fast instead of making the user sit
// through every profile's MFA prompt first. It proves the "without invoking
// aws" half by pointing PATH at a fake "aws" that records whether it was
// ever run.
func TestBadDefaultIsExitCode5WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	withConfig(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(t.TempDir(), "agent.config")
	// Both profiles in testConfig exist, so this exercises a real,
	// non-empty resolved set with a --default that names neither of them.
	_, err := run(t, "--default", "does-not-exist", "-o", out)
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 5 {
		t.Fatalf("want exit code 5, got %v", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should name the bad --default, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --default is invalid")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("no file should be written")
	}
}

// TestWhitespaceRegionIsExitCode3WithoutInvokingAWS covers the relocation of
// the --region/--output/-c whitespace check in opts.extras(): it must run
// before vend.VendAll, not after, so a typo'd --region fails fast instead of
// making the user sit through every profile's MFA prompt first. It proves
// the "without invoking aws" half the same way
// TestBadDefaultIsExitCode5WithoutInvokingAWS does: by pointing PATH at a
// fake "aws" that records whether it was ever run.
func TestWhitespaceRegionIsExitCode3WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	withConfig(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "--region", "us east 1", "-o", out)
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v", err)
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("error should mention whitespace, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --region contains whitespace")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("no file should be written")
	}
}

// TestBadFormatIsExitCode2WithoutInvokingAWS covers FIX B: an unknown
// --format must be rejected before vend.VendAll runs, not after — output.
// Render performs the same check, but only once every matching profile
// (MFA-requiring ones included) has already been vended. Proves the
// "without invoking aws" half the same way TestBadDefaultIsExitCode5Without
// InvokingAWS does: by pointing PATH at a fake "aws" that records whether it
// was ever run.
func TestBadFormatIsExitCode2WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	withConfig(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "--all", "acme", "--format", "bogus", "-o", out)
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 2 {
		t.Fatalf("want exit code 2, got %v", err)
	}
	if !strings.Contains(err.Error(), `unknown format "bogus"`) {
		t.Fatalf("error should name the bad format, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --format is unknown")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("no file should be written")
	}
}

// TestFormatEnvWithMultipleMatchesIsExitCode2WithoutInvokingAWS covers the
// other half of FIX B: --format env against more than one resolved profile
// must fail before vending, not after.
func TestFormatEnvWithMultipleMatchesIsExitCode2WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	withConfig(t) // testConfig has two profiles

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := run(t, "--format", "env", "-o", "-")
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 2 {
		t.Fatalf("want exit code 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "needs exactly one profile, got 2") {
		t.Fatalf("error should mention the count, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --format env has more than one match")
	}
}

// TestDefaultCollidingWithRealDefaultIsExitCode5WithoutInvokingAWS covers
// FIX 6: --default naming something other than "default" while the resolved
// set already contains a profile literally named "default" must be rejected
// before vending — the AWS CLI would otherwise silently resolve "default" to
// whichever [default] block it parses last.
func TestDefaultCollidingWithRealDefaultIsExitCode5WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	p := filepath.Join(t.TempDir(), "config")
	cfg := "[default]\nregion = us-east-1\n\n[profile acme-dev/ReadOnly]\nsso_account_id = 111111111111\nsso_role_name = ReadOnly\n"
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", p)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(t.TempDir(), "agent.config")
	_, err := run(t, "--default", "acme-dev/ReadOnly", "-o", out)
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 5 {
		t.Fatalf("want exit code 5, got %v", err)
	}
	if !strings.Contains(err.Error(), "default") {
		t.Fatalf("error should mention the colliding default profile, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --default collides with a real [default] profile")
	}
}

// TestDefaultWithJSONFormatIsExitCode3WithoutInvokingAWS covers item 7:
// --default used to be silently ignored for --format json/env; it must
// error instead of shaping output no differently than without it.
func TestDefaultWithJSONFormatIsExitCode3WithoutInvokingAWS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	withConfig(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\necho invoked > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(t.TempDir(), "agent.json")
	_, err := run(t, "--all", "acme-dev", "--default", "acme-dev/ReadOnly", "--format", "json", "-o", out)
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("aws must never be invoked when --default is used with --format json")
	}
}

// TestNoPositionalArgsSuggestsAllFlag covers item 7: faws has no
// subcommands, so `faws readonly` must not print cobra's generic "unknown
// command" message — it should point at --all instead.
func TestNoPositionalArgsSuggestsAllFlag(t *testing.T) {
	withConfig(t)
	out, err := run(t, "readonly")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("must not use cobra's generic unknown-command message, got %v", err)
	}
	if !strings.Contains(err.Error(), "--all readonly") {
		t.Fatalf("error should suggest --all readonly, got %v: %s", err, out)
	}
}

func TestInvocationLine(t *testing.T) {
	got := invocationLine(opts{all: []string{"a,b"}, any: []string{"c"}, exclude: []string{"prod"}})
	want := "--all a,b --any c --exclude prod"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestInvocationLineRecordsFormatRegionOutputAndSet(t *testing.T) {
	got := invocationLine(opts{
		all:       []string{"readonly"},
		format:    "credentials",
		region:    "ap-southeast-2",
		outputFmt: "json",
		set:       []string{"role_session_name=agent"},
	})
	want := "--all readonly --format credentials --region ap-southeast-2 --output json -c role_session_name=agent"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestInvocationLineRecordsDefault covers FIX A: --default must be recorded
// so --refresh reproduces the [default] block, not just the profile set and
// format/region/output/-c.
func TestInvocationLineRecordsDefault(t *testing.T) {
	got := invocationLine(opts{all: []string{"acme"}, defaultTo: "acme-dev/ReadOnly"})
	want := "--all acme --default acme-dev/ReadOnly"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestInvocationLineRoundTripsNoCopy covers --no-copy: it must survive
// invocationLine/parseInvocationLine like every other flag that shapes
// output, or a file vended with --no-copy would refresh back silently
// carrying copied keys again.
func TestInvocationLineRoundTripsNoCopy(t *testing.T) {
	line := invocationLine(opts{all: []string{"readonly"}, noCopy: true})
	want := "--all readonly --no-copy"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
	parsed, err := parseInvocationLine(line)
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if !parsed.noCopy {
		t.Fatal("--no-copy must round-trip")
	}
	if invocationLine(parsed) != line {
		t.Fatalf("round trip lost information: %q", invocationLine(parsed))
	}
}

func TestInvocationLineOmitsDefaultFormat(t *testing.T) {
	got := invocationLine(opts{all: []string{"readonly"}, format: "config"})
	if strings.Contains(got, "--format") {
		t.Fatalf("the default format must not be recorded: %q", got)
	}
}

// TestClearRefreshOutAreMutuallyExclusive covers FIX 6: cobra rejects the
// combination up front rather than --clear silently ignoring -o/--refresh,
// or --refresh silently overriding -o.
func TestClearRefreshOutAreMutuallyExclusive(t *testing.T) {
	withConfig(t)
	for _, args := range [][]string{
		{"--clear", "x", "--refresh", "y"},
		{"--clear", "x", "-o", "y"},
		{"--refresh", "x", "-o", "y"},
	} {
		if _, err := run(t, args...); err == nil {
			t.Fatalf("%v: want an error", args)
		}
	}
}

func asExitCoder(err error, target *ExitCoder) bool {
	for err != nil {
		if ec, ok := err.(ExitCoder); ok {
			*target = ec
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// A deny-listed key passed with -c would make the consumer re-resolve, which
// is what faws exists to prevent, so it fails before any vend.
func TestSetRejectsDenyListedKeysWithoutInvokingAWS(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	for _, kv := range []string{"credential_process=/bin/evil", "role_arn=arn:aws:iam::1:role/x", "source_profile=x", "sso_start_url=x", "granted_sso_region=x", "aws_secret_access_key=x"} {
		path := filepath.Join(t.TempDir(), "agent.config")
		_, err := run(t, "--all", "readonly", "-c", kv, "-o", path)
		var ec ExitCoder
		if err == nil || !asExitCoder(err, &ec) || ec.ExitCode() != 3 {
			t.Fatalf("-c %s: want exit 3, got %v", kv, err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("-c %s: nothing must be written", kv)
		}
	}
}
