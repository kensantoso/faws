package vend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse([]byte(`{"Version":1,"AccessKeyId":"ASIAX","SecretAccessKey":"s","SessionToken":"t","Expiration":"2026-09-23T18:42:00Z"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.AccessKeyID != "ASIAX" || got.SecretAccessKey != "s" || got.SessionToken != "t" {
		t.Fatalf("bad creds: %+v", got)
	}
	if got.Expiration != "2026-09-23T18:42:00Z" {
		t.Fatalf("expiration must be captured, got %q", got.Expiration)
	}
}

func TestParseRejectsMissingKeys(t *testing.T) {
	if _, err := Parse([]byte(`{"Version":1}`)); err == nil {
		t.Fatal("missing access key must error")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestVendErrorNamesTheProfile(t *testing.T) {
	// "aws" will either be missing or fail on a nonsense profile; either way
	// the error must name the profile so a failure in a batch is traceable.
	_, err := Vend(context.Background(), "definitely-not-a-real-profile-xyz", strings.NewReader(""), 0, false)
	if err == nil {
		t.Skip("unexpectedly succeeded; skipping")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-profile-xyz") {
		t.Fatalf("error must name the profile, got %v", err)
	}
}

// writeFakeAWSVersionCLI puts a fake "aws" on PATH that answers only
// "--version", so CheckCLI's version gate can be tested without a real AWS
// CLI install.
func writeFakeAWSVersionCLI(t *testing.T, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("relies on a #!/bin/sh fake aws")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"aws-cli/" + version + " Python/3.11.6 Darwin/23.0.0 exe/x86_64\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckCLIAcceptsMinimumSupportedV2(t *testing.T) {
	writeFakeAWSVersionCLI(t, "2.9.0")
	if err := CheckCLI(); err != nil {
		t.Fatalf("2.9.0 must be accepted: %v", err)
	}
}

func TestCheckCLIAcceptsNewerV2(t *testing.T) {
	writeFakeAWSVersionCLI(t, "2.36.41")
	if err := CheckCLI(); err != nil {
		t.Fatalf("a newer v2 must be accepted: %v", err)
	}
}

func TestCheckCLIRejectsV1(t *testing.T) {
	writeFakeAWSVersionCLI(t, "1.22.34")
	err := CheckCLI()
	if err == nil {
		t.Fatal("AWS CLI v1 must be rejected: export-credentials does not exist there")
	}
	if !strings.Contains(err.Error(), "1.22.34") {
		t.Fatalf("error should name the found version, got %v", err)
	}
}

func TestCheckCLIRejectsOldV2(t *testing.T) {
	writeFakeAWSVersionCLI(t, "2.8.9")
	err := CheckCLI()
	if err == nil {
		t.Fatal("2.8.9 predates export-credentials (landed in 2.9.0) and must be rejected")
	}
	if !strings.Contains(err.Error(), "2.8.9") {
		t.Fatalf("error should name the found version, got %v", err)
	}
}

func TestCheckCLIRejectsUnparsableVersion(t *testing.T) {
	writeFakeAWSVersionCLI(t, "banana")
	if err := CheckCLI(); err == nil {
		t.Fatal("an unparsable --version output must be rejected, not silently accepted")
	}
}
