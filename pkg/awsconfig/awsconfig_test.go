package awsconfig

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `[default]
region = us-east-1

[profile Dev/ReadOnly]
sso_account_id = 111111111111
sso_role_name  = ReadOnly
region         = us-east-1  # inline comment
credential_process = granted credential-process --profile Dev/ReadOnly

; a semicolon comment line
[profile prod-admin]
role_arn       = arn:aws:iam::222222222222:role/AdminRole
source_profile = default
mfa_serial     = arn:aws:iam::111111111111:mfa/ken

[sso-session acme]
sso_start_url = https://acme.awsapps.com/start
sso_region    = us-east-1

[services myservices]
s3 =
  endpoint_url = https://s3.example.com
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFile(t *testing.T) {
	got, err := ParseFile(writeTemp(t, "config", sample))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 profiles (incl. default), got %d: %+v", len(got), got)
	}
	if got[0].Name != "default" {
		t.Fatalf("bare [default] must be captured, got %q", got[0].Name)
	}
	if got[1].Name != "Dev/ReadOnly" {
		t.Fatalf("want Dev/ReadOnly, got %q", got[1].Name)
	}
	// Trailing "comments" are NOT stripped: botocore does not strip them
	// either (verified: `aws configure get region` returns the whole string
	// "us-east-1  # inline comment"), so faws's parse must match it byte for
	// byte rather than silently losing data the CLI keeps.
	if v := got[1].Get("region"); v != "us-east-1  # inline comment" {
		t.Fatalf("value must match botocore byte for byte (no comment stripping), got %q", v)
	}
	// Every key must be retained, not just recognised ones.
	if v := got[1].Get("credential_process"); v == "" {
		t.Fatal("credential_process must be retained")
	}
	if v := got[2].Get("role_arn"); v != "arn:aws:iam::222222222222:role/AdminRole" {
		t.Fatalf("role_arn: %q", v)
	}
}

// TestParseFileSkipsNonProfileSections covers the section-header SKIP branch:
// [sso-session ...] and [services ...] are not profiles and must not appear
// in the result, and their keys must not leak into whatever profile precedes
// or follows them.
func TestParseFileSkipsNonProfileSections(t *testing.T) {
	got, err := ParseFile(writeTemp(t, "config", sample))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("sso-session and services sections must not become profiles; want 3, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Name == "acme" || p.Name == "myservices" {
			t.Fatalf("non-profile section leaked in as a profile: %+v", p)
		}
		if v := p.Get("sso_start_url"); v != "" {
			t.Fatalf("sso-session key leaked into profile %q: %q", p.Name, v)
		}
		if v := p.Get("endpoint_url"); v != "" {
			t.Fatalf("services key leaked into profile %q: %q", p.Name, v)
		}
	}
}

// TestParseFileStripsQuotedProfileNames covers item 4: botocore shlex-parses
// [profile "quoted name"] down to the profile "quoted name", not "quoted
// name" including the quote characters. VERIFIED against the real CLI:
// `aws configure list-profiles` shows the unquoted form, and vending fails
// unless faws matches it. Both double- and single-quoted forms must work.
func TestParseFileStripsQuotedProfileNames(t *testing.T) {
	cfg := "[profile \"quoted name\"]\nregion = us-east-1\n\n[profile 'single name']\nregion = us-west-2\n"
	got, err := ParseFile(writeTemp(t, "config", cfg))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 profiles, got %d: %+v", len(got), got)
	}
	if got[0].Name != "quoted name" {
		t.Fatalf("double-quoted name must be stripped, got %q", got[0].Name)
	}
	if got[1].Name != "single name" {
		t.Fatalf("single-quoted name must be stripped, got %q", got[1].Name)
	}
}

// TestParseFileRetainsNestedSettings covers item 3: a botocore-nested
// setting (an indented continuation under a parent key, e.g. `s3 =` followed
// by indented `max_concurrent_requests`/`addressing_style`) used to be
// flattened into bogus top-level keys, so the parent vended empty and the
// nested tuning was silently unreachable (`aws configure get
// s3.max_concurrent_requests` returned nothing). The parser must record it
// under its parent instead of promoting it to the top level.
func TestParseFileRetainsNestedSettings(t *testing.T) {
	cfg := "[profile nested-settings]\n" +
		"region = us-east-1\n" +
		"s3 =\n" +
		"  max_concurrent_requests = 20\n" +
		"  addressing_style = path\n" +
		"output = json\n"
	got, err := ParseFile(writeTemp(t, "config", cfg))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 profile, got %d: %+v", len(got), got)
	}
	p := got[0]

	// The parent key itself is still a normal top-level key, empty, exactly
	// as written.
	if v, ok := p.Keys["s3"]; !ok || v != "" {
		t.Fatalf("parent key s3 must still be a top-level key with its own (empty) value, got %q, present=%v", v, ok)
	}
	// The nested children must NOT leak into the top-level Keys/Order at all
	// — that was the whole bug (a bogus, unreachable top-level key).
	for _, bogus := range []string{"max_concurrent_requests", "addressing_style"} {
		if _, ok := p.Keys[bogus]; ok {
			t.Fatalf("%q must not be promoted to a top-level key: %+v", bogus, p)
		}
	}
	// Every OTHER top-level key must still parse normally, undisturbed by
	// the nested block sitting between them.
	if p.Get("region") != "us-east-1" || p.Get("output") != "json" {
		t.Fatalf("keys around the nested block must still parse: %+v", p)
	}

	if got := p.GetNested("s3", "max_concurrent_requests"); got != "20" {
		t.Fatalf("GetNested(s3, max_concurrent_requests) = %q, want 20", got)
	}
	if got := p.GetNested("s3", "addressing_style"); got != "path" {
		t.Fatalf("GetNested(s3, addressing_style) = %q, want path", got)
	}
	if got := p.Nested["s3"].Order; len(got) != 2 || got[0] != "max_concurrent_requests" || got[1] != "addressing_style" {
		t.Fatalf("nested key order must be preserved, got %v", got)
	}
}

