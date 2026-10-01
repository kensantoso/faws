package listing

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/filter"
)

func TestRender(t *testing.T) {
	ms := []filter.Match{
		{Profile: awsconfig.Profile{Name: "acme-dev/ReadOnly", Keys: map[string]string{
			"sso_account_id": "111111111111", "sso_role_name": "ReadOnly", "region": "us-east-1"}},
			Keys: []string{"sso_role_name"}},
		{Profile: awsconfig.Profile{Name: "prod-admin", Keys: map[string]string{
			"role_arn": "arn:aws:iam::222222222222:role/AdminRole"}},
			Keys: []string{"name"}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, ms); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"acme-dev/ReadOnly", "111111111111", "ReadOnly", "us-east-1", "sso_role_name",
		"prod-admin", "222222222222", "AdminRole",
		"2 profiles",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSingularCount(t *testing.T) {
	ms := []filter.Match{
		{Profile: awsconfig.Profile{Name: "acme-dev/ReadOnly", Keys: map[string]string{
			"sso_account_id": "111111111111", "sso_role_name": "ReadOnly", "region": "us-east-1"}},
			Keys: []string{"sso_role_name"}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, ms); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 profile\n") {
		t.Fatalf("want singular '1 profile', got %q", out)
	}
	if strings.Contains(out, "1 profiles") {
		t.Fatalf("must not pluralize a count of 1, got %q", out)
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "0 profiles") {
		t.Fatalf("want a zero count, got %q", buf.String())
	}
}

func TestDisplayNameUnderCapUnchanged(t *testing.T) {
	name := "org00-acct0/ReadOnly"
	if got := displayName(name); got != name {
		t.Fatalf("displayName(%q) = %q, want unchanged", name, got)
	}
}

func TestDisplayNameOverCapElidedInMiddle(t *testing.T) {
	long := "org00-acct0-" + strings.Repeat("x", 220) + "/ReadOnly"
	got := displayName(long)

	if n := utf8.RuneCountInString(got); n != maxDisplayNameWidth {
		t.Fatalf("displayName rune length = %d, want %d (got %q)", n, maxDisplayNameWidth, got)
	}
	if !strings.HasPrefix(got, "org00-acct0-") {
		t.Fatalf("displayName lost its leading segment: %q", got)
	}
	if !strings.HasSuffix(got, "/ReadOnly") {
		t.Fatalf("displayName lost its trailing /Role: %q", got)
	}
	if !strings.Contains(got, string(ellipsis)) {
		t.Fatalf("displayName did not elide: %q", got)
	}
}

func TestDisplayNameUnicodeOverCapNotCorrupted(t *testing.T) {
	long := "東京-dev-" + strings.Repeat("東", 220) + "/ReadOnly"
	got := displayName(long)

	if !utf8.ValidString(got) {
		t.Fatalf("displayName produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != maxDisplayNameWidth {
		t.Fatalf("displayName rune length = %d, want %d (got %q)", n, maxDisplayNameWidth, got)
	}
	if !strings.HasSuffix(got, "/ReadOnly") {
		t.Fatalf("displayName lost its trailing /Role: %q", got)
	}
}

func TestRenderDoesNotWidenOtherRows(t *testing.T) {
	longName := "org00-acct0-" + strings.Repeat("x", 220) + "/ReadOnly"
	ms := []filter.Match{
		{Profile: awsconfig.Profile{Name: longName, Keys: map[string]string{
			"sso_account_id": "111111111111", "sso_role_name": "ReadOnly", "region": "us-east-1"}},
			Keys: []string{"sso_role_name"}},
		{Profile: awsconfig.Profile{Name: "prod-admin", Keys: map[string]string{
			"role_arn": "arn:aws:iam::222222222222:role/AdminRole"}},
			Keys: []string{"name"}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, ms); err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if !strings.Contains(line, "prod-admin") {
			continue
		}
		// Other columns (account id, role, region, why) still pad to their
		// own natural widths independent of the name column; what this
		// guards against is the outlier's raw 232-rune name (before
		// truncation) leaking into this row's width. maxDisplayNameWidth*2
		// is a generous ceiling that is nowhere near that raw length.
		if n := utf8.RuneCountInString(line); n > maxDisplayNameWidth*2 {
			t.Fatalf("row for a short profile name was widened to %d runes by an outlier: %q", n, line)
		}
	}
}
