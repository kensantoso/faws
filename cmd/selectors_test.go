package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/output"
)

// withStore points the selector store at a fresh temp dir.
func withStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "faws", "selectors")
}

func wantExit(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatalf("want exit %d, got no error", code)
	}
	var ec ExitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != code {
		t.Fatalf("want exit %d, got %v", code, err)
	}
}

func TestSaveListsAndSavesSelector(t *testing.T) {
	withConfig(t)
	store := withStore(t)
	out, err := run(t, "--all", "readonly", "--save", "ro")
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if !strings.Contains(out, "acme-dev/ReadOnly") || !strings.Contains(out, "saved @ro") {
		t.Fatalf("save must list the matches and confirm:\n%s", out)
	}
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("store not written: %v", err)
	}
	if !strings.Contains(string(data), "ro = --all readonly") {
		t.Fatalf("store content:\n%s", data)
	}
	if fi, _ := os.Stat(store); fi.Mode().Perm() != 0o600 {
		t.Fatalf("store mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestUseSelectorLists(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "@ro")
	if err != nil {
		t.Fatalf("use: %v\n%s", err, out)
	}
	if strings.Contains(out, "acme-prod/Admin") || !strings.Contains(out, "1 profile") {
		t.Fatalf("@ro must apply the saved filter:\n%s", out)
	}
}

func TestSaveRefusesOverwriteWithoutForce(t *testing.T) {
	withConfig(t)
	store := withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "--all", "admin", "--save", "ro")
	wantExit(t, err, 3)
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error must point at --force: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(store)
	if !strings.Contains(string(data), "ro = --all readonly") {
		t.Fatalf("refused overwrite must leave the store alone:\n%s", data)
	}
	if _, err := run(t, "--all", "admin", "--save", "ro", "--force"); err != nil {
		t.Fatalf("--force overwrite: %v", err)
	}
	data, _ = os.ReadFile(store)
	if !strings.Contains(string(data), "ro = --all admin") {
		t.Fatalf("--force must overwrite:\n%s", data)
	}
}

func TestSaveRejectsBadName(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "--all", "readonly", "--save", "bad name")
	wantExit(t, err, 3)
}

func TestSaveNeverVends(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "--all", "readonly", "--save", "ro", "-o", filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatal("--save with -o must be rejected")
	}
}

func TestUnknownSelectorIsExitCode3(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "@nope")
	wantExit(t, err, 3)
	if !strings.Contains(err.Error(), "@nope") {
		t.Fatalf("error must name the selector: %v", err)
	}
}

func TestOnlyOneSelectorAllowed(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "@a", "@b")
	wantExit(t, err, 3)
}

func TestSelectorPlusExcludeNarrows(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--any", "dev,prod", "--save", "both"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "@both", "-x", "prod")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "acme-prod/Admin") || !strings.Contains(out, "acme-dev/ReadOnly") {
		t.Fatalf("-x must narrow the selector:\n%s", out)
	}
}

func TestSelectorPlusAllWarnsItWidens(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "@ro", "--all", "admin")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "widens @ro") || !strings.Contains(out, "2 profiles") {
		t.Fatalf("an extra --all must be applied and flagged:\n%s", out)
	}
}

func TestExplicitScalarFlagOverridesSelector(t *testing.T) {
	base := opts{region: "us-east-1", all: []string{"readonly"}}
	cli := opts{region: "ap-southeast-2", exclude: []string{"prod"}}
	got := mergeSelector(base, cli, func(name string) bool { return name == "region" || name == "exclude" })
	if got.region != "ap-southeast-2" {
		t.Fatalf("region: got %q", got.region)
	}
	if strings.Join(got.all, "|") != "readonly" || strings.Join(got.exclude, "|") != "prod" {
		t.Fatalf("lists must combine: %+v", got)
	}
}

func TestUnchangedScalarKeepsSelectorValue(t *testing.T) {
	base := opts{region: "us-east-1", format: "credentials"}
	cli := opts{format: "config"} // the flag default, not typed
	got := mergeSelector(base, cli, func(string) bool { return false })
	if got.region != "us-east-1" || got.format != "credentials" {
		t.Fatalf("untyped flags must not override: %+v", got)
	}
}

