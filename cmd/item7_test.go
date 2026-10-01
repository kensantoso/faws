package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/filter"
)

// writeFakeAWSFullCLI puts a fake "aws" on PATH that answers both
// `--version` (so vend.CheckCLI passes) and `configure export-credentials
// --profile X --format process` (so a real vend succeeds), with
// deterministic fake credentials derived from the profile name.
func writeFakeAWSFullCLI(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"aws-cli/2.15.0 Python/3.11.6 Darwin/23.0.0 exe/x86_64\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"configure\" ] && [ \"$2\" = \"export-credentials\" ]; then\n" +
		"  shift 2\n" +
		"  profile=unknown\n" +
		"  while [ $# -gt 0 ]; do\n" +
		"    if [ \"$1\" = \"--profile\" ]; then profile=\"$2\"; fi\n" +
		"    shift\n" +
		"  done\n" +
		"  echo '{\"Version\":1,\"AccessKeyId\":\"ASIAFAKE\",\"SecretAccessKey\":\"fake-secret\",\"SessionToken\":\"fake-token\",\"Expiration\":\"2099-01-01T00:00:00Z\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestMissingConfigFileReportsClearError covers item 7: on a machine with no
// ~/.aws at all, Load() returns zero profiles with no error, so this used to
// fall through to "filter matched no profiles in <path>", naming a path that
// doesn't exist and blaming a filter that was never the problem.
func TestMissingConfigFileReportsClearError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "no-such-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "no-such-credentials"))

	_, err := run(t, "--all", "readonly")
	if err == nil {
		t.Fatal("want an error")
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 2 {
		t.Fatalf("want exit code 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "no AWS config file found") {
		t.Fatalf("error should say plainly that the config file is missing, got %v", err)
	}
	if strings.Contains(err.Error(), "filter matched no profiles") {
		t.Fatalf("must not blame the filter when the real problem is a missing file, got %v", err)
	}
}

// TestWarnsOnDuplicateProfileNameInBothFiles covers item 7: a profile name
// present in both config and credentials is resolved by preferring config
// (awsconfig.Load), but the real AWS CLI resolves credential keys from
// credentials first — a genuine divergence faws does not attempt to
// replicate. It must warn, naming the profile, rather than stay silent.
func TestWarnsOnDuplicateProfileNameInBothFiles(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	credPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfgPath, []byte("[profile dup]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("[dup]\naws_access_key_id = AKIAREAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credPath)

	out, err := run(t) // bare listing: no -o, no AWS CLI needed
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "dup") {
		t.Fatalf("want a warning naming the duplicate profile, got:\n%s", out)
	}
}

// TestNoWarningWhenNoDuplicateProfileNames is the negative case: an ordinary
// config/credentials pair with no overlapping names must never produce the
// warning.
func TestNoWarningWhenNoDuplicateProfileNames(t *testing.T) {
	withConfig(t)
	out, err := run(t)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("must not warn when there is no collision:\n%s", out)
	}
}

