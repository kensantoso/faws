package output

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/vend"
)

func creds() []vend.Creds {
	return []vend.Creds{
		{Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1", SessionToken: "t1", Expiration: "2026-09-23T18:42:00Z"},
		{Profile: "acme-prod/ReadOnly", AccessKeyID: "ASIA2", SecretAccessKey: "s2", SessionToken: "t2"},
	}
}

func TestRenderConfigFormat(t *testing.T) {
	var buf bytes.Buffer
	opts := Options{Format: "config", Default: "acme-dev/ReadOnly",
		Extra: map[string]string{"region": "ap-southeast-2"}, ExtraOrder: []string{"region"}}
	if err := Render(&buf, creds(), opts); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[profile acme-dev/ReadOnly]") {
		t.Fatalf("config format needs a 'profile ' prefix:\n%s", out)
	}
	if !strings.Contains(out, "[default]") {
		t.Fatalf("default block missing:\n%s", out)
	}
	if !strings.Contains(out, "region = ap-southeast-2") {
		t.Fatalf("extra keys must be written:\n%s", out)
	}
	if !strings.Contains(out, "2026-09-23T18:42:00Z") {
		t.Fatalf("expiration must be recorded:\n%s", out)
	}
}

// TestRenderQuotesSectionNamesContainingWhitespace covers item 5: faws
// strips quotes from a `[profile "quoted name"]` header on READ (so its own
// filtering matches the CLI's unquoted name), but used to write the name
// back out bare on vend. botocore shlex-splits the header, and a bare space
// makes `aws configure list-profiles` silently omit the profile — not error,
// just drop it. The name must be re-quoted whenever it contains whitespace.
func TestRenderQuotesSectionNamesContainingWhitespace(t *testing.T) {
	c := []vend.Creds{{Profile: "acme prod/Admin", AccessKeyID: "ASIA1", SecretAccessKey: "s1"}}

	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `[profile "acme prod/Admin"]`) {
		t.Fatalf("a name with a space must be re-quoted on write:\n%s", buf.String())
	}

	buf.Reset()
	if err := Render(&buf, c, Options{Format: "credentials"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `["acme prod/Admin"]`) {
		t.Fatalf("credentials format must also re-quote a name with a space:\n%s", buf.String())
	}
}

// TestRenderDoesNotQuoteOrdinaryNames covers the common case: the
// overwhelming majority of profile names carry no whitespace or quote
// characters and must round-trip byte for byte, unquoted.
func TestRenderDoesNotQuoteOrdinaryNames(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, creds(), Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), `"`) {
		t.Fatalf("an ordinary name must never be quoted:\n%s", buf.String())
	}
}

func TestRenderCredentialsFormatHasNoProfilePrefix(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, creds(), Options{Format: "credentials"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[acme-dev/ReadOnly]") || strings.Contains(out, "[profile ") {
		t.Fatalf("credentials format uses bare section names:\n%s", out)
	}
}

func TestRenderEnvFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, creds()[:1], Options{Format: "env"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"export AWS_ACCESS_KEY_ID=ASIA1", "export AWS_SESSION_TOKEN=t1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestRenderEnvRejectsMultiple(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, creds(), Options{Format: "env"}); err == nil {
		t.Fatal("env format cannot express more than one profile")
	}
}

func TestRenderJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, creds(), Options{Format: "json"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `"AccessKeyId": "ASIA1"`) {
		t.Fatalf("json shape:\n%s", buf.String())
	}
}

// TestRenderCopiesSourceKeysDenyingAcquisitionOnes covers FIX 1: a profile's
// own region/output/exotic key must be copied into the vended block, but its
// SSO/role-assumption/credential_process acquisition keys must never be —
// copying those would make the consumer try to re-resolve, the exact thing
// faws exists to prevent.
func TestRenderCopiesSourceKeysDenyingAcquisitionOnes(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys: map[string]string{
			"region":         "ap-southeast-2",
			"output":         "json",
			"ca_bundle":      "/etc/ssl/custom.pem",
			"sso_start_url":  "https://acme.awsapps.com/start",
			"role_arn":       "arn:aws:iam::111111111111:role/ReadOnly",
			"source_profile": "base",
			"mfa_serial":     "arn:aws:iam::111111111111:mfa/ken",
		},
		SourceOrder: []string{"sso_start_url", "role_arn", "source_profile", "mfa_serial", "region", "output", "ca_bundle"},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"region = ap-southeast-2", "output = json", "ca_bundle = /etc/ssl/custom.pem"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage key must be copied, missing %q:\n%s", want, out)
		}
	}
	for _, deny := range []string{"sso_start_url", "role_arn", "source_profile", "mfa_serial"} {
		if strings.Contains(out, deny) {
			t.Fatalf("acquisition key %q must never be copied:\n%s", deny, out)
		}
	}
}