func TestListSelectorsShowsCountsAndFlags(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--any", "dev,prod", "--save", "both"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "@")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"ro", "1 profile", "--all readonly", "both", "2 profiles", "--any dev,prod"} {
		if !strings.Contains(out, want) {
			t.Fatalf("listing missing %q:\n%s", want, out)
		}
	}
}

func TestListSelectorsWhenNoneSaved(t *testing.T) {
	withConfig(t)
	withStore(t)
	out, err := run(t, "@")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "no saved selectors") {
		t.Fatalf("empty store must say so:\n%s", out)
	}
}

func TestForgetRemovesSelector(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--forget", "ro"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	_, err := run(t, "@ro")
	wantExit(t, err, 3)
}

func TestForgetUnknownIsExitCode3(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "--forget", "nope")
	wantExit(t, err, 3)
}

func TestStoreKeepsCommentsAndOtherSelectors(t *testing.T) {
	withConfig(t)
	store := withStore(t)
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := "# my selectors\nro = --all readonly\n"
	if err := os.WriteFile(store, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--all", "admin", "--save", "adm"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(store)
	for _, want := range []string{"# my selectors", "ro = --all readonly", "adm = --all admin"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("store lost %q:\n%s", want, data)
		}
	}
}

func TestVendFromSelectorRecordsLabelAndRefreshes(t *testing.T) {
	withConfig(t)
	withStore(t)
	writeFakeAWSFullCLI(t)
	if _, err := run(t, "--all", "readonly", "--region", "ap-southeast-2", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.config")
	if out, err := run(t, "@ro", "-o", path); err != nil {
		t.Fatalf("vend: %v\n%s", err, out)
	}
	line, err := output.ReadFilterLine(path)
	if err != nil {
		t.Fatal(err)
	}
	if line != "@ro = --all readonly --region ap-southeast-2" {
		t.Fatalf("header: got %q", line)
	}
	// Editing the selector later must not change what --refresh replays.
	if _, err := run(t, "--all", "acme", "--save", "ro", "--force"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "--refresh", path); err != nil {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "acme-prod/Admin") {
		t.Fatalf("refresh must replay the recorded flags, not the edited selector:\n%s", data)
	}
}

func TestVendFromSelectorWithExtrasDropsLabel(t *testing.T) {
	withConfig(t)
	withStore(t)
	writeFakeAWSFullCLI(t)
	if _, err := run(t, "--any", "dev,prod", "--save", "both"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.config")
	if out, err := run(t, "@both", "-x", "prod", "-o", path); err != nil {
		t.Fatalf("vend: %v\n%s", err, out)
	}
	line, _ := output.ReadFilterLine(path)
	if line != "--any dev,prod --exclude prod" {
		t.Fatalf("a modified selector must record only the expanded flags: got %q", line)
	}
}

func TestBareAtRejectsOtherModes(t *testing.T) {
	withConfig(t)
	withStore(t)
	f := filepath.Join(t.TempDir(), "agent.config")
	for _, args := range [][]string{
		{"@", "--clear", f},
		{"@", "--refresh", f},
		{"@", "-o", f},
		{"@", "--save", "x"},
	} {
		_, err := run(t, args...)
		wantExit(t, err, 3)
	}
}

func TestSelectorRejectsRefreshAndClear(t *testing.T) {
	withConfig(t)
	withStore(t)
	if _, err := run(t, "--all", "readonly", "--save", "ro"); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "agent.config")
	for _, flag := range []string{"--refresh", "--clear"} {
		_, err := run(t, "@ro", flag, f)
		wantExit(t, err, 3)
	}
}

func TestSaveEmptyNameIsExitCode3(t *testing.T) {
	withConfig(t)
	withStore(t)
	_, err := run(t, "--all", "readonly", "--save", "")
	wantExit(t, err, 3)
}

func TestSaveRejectsDenyListedSetKey(t *testing.T) {
	withConfig(t)
	store := withStore(t)
	_, err := run(t, "--all", "readonly", "-c", "credential_process=/bin/evil", "--save", "cp")
	wantExit(t, err, 3)
	if _, statErr := os.Stat(store); !os.IsNotExist(statErr) {
		t.Fatalf("a rejected save must not write the store")
	}
}