// TestParseFileCredentialsStyleNameWithSpaceIsNotDropped is the regression
// guard for a bug this fix hit while wiring up items 3/5's own tests: the
// non-profile-section skip ("[sso-session x]/[services x] are not
// profiles") used to fire on ANY bracket content containing a space whose
// first word wasn't literally "profile" — which silently dropped an
// ordinary bare credentials-style section whose own name has a space
// (typically because it's quoted, e.g. a file faws itself wrote for a
// profile like "quoted space/Ops" via pkg/output.quoteSectionName). Only
// "sso-session" and "services" are section KEYWORDS; a credentials file's
// bare `[name]` is never shlex-tokenized by botocore in the first place, so
// its name must be taken as one literal string, spaces included.
func TestParseFileCredentialsStyleNameWithSpaceIsNotDropped(t *testing.T) {
	got, err := ParseFile(writeTemp(t, "credentials", "[\"quoted space/Ops\"]\naws_access_key_id = AKIAEXAMPLE\n"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 profile, got %d: %+v", len(got), got)
	}
	if got[0].Name != "quoted space/Ops" {
		t.Fatalf("want the quotes stripped and the space preserved, got %q", got[0].Name)
	}
}

func TestParseFileCredentialsStyle(t *testing.T) {
	// The credentials file uses bare [name] sections.
	got, err := ParseFile(writeTemp(t, "credentials", "[work]\naws_access_key_id = AKIAEXAMPLE\n"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got) != 1 || got[0].Name != "work" {
		t.Fatalf("want one profile named work, got %+v", got)
	}
}

func TestParseFileMissingIsEmpty(t *testing.T) {
	got, err := ParseFile(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no profiles, got %+v", got)
	}
}

func TestLoadPrefersConfigOnNameCollision(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	credPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfgPath, []byte("[profile dup]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("[dup]\naws_access_key_id = AKIA\n[only-creds]\naws_access_key_id = AKIA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credPath)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 profiles, got %d: %+v", len(got), got)
	}
	if got[0].Name != "dup" || got[0].Get("region") != "us-east-1" {
		t.Fatalf("config must win on collision: %+v", got[0])
	}
	if got[1].Name != "only-creds" {
		t.Fatalf("credentials-only profile must be included: %+v", got[1])
	}
}