// TestRenderReEmitsNestedSettingsIndented covers item 3: a botocore-nested
// setting (recorded under vend.Creds.SourceNested by cmd/run.go) must be
// re-emitted indented under its own parent key, not flattened into bogus
// top-level keys — that flattening is exactly the bug that made the nested
// tuning silently unreachable (`aws configure get
// s3.max_concurrent_requests` returned nothing).
func TestRenderReEmitsNestedSettingsIndented(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:        map[string]string{"s3": "", "region": "us-east-1"},
		SourceOrder:       []string{"s3", "region"},
		SourceNested:      map[string]map[string]string{"s3": {"max_concurrent_requests": "20", "addressing_style": "path"}},
		SourceNestedOrder: map[string][]string{"s3": {"max_concurrent_requests", "addressing_style"}},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "s3 = \n  max_concurrent_requests = 20\n  addressing_style = path\n") {
		t.Fatalf("nested settings must be re-emitted indented under their parent:\n%s", out)
	}
	// The nested keys must never appear as bogus top-level (unindented)
	// lines — that was the original bug.
	if strings.Contains(out, "\nmax_concurrent_requests = 20") {
		t.Fatalf("nested key must not be promoted to a bare top-level line:\n%s", out)
	}
}

// TestRenderSuppressesNestedSettingsWithNoCopy covers --no-copy for a
// profile carrying a nested setting: --no-copy must suppress the nested
// children exactly like it suppresses every other copied key.
func TestRenderSuppressesNestedSettingsWithNoCopy(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:        map[string]string{"s3": ""},
		SourceOrder:       []string{"s3"},
		SourceNested:      map[string]map[string]string{"s3": {"addressing_style": "path"}},
		SourceNestedOrder: map[string][]string{"s3": {"addressing_style"}},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config", NoCopy: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "addressing_style") {
		t.Fatalf("--no-copy must suppress nested settings too:\n%s", buf.String())
	}
}

// TestRenderNeverCopiesServicesKey covers item 4: `services = NAME`
// references a [services NAME] section faws does not copy along, so copying
// the bare key produces a file the real AWS CLI rejects outright ("the
// services configuration does not exist"). It must be dropped like any
// other deny-listed key.
func TestRenderNeverCopiesServicesKey(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"services": "myservices", "region": "us-east-1"},
		SourceOrder: []string{"services", "region"},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "services") {
		t.Fatalf("services must never be copied, it references a section faws doesn't carry along:\n%s", out)
	}
	if !strings.Contains(out, "region = us-east-1") {
		t.Fatalf("other keys must still be copied:\n%s", out)
	}
}

// TestRenderCredentialsFormatAlsoCopiesSourceKeys covers the other half of
// FIX 1: the AWS CLI reads region/output from the credentials file too
// (verified against real aws-cli), so --format credentials must carry copied
// keys exactly like config does — the only difference is the section header.
func TestRenderCredentialsFormatAlsoCopiesSourceKeys(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"region": "us-west-2"},
		SourceOrder: []string{"region"},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "credentials"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "region = us-west-2") {
		t.Fatalf("credentials format must also carry copied keys:\n%s", buf.String())
	}
}

// TestRenderExplicitRegionOverridesCopiedOne covers the stated precedence:
// "copied source value < --region/--output/-c (explicit flags win)".
func TestRenderExplicitRegionOverridesCopiedOne(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"region": "us-west-2"},
		SourceOrder: []string{"region"},
	}}
	var buf bytes.Buffer
	opts := Options{Format: "config", Extra: map[string]string{"region": "eu-west-1"}, ExtraOrder: []string{"region"}}
	if err := Render(&buf, c, opts); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "region = eu-west-1") {
		t.Fatalf("--region must override the copied value:\n%s", out)
	}
	if strings.Contains(out, "us-west-2") {
		t.Fatalf("copied value must not also appear once overridden:\n%s", out)
	}
}