// TestRefreshWarnsWhenAProfileDisappears covers item 7: --refresh used to
// rewrite a shrunk file silently and report success when a profile the file
// was recorded with no longer resolves (renamed, deleted, or no longer
// matching the recorded filter). It must warn on stderr, naming the
// profile(s) that disappeared.
func TestRefreshWarnsWhenAProfileDisappears(t *testing.T) {
	writeFakeAWSFullCLI(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	cfg := "[profile team-a/ReadOnly]\nregion = us-east-1\n\n[profile team-b/ReadOnly]\nregion = us-west-2\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))

	out := filepath.Join(dir, "agent.config")
	if _, err := run(t, "--all", "readonly", "-o", out); err != nil {
		t.Fatalf("initial vend: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "team-a/ReadOnly") || !strings.Contains(string(data), "team-b/ReadOnly") {
		t.Fatalf("initial vend must carry both profiles:\n%s", data)
	}

	// team-b disappears from the source config entirely.
	shrunk := "[profile team-a/ReadOnly]\nregion = us-east-1\n"
	if err := os.WriteFile(cfgPath, []byte(shrunk), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshOut, err := run(t, "--refresh", out)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !strings.Contains(refreshOut, "warning:") || !strings.Contains(refreshOut, "team-b/ReadOnly") {
		t.Fatalf("want a warning naming the disappeared profile, got:\n%s", refreshOut)
	}
	data, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "team-b/ReadOnly") {
		t.Fatalf("refreshed file must not still carry the disappeared profile:\n%s", data)
	}
	if !strings.Contains(string(data), "team-a/ReadOnly") {
		t.Fatalf("refreshed file must still carry the surviving profile:\n%s", data)
	}
}

// TestSSOExpiryHintAppendsLoginCommandForSSOSessionProfile covers item 7's
// SSO expiry hint: a vend failure whose message matches botocore's stable
// "SSO session ... has expired" text, for a profile that references a
// shared sso_session, must get the exact `aws sso login --sso-session NAME`
// command appended — faws already knows the session name from the config it
// parsed, and botocore's own generic "run aws sso login" hint doesn't name
// which of possibly several sessions to refresh.
func TestSSOExpiryHintAppendsLoginCommandForSSOSessionProfile(t *testing.T) {
	profileByName := map[string]awsconfig.Profile{
		"acme-dev/ReadOnly": {Name: "acme-dev/ReadOnly", Keys: map[string]string{"sso_session": "acme"}},
	}
	orig := errors.New(`vend "acme-dev/ReadOnly": exit status 1: The SSO session associated with this profile has expired or is otherwise invalid.`)
	got := ssoExpiryHint(orig, profileByName)
	if !strings.Contains(got.Error(), "aws sso login --sso-session acme") {
		t.Fatalf("want the exact login command appended, got %v", got)
	}
	if !errors.Is(got, orig) {
		t.Fatal("the hint must wrap, not replace, the original error")
	}
}

// TestSSOExpiryHintLeavesLegacyProfileUntouched covers the other half: a
// legacy SSO profile (sso_start_url directly on the profile, no
// sso_session) has no session name for faws to add, and botocore's own
// error already names the right command in that case.
func TestSSOExpiryHintLeavesLegacyProfileUntouched(t *testing.T) {
	profileByName := map[string]awsconfig.Profile{
		"legacy/ReadOnly": {Name: "legacy/ReadOnly", Keys: map[string]string{"sso_start_url": "https://legacy.awsapps.com/start"}},
	}
	orig := errors.New(`vend "legacy/ReadOnly": exit status 1: The SSO session associated with this profile has expired or is otherwise invalid.`)
	got := ssoExpiryHint(orig, profileByName)
	if got.Error() != orig.Error() {
		t.Fatalf("a profile with no sso_session must be left untouched, got %v", got)
	}
}

// TestSSOExpiryHintIgnoresUnrelatedErrors covers the no-op path: an
// ordinary vend failure must never be rewritten.
func TestSSOExpiryHintIgnoresUnrelatedErrors(t *testing.T) {
	profileByName := map[string]awsconfig.Profile{
		"acme-dev/ReadOnly": {Name: "acme-dev/ReadOnly", Keys: map[string]string{"sso_session": "acme"}},
	}
	orig := errors.New(`vend "acme-dev/ReadOnly": exit status 1: some unrelated failure`)
	got := ssoExpiryHint(orig, profileByName)
	if got.Error() != orig.Error() {
		t.Fatalf("an unrelated error must be left untouched, got %v", got)
	}
}

// TestDuplicateNamesAmongScopesToMatches covers the scoping decision itself
// (not just the end-to-end behaviour above): a collision the current
// selection never touched must not be reported.
func TestDuplicateNamesAmongScopesToMatches(t *testing.T) {
	// duplicateProfileNames() reads the real AWS_CONFIG_FILE/
	// AWS_SHARED_CREDENTIALS_FILE env vars directly, so point them at a
	// fixture with two collisions and filter matches down to only one.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	credPath := filepath.Join(dir, "credentials")
	cfg := "[profile dup-a]\nregion = us-east-1\n\n[profile dup-b]\nregion = us-west-2\n\n[profile solo]\nregion = eu-west-1\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := "[dup-a]\naws_access_key_id = AKIAA\n\n[dup-b]\naws_access_key_id = AKIAB\n"
	if err := os.WriteFile(credPath, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credPath)

	profiles, err := awsconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	f, err := filter.New([]string{"dup-a"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	matches := f.Apply(profiles)

	got := duplicateNamesAmong(matches)
	if len(got) != 1 || got[0] != "dup-a" {
		t.Fatalf("want only dup-a (the one actually matched), got %v", got)
	}
}
