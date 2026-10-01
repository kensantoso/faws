package filter

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/awsconfig"
)

func profiles() []awsconfig.Profile {
	return []awsconfig.Profile{
		{Name: "acme-dev/ReadOnly", Keys: map[string]string{
			"sso_account_id": "111111111111", "sso_role_name": "ReadOnly"},
			Order: []string{"sso_account_id", "sso_role_name"}},
		{Name: "acme-prod/ReadOnly", Keys: map[string]string{
			"sso_account_id": "222222222222", "sso_role_name": "ReadOnly"},
			Order: []string{"sso_account_id", "sso_role_name"}},
		{Name: "acme-prod/Admin", Keys: map[string]string{
			"sso_account_id": "222222222222", "sso_role_name": "Admin"},
			Order: []string{"sso_account_id", "sso_role_name"}},
		{Name: "prod-admin", Keys: map[string]string{
			"role_arn": "arn:aws:iam::333333333333:role/AdminRole"},
			Order: []string{"role_arn"}},
		{Name: "secret-holder", Keys: map[string]string{
			"aws_secret_access_key": "readonlysecret"},
			Order: []string{"aws_secret_access_key"}},
	}
}

func names(ms []Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Profile.Name)
	}
	sort.Strings(out)
	return out
}

func mustNew(t *testing.T, all, any, exclude []string) Filter {
	t.Helper()
	f, err := New(all, any, exclude)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

func TestApplyAndWithinGroup(t *testing.T) {
	got := names(mustNew(t, []string{"222222222222,readonly"}, nil, nil).Apply(profiles()))
	want := []string{"acme-prod/ReadOnly"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestApplyOrAcrossGroups(t *testing.T) {
	f := mustNew(t, []string{"111111111111,readonly", "222222222222,admin"}, nil, nil)
	got := names(f.Apply(profiles()))
	want := []string{"acme-dev/ReadOnly", "acme-prod/Admin"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAnyIsSugarForOneGroupPerTerm(t *testing.T) {
	fromAny := names(mustNew(t, nil, []string{"111111111111,222222222222"}, nil).Apply(profiles()))
	fromAll := names(mustNew(t, []string{"111111111111", "222222222222"}, nil, nil).Apply(profiles()))
	if strings.Join(fromAny, ",") != strings.Join(fromAll, ",") {
		t.Fatalf("--any %v != repeated --all %v", fromAny, fromAll)
	}
}

func TestExcludeWinsAndIsSubtractedLast(t *testing.T) {
	got := names(mustNew(t, []string{"readonly"}, nil, []string{"prod"}).Apply(profiles()))
	want := []string{"acme-dev/ReadOnly"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMatchesRawValuesIncludingARN(t *testing.T) {
	// No ARN parsing: the raw value already contains account and role.
	if got := names(mustNew(t, []string{"333333333333"}, nil, nil).Apply(profiles())); len(got) != 1 || got[0] != "prod-admin" {
		t.Fatalf("account from raw role_arn: %v", got)
	}
	if got := names(mustNew(t, []string{"adminrole"}, nil, nil).Apply(profiles())); len(got) != 1 || got[0] != "prod-admin" {
		t.Fatalf("role from raw role_arn: %v", got)
	}
}

func TestNeverMatchesCredentialMaterial(t *testing.T) {
	for _, m := range mustNew(t, []string{"readonlysecret"}, nil, nil).Apply(profiles()) {
		if m.Profile.Name == "secret-holder" {
			t.Fatal("must never match against secret material")
		}
	}
}

// TestNeverMatchesCredentialMaterialUppercaseKeys covers the case-sensitivity
// bug: a section whose secret keys are written in the (also valid) uppercase
// form must be excluded from matching exactly like the lowercase form. This
// goes through the real parser (awsconfig.ParseFile), not a hand-built
// Profile, so it exercises the actual key-lowercasing fix in cutKV.
func TestNeverMatchesCredentialMaterialUppercaseKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	body := "[upper-holder]\nAWS_SECRET_ACCESS_KEY = readonlysecret\nAWS_ACCESS_KEY_ID = alsoreadonly\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := awsconfig.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	f := mustNew(t, []string{"readonlysecret"}, nil, nil)
	for _, m := range f.Apply(got) {
		if m.Profile.Name == "upper-holder" {
			t.Fatal("must never match against secret material, even written in uppercase keys")
		}
	}
	if f2 := mustNew(t, []string{"alsoreadonly"}, nil, nil); len(f2.Apply(got)) != 0 {
		t.Fatal("must never match against AWS_ACCESS_KEY_ID written in uppercase")
	}
}

// TestMatchesNestedSettingValues covers item 3: a filter term must still
// find a value written as an indented, nested setting (e.g. under `s3 =`),
// and report it under "parent.child" — not silently miss it (the old
// flattening bug). Goes through the real parser.
func TestMatchesNestedSettingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	body := "[profile nested-settings]\n" +
		"region = us-east-1\n" +
		"s3 =\n" +
		"  max_concurrent_requests = 20\n" +
		"  addressing_style = path\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := awsconfig.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	matches := mustNew(t, []string{"path"}, nil, nil).Apply(got)
	if len(matches) != 1 || matches[0].Profile.Name != "nested-settings" {
		t.Fatalf("must match a nested value, got %v", matches)
	}
	joined := strings.Join(matches[0].Keys, ",")
	if !strings.Contains(joined, "s3.addressing_style") {
		t.Fatalf("match should report the nested key as parent.child, got %q", joined)
	}
}

func TestMatchReportsWhichKeysMatched(t *testing.T) {
	got := mustNew(t, []string{"readonly"}, nil, []string{"prod"}).Apply(profiles())
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d", len(got))
	}
	joined := strings.Join(got[0].Keys, ",")
	if !strings.Contains(joined, "sso_role_name") && !strings.Contains(joined, "name") {
		t.Fatalf("match should name the key that matched, got %q", joined)
	}
}

func TestEmptyFilterMatchesEverything(t *testing.T) {
	f := mustNew(t, nil, nil, nil)
	if got := f.Apply(profiles()); len(got) != len(profiles()) {
		t.Fatalf("empty filter lists everything, got %d", len(got))
	}
}

func TestNewRejectsEmptyTerm(t *testing.T) {
	if _, err := New([]string{"readonly,"}, nil, nil); err == nil {
		t.Fatal("empty term must error")
	}
}

func TestNewRejectsTermWithWhitespace(t *testing.T) {
	if _, err := New([]string{"foo bar"}, nil, nil); err == nil {
		t.Fatal("a term containing whitespace must error")
	}
	// Surrounding whitespace is still fine.
	if _, err := New([]string{"  foo , bar  "}, nil, nil); err != nil {
		t.Fatalf("surrounding whitespace must still be trimmed and accepted: %v", err)
	}
}

func TestReportedKeyFollowsOrderNotMapIteration(t *testing.T) {
	// Two keys both contain "readonly"; the reported key must be the first in Order.
	p := awsconfig.Profile{
		Name: "p",
		Keys: map[string]string{
			"sso_role_name":      "ReadOnly",
			"credential_process": "granted credential-process --profile Dev/ReadOnly",
		},
		Order: []string{"sso_role_name", "credential_process"},
	}
	for i := 0; i < 50; i++ {
		got := mustNew(t, []string{"readonly"}, nil, nil).Apply([]awsconfig.Profile{p})
		if len(got) != 1 || len(got[0].Keys) != 1 || got[0].Keys[0] != "sso_role_name" {
			t.Fatalf("iteration %d: want [sso_role_name], got %v", i, got[0].Keys)
		}
	}
}