// TestRenderNoCopySuppressesSourceKeys covers --no-copy.
func TestRenderNoCopySuppressesSourceKeys(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"region": "us-west-2"},
		SourceOrder: []string{"region"},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "config", NoCopy: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "region") {
		t.Fatalf("--no-copy must suppress the copied key entirely:\n%s", buf.String())
	}
}

// TestRenderNoCopyStillHonorsExplicitRegion: --no-copy disables COPYING, not
// the --region/--output/-c flags on this exact invocation.
func TestRenderNoCopyStillHonorsExplicitRegion(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"region": "us-west-2"},
		SourceOrder: []string{"region"},
	}}
	var buf bytes.Buffer
	opts := Options{Format: "config", NoCopy: true, Extra: map[string]string{"region": "eu-west-1"}, ExtraOrder: []string{"region"}}
	if err := Render(&buf, c, opts); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "region = eu-west-1") {
		t.Fatalf("--no-copy must not suppress an explicit --region:\n%s", buf.String())
	}
}

// TestRenderEnvEmitsCopiedRegion covers "--format env: emit the
// env-expressible ones too, at minimum AWS_REGION".
func TestRenderEnvEmitsCopiedRegion(t *testing.T) {
	c := []vend.Creds{{
		Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1",
		SourceKeys:  map[string]string{"region": "ap-southeast-2"},
		SourceOrder: []string{"region"},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, c, Options{Format: "env"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "export AWS_REGION=ap-southeast-2") {
		t.Fatalf("env format must emit AWS_REGION:\n%s", buf.String())
	}
}

// TestRenderRejectsDefaultWithJSONOrEnv covers item 7: --default used to be
// silently ignored for --format json/env; it must error instead.
func TestRenderRejectsDefaultWithJSONOrEnv(t *testing.T) {
	c := []vend.Creds{{Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1"}}
	for _, format := range []string{"json", "env"} {
		var buf bytes.Buffer
		err := Render(&buf, c, Options{Format: format, Default: "acme-dev/ReadOnly"})
		if err == nil {
			t.Fatalf("--default with --format %s must error, not silently no-op", format)
		}
	}
}

func TestRenderRejectsDefaultNotInSet(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, creds(), Options{Format: "config", Default: "nope"})
	if err == nil {
		t.Fatal("a default outside the resolved set must error")
	}
}

func TestRenderOmitsEmptySessionToken(t *testing.T) {
	noToken := []vend.Creds{
		{Profile: "acme-static", AccessKeyID: "AKIA1", SecretAccessKey: "s1", SessionToken: ""},
	}
	var buf bytes.Buffer
	if err := Render(&buf, noToken, Options{Format: "config"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "aws_session_token") {
		t.Fatalf("empty session token must be omitted from config format:\n%s", buf.String())
	}

	buf.Reset()
	if err := Render(&buf, noToken, Options{Format: "json"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "SessionToken") {
		t.Fatalf("empty session token must be omitted from json format:\n%s", buf.String())
	}
}

// TestWriteFileRejectsExistingDirectory covers item 7: -o pointing at an
// existing directory used to fail deep inside os.Rename with a confusing
// "file exists" error; it must say plainly that the target is a directory.
func TestWriteFileRejectsExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "adir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	err := WriteFile(target, creds(), Options{Format: "config"})
	if err == nil {
		t.Fatal("want an error when -o names an existing directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error should say plainly that the target is a directory, got %v", err)
	}
}

func TestWriteFileRecordsFilterAndIsAtomicAnd0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent.config")
	opts := Options{Format: "config", FilterLine: "--all readonly --exclude prod"}
	if err := WriteFile(path, creds(), opts); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasPrefix(string(b), FilterMarker) {
		t.Fatalf("file must start with the filter marker for --refresh:\n%s", b)
	}
	if !strings.Contains(string(b), "--all readonly --exclude prod") {
		t.Fatalf("filter not recorded:\n%s", b)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
	// Skipped on Windows because Go's file modes are effectively ignored
	// there, not because the file is protected there — the assertion would
	// be meaningless on that platform.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("want 0600, got %v", fi.Mode().Perm())
		}
	}
}
