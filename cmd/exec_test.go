package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// execPath pulls "PATH=<path>" back out of the child's output.
func execPath(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^PATH=(.+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("child did not print its config path:\n%s", out)
	}
	return m[1]
}

func TestExecRunsCommandWithConfigAndDeletesIt(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	out, err := run(t, "--all", "readonly", "--",
		"sh", "-c", `echo "PATH=$AWS_CONFIG_FILE"; echo "ARG=$1"; echo "SHARED=$AWS_SHARED_CREDENTIALS_FILE"; cat "$AWS_CONFIG_FILE"`, "sh", "{}")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	path := execPath(t, out)
	if !strings.Contains(out, "ARG="+path+"\n") {
		t.Fatalf("{} must be replaced with the config path:\n%s", out)
	}
	if !strings.Contains(out, "SHARED="+os.DevNull) {
		t.Fatalf("AWS_SHARED_CREDENTIALS_FILE must point at %s:\n%s", os.DevNull, out)
	}
	if !strings.Contains(out, "[profile acme-dev/ReadOnly]") || !strings.Contains(out, "aws_access_key_id = ASIAFAKE") {
		t.Fatalf("child must see the vended config:\n%s", out)
	}
	if strings.Contains(out, "acme-prod/Admin") {
		t.Fatalf("filter not applied:\n%s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config file must be deleted after the child exits: %v", err)
	}
}

func TestExecReplacesPlaceholderInsideAnArgument(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	out, err := run(t, "--all", "readonly", "--",
		"sh", "-c", `echo "PATH=$AWS_CONFIG_FILE"; echo "ARG=$1"`, "sh", "-v={}:{}:ro")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	path := execPath(t, out)
	if !strings.Contains(out, "ARG=-v="+path+":"+path+":ro") {
		t.Fatalf("every {} inside an argument must be replaced:\n%s", out)
	}
}

func TestExecPropagatesExitCodeSilently(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	_, err := run(t, "--all", "readonly", "--", "sh", "-c", "exit 7")
	wantExit(t, err, 7)
	if err.Error() != "" {
		t.Fatalf("a child's own exit code must not print a faws error, got %q", err.Error())
	}
}

func TestExecDeletesConfigWhenChildIsKilled(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	out, err := run(t, "--all", "readonly", "--",
		"sh", "-c", `echo "PATH=$AWS_CONFIG_FILE"; kill -TERM $$`)
	wantExit(t, err, 143)
	if _, statErr := os.Stat(execPath(t, out)); !os.IsNotExist(statErr) {
		t.Fatalf("config file must be deleted after the child is killed: %v", statErr)
	}
}

func TestExecStripsAmbientAWSCredentials(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	t.Setenv("AWS_PROFILE", "host-admin")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAHOST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "host-secret")
	t.Setenv("AWS_SESSION_TOKEN", "host-token")
	t.Setenv("AWS_REGION", "eu-west-1")
	out, err := run(t, "--all", "readonly", "--", "sh", "-c", "env")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	for _, leaked := range []string{"host-admin", "AKIAHOST", "host-secret", "host-token"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("ambient credential %q leaked into the child:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "AWS_REGION=eu-west-1") {
		t.Fatalf("non-credential AWS settings must pass through:\n%s", out)
	}
}

func TestExecMissingCommandIsExitCode127(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	_, err := run(t, "--all", "readonly", "--", "faws-no-such-command-xyz")
	wantExit(t, err, 127)
}

func TestExecNeedsACommandAfterDash(t *testing.T) {
	withConfig(t)
	_, err := run(t, "--all", "readonly", "--")
	wantExit(t, err, 3)
}

func TestExecRejectsOut(t *testing.T) {
	withConfig(t)
	_, err := run(t, "--all", "readonly", "-o", "x", "--", "true")
	wantExit(t, err, 3)
}

func TestExecRejectsNonConfigFormat(t *testing.T) {
	withConfig(t)
	_, err := run(t, "--all", "readonly", "--format", "json", "--", "true")
	wantExit(t, err, 3)
}

func TestExecWorksWithSelector(t *testing.T) {
	withConfig(t)
	withStore(t)
	writeFakeAWSFullCLI(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "@ro", "--", "sh", "-c", `cat "$AWS_CONFIG_FILE"`)
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# faws-filter: @ro = --all readonly") || strings.Contains(out, "acme-prod/Admin") {
		t.Fatalf("@ro -- cmd must vend the selector:\n%s", out)
	}
}

func TestPositionalFilterTermStillSuggestsAll(t *testing.T) {
	withConfig(t)
	_, err := run(t, "readonly", "--", "true")
	if err == nil || !strings.Contains(err.Error(), "--all readonly") {
		t.Fatalf("a bare term before -- must still suggest --all: %v", err)
	}
}

func TestExecRejectsNonConfigFormatFromSelector(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--format", "credentials", "--save", "cr"); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, "@cr", "--", "true")
	wantExit(t, err, 3)
}

// execTmp points os.MkdirTemp at a fresh dir so a test can prove nothing is left.
func execTmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("exec left %d entries in %s", len(entries), dir)
	}
}

func TestExecLeavesNoTempDirOnAnyExit(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	tmp := execTmp(t)
	for _, script := range []string{"true", "exit 7", "kill -TERM $$"} {
		_, _ = run(t, "--all", "readonly", "--", "sh", "-c", script)
		assertEmpty(t, tmp)
	}
	_, _ = run(t, "--all", "readonly", "--", "faws-no-such-command-xyz")
	assertEmpty(t, tmp)
}

func TestExecFileAndDirArePrivate(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	execTmp(t)
	out, err := run(t, "--all", "readonly", "--", "sh", "-c",
		`ls -ld "$(dirname "$AWS_CONFIG_FILE")"; ls -l "$AWS_CONFIG_FILE"`)
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drwx------") || !strings.Contains(out, "-rw-------") {
		t.Fatalf("want a 0700 dir and a 0600 file:\n%s", out)
	}
}

func TestExecStripsEveryAmbientCredentialSource(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	for _, k := range []string{
		"AWS_BEARER_TOKEN_BEDROCK", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_ROLE_ARN", "AWS_ACCESS_KEY", "AWS_SECRET_KEY", "AWS_CREDENTIAL_PROFILES_FILE",
	} {
		t.Setenv(k, "leak-"+k)
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
	out, err := run(t, "--all", "readonly", "--", "sh", "-c", "env")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if strings.Contains(out, "leak-") {
		t.Fatalf("an ambient credential source leaked into the child:\n%s", out)
	}
	// Without this, a child on an EC2/ECS host falls back to the instance role.
	if !strings.Contains(out, "AWS_EC2_METADATA_DISABLED=true\n") || strings.Contains(out, "AWS_EC2_METADATA_DISABLED=false") {
		t.Fatalf("IMDS must be disabled for the child:\n%s", out)
	}
}

func TestExecWithoutFilterWarns(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	out, err := run(t, "--", "true")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "the command gets all 2 profiles") {
		t.Fatalf("an unfiltered exec must warn:\n%s", out)
	}
}

func TestExecWithFilterDoesNotWarn(t *testing.T) {
	withConfig(t)
	writeFakeAWSFullCLI(t)
	out, err := run(t, "--all", "readonly", "--", "true")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if strings.Contains(out, "gets all") {
		t.Fatalf("a filtered exec must not warn:\n%s", out)
	}
}
