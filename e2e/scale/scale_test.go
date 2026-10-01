// Package scale drives the real faws binary against a ~500-profile hermetic
// AWS config, generated deterministically by gen-fixture.sh (see
// e2e/scale/README.md for the distribution). This is not a re-run of the
// small e2e suite at bigger numbers: it exists to find what only breaks,
// degrades, or misleads once the profile count leaves toy territory.
//
// Every expected count in this file is computed from an independent model of
// the fixture (below) plus a from-spec re-implementation of the filter
// algorithm (AND within a group, OR across groups, exclude subtracted last),
// never copied out of a run of faws itself. If the model and the real binary
// disagree, that is either a bug in faws or a bug in the model — either way
// worth finding, not papering over by "fixing" the model to match output.
//
// The fixture is otherwise identical in spirit to e2e/'s: real faws binary,
// real `aws configure export-credentials` code path, e2e/fake-credential-process
// as the sole vendor, no network, HOME repointed away from ~/.aws.
package scale

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// The independent model: what gen-fixture.sh puts in the fixture, expressed
// as data, not as a script we shell out to and scrape.
// ---------------------------------------------------------------------------

// modelProfile is one section as the fixture defines it: a name plus every
// non-secret value (secret key/value pairs are tracked separately and never
// contribute to a match, exactly like pkg/filter.SecretKeys).
type modelProfile struct {
	name   string
	values []string // every non-secret value in the section; name is checked separately
}

const (
	numOrgs         = 24
	acctsPerOrg     = 5
	rolesPerAcct    = 4
	basePopulation  = numOrgs * acctsPerOrg * rolesPerAcct // 480
	numAdversarial  = 20
	numCredsOnly    = 16 // credentials-file entries with no config collision
	numCollisions   = 4  // credentials-file entries that collide with a config name (config wins)
	expectedProfile = basePopulation + numAdversarial + numCredsOnly
)

var roles = []string{"ReadOnly", "PowerUser", "Admin", "ReadOnlyPlus"}
var regionCycle = []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-2", "eu-central-1", "ca-central-1"}

// baseModel reproduces gen-fixture.sh's base-population loop exactly: same
// account id formula, same region cycle, same profile name shape.
func baseModel() []modelProfile {
	out := make([]modelProfile, 0, basePopulation)
	i := 0
	for org := 0; org < numOrgs; org++ {
		orgNN := fmt.Sprintf("%02d", org)
		startURL := fmt.Sprintf("https://org%s.awsapps.com/start", orgNN)
		for acct := 0; acct < acctsPerOrg; acct++ {
			acctID := fmt.Sprintf("%02d%08d%02d", org, 0, acct)
			for _, role := range roles {
				region := regionCycle[i%len(regionCycle)]
				name := fmt.Sprintf("org%s-acct%d/%s", orgNN, acct, role)
				out = append(out, modelProfile{
					name:   name,
					values: []string{startURL, "us-east-1", acctID, role, region},
				})
				i++
			}
		}
	}
	return out
}

// adversarialModel reproduces the 20 hand-built profiles gen-fixture.sh
// writes. Values mirror what's actually in the file; secret key/value pairs
// (shouty-scale-a/b) are omitted entirely, matching filter.SecretKeys
// exclusion — they must never contribute to a match.
func adversarialModel() []modelProfile {
	longx := strings.Repeat("x", 220)
	longname := "longname-" + longx + "/ReadOnly"
	adv := "https://scale-adversarial.awsapps.com/start"
	return []modelProfile{
		{"nimbus", []string{adv, "us-east-1", "990000000001", "ReadOnly"}},
		{"nimbus-extra", []string{adv, "us-east-1", "990000000002", "ReadOnly"}},
		{"roletest/Deploy", []string{adv, "us-east-1", "990000000003", "Deploy"}},
		{"roletest/DeployAdmin", []string{adv, "us-east-1", "990000000004", "DeployAdmin"}},
		{"roletest/DeployAdminPlus", []string{adv, "us-east-1", "990000000005", "DeployAdminPlus"}},
		{"acctclose-a/ReadOnly", []string{adv, "us-east-1", "990000000010", "ReadOnly"}},
		{"acctclose-b/ReadOnly", []string{adv, "us-east-1", "990000000011", "ReadOnly"}},
		{"acctclose-c/ReadOnly", []string{adv, "us-east-1", "990000000012", "ReadOnly"}},
		{"café-prod/ReadOnly", []string{adv, "us-east-1", "990000000020", "ReadOnly"}},
		{"東京-dev/ReadOnly", []string{adv, "us-east-1", "990000000021", "ReadOnly"}},
		{longname, []string{adv, "us-east-1", "990000000030", "ReadOnly"}},
		{"emptyrole-profile", []string{adv, "us-east-1", "990000000040", ""}},
		{"chain-a", []string{"arn:aws:iam::990000000050:role/ChainRoleA", "chain-base-a"}},
		{"chain-b", []string{"arn:aws:iam::990000000051:role/ChainRoleB", "chain-base-b"}},
		{"shouty-scale-a", nil}, // secret keys only: never matchable
		{"shouty-scale-b", nil},
		{"mfa-a/ReadOnly", []string{adv, "us-east-1", "990000000060", "ReadOnly", "arn:aws:iam::990000000060:mfa/scale-a"}},
		{"mfa-b/PowerUser", []string{adv, "us-east-1", "990000000061", "PowerUser", "arn:aws:iam::990000000061:mfa/scale-b"}},
		{"casecheck-a/ReadOnly", []string{adv, "us-east-1", "990000000070", "ReadOnly"}},
		{"casecheck-b/PowerUser", []string{adv, "us-east-1", "990000000071", "PowerUser"}},
	}
}

// credsOnlyModel is the 16 credentials-file entries with no config
// collision. The 4 colliding entries (nimbus, shouty-scale-a, mfa-a/ReadOnly,
// org05-acct3/Admin) are deliberately NOT modeled here: config wins, so they
// contribute nothing beyond what baseModel/adversarialModel already cover.
func credsOnlyModel() []modelProfile {
	out := make([]modelProfile, 0, numCredsOnly)
	for n := 0; n <= 15; n++ {
		nn := fmt.Sprintf("%02d", n)
		out = append(out, modelProfile{
			name:   "credsonly-" + nn,
			values: []string{"scale-creds-only-" + nn},
		})
	}
	return out
}

// collisionNotes are the marker values written ONLY into the credentials
// file's colliding sections. None must ever surface through a filter: their
// presence would mean the credentials-file copy leaked through instead of
// being dropped in favor of the config version.
var collisionNotes = []string{
	"credswins-nimbus-should-not-appear",
	"credswins-shouty-scale-a-should-not-appear",
	"credswins-mfa-a-readonly-should-not-appear",
	"credswins-org05-acct3-admin-should-not-appear",
}

// fullModel is every profile that must be visible after Load() merges both
// files: base + adversarial + the 16 non-colliding creds-only entries.
func fullModel() []modelProfile {
	m := baseModel()
	m = append(m, adversarialModel()...)
	m = append(m, credsOnlyModel()...)
	return m
}

// matchesTerm mirrors pkg/filter.matchTerm: case-insensitive substring over
// the name or any non-secret value.
func matchesTerm(p modelProfile, term string) bool {
	term = strings.ToLower(term)
	if strings.Contains(strings.ToLower(p.name), term) {
		return true
	}
	for _, v := range p.values {
		if strings.Contains(strings.ToLower(v), term) {
			return true
		}
	}
	return false
}

func matchesAllTerms(p modelProfile, terms []string) bool {
	for _, t := range terms {
		if !matchesTerm(p, t) {
			return false
		}
	}
	return true
}

// expectedMatches mirrors pkg/filter.Filter.Apply: groups are ANDed
// internally and ORed against each other; exclude is subtracted last and
// always wins, checked independently of group membership.
func expectedMatches(model []modelProfile, groups [][]string, exclude []string) []modelProfile {
	var out []modelProfile
	for _, p := range model {
		excluded := false
		for _, t := range exclude {
			if matchesTerm(p, t) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if len(groups) == 0 {
			out = append(out, p)
			continue
		}
		for _, g := range groups {
			if matchesAllTerms(p, g) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func expectedCount(model []modelProfile, groups [][]string, exclude []string) int {
	return len(expectedMatches(model, groups, exclude))
}

// ---------------------------------------------------------------------------
// Harness (mirrors e2e/e2e_test.go's conventions: same fixture rendering
// approach, same hermetic env handling, same binary-build-once pattern).
// ---------------------------------------------------------------------------

func requireAWS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws CLI not found on PATH; skipping scale suite")
	}
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "faws-scale-bin")
		if err != nil {
			buildErr = err
			return
		}
		out := filepath.Join(dir, "faws")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = "../.." // repo root: this package's directory is e2e/scale/
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

type fixture struct {
	configPath string
	credsPath  string
	vendDir    string
}

// setupFixture renders e2e/scale/aws/config (substituting __FAWS_E2E_ROOT__
// for e2e/, this package's grandparent directory — where
// fake-credential-process actually lives) and copies aws/credentials
// verbatim, both into t.TempDir(). HOME and both AWS file env vars point at
// the temp dir so nothing here can touch the real ~/.aws.
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

	scaleDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	e2eRoot := filepath.Dir(scaleDir) // e2e/scale/.. = e2e/

	tmpl, err := os.ReadFile(filepath.Join(scaleDir, "aws", "config"))
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(tmpl), "__FAWS_E2E_ROOT__", e2eRoot)
	configPath := filepath.Join(awsDir, "config")
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := os.ReadFile(filepath.Join(scaleDir, "aws", "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(awsDir, "credentials")
	if err := os.WriteFile(credsPath, creds, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credsPath)
	t.Setenv("HOME", home)
	for _, name := range []string{
		"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION",
	} {
		t.Setenv(name, "")
	}

	return fixture{configPath: configPath, credsPath: credsPath, vendDir: vendDir}
}

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
		t.Fatalf("want %q in output (len %d):\n%.2000s", substr, len(s), s)
	}
}

func mustNotContain(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("did not want %q in output:\n%.2000s", substr, s)
	}
}

// countLine extracts the "N profile(s)" trailer listing.Render writes.
func countLine(t *testing.T, out string) int {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	fields := strings.Fields(last)
	if len(fields) < 2 {
		t.Fatalf("could not parse count line %q", last)
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("could not parse count line %q: %v", last, err)
	}
	return n
}

// assertFilter runs faws with the given groups/exclude, compares its
// reported count against the model's independently computed expectation,
// and returns the raw output for further assertions.
func assertFilter(t *testing.T, bin string, all, any, exclude []string, wantCount int) string {
	t.Helper()
	var args []string
	for _, g := range all {
		args = append(args, "--all", g)
	}
	for _, g := range any {
		args = append(args, "--any", g)
	}
	for _, x := range exclude {
		args = append(args, "--exclude", x)
	}
	r := run(t, bin, args...)
	if wantCount == 0 {
		if r.code != 4 {
			t.Fatalf("args %v: want exit 4 (no match), got %d:\n%.2000s", args, r.code, r.out)
		}
		return r.out
	}
	if r.code != 0 {
		t.Fatalf("args %v: want exit 0, got %d:\n%.2000s", args, r.code, r.out)
	}
	got := countLine(t, r.out)
	if got != wantCount {
		t.Fatalf("args %v: want %d profiles, got %d:\n%.2000s", args, wantCount, got, r.out)
	}
	return r.out
}

// groupsOf turns a flat --all-style slice of comma strings into the [][]string
// shape splitTerms/matchesAllTerms expects, purely for readability at call sites.
func groupsOf(allTerms ...string) [][]string {
	var out [][]string
	for _, g := range allTerms {
		out = append(out, strings.Split(g, ","))
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. Parse correctness.
// ---------------------------------------------------------------------------

func TestParseCounts(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	r := run(t, bin)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	got := countLine(t, r.out)
	if got != expectedProfile {
		t.Fatalf("want %d total profiles (base %d + adversarial %d + credsonly %d), got %d",
			expectedProfile, basePopulation, numAdversarial, numCredsOnly, got)
	}

	// [sso-session ...] / [services ...] must not appear as profiles.
	mustNotContain(t, r.out, "scale-session")
	mustNotContain(t, r.out, "scale-services")

	// Item 7 regression guard, at scale: an unfiltered listing resolves
	// every one of this fixture's 4 deliberately colliding names (see
	// collisionNotes), so faws must warn about all 4 — naming them — on
	// stderr, ahead of the listing rows themselves.
	for _, name := range []string{"nimbus", "shouty-scale-a", "mfa-a/ReadOnly", "org05-acct3/Admin"} {
		mustContain(t, r.out, name)
	}
	if !strings.Contains(r.out, "warning:") {
		t.Fatalf("want a warning about profiles present in both files:\n%.500s", r.out)
	}

	// No duplicate profile names in the listing (first tab-separated field of
	// every line except the leading warning line and the trailing count
	// line).
	lines := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "warning:") {
		t.Fatalf("want the collision warning as the first line, got %q", lines[0])
	}
	lines = lines[1 : len(lines)-1] // drop the warning line and the count line
	seen := map[string]bool{}
	for _, line := range lines {
		name := strings.SplitN(line, "\t", 2)[0]
		if seen[name] {
			t.Fatalf("duplicate profile name in listing: %q", name)
		}
		seen[name] = true
	}
	if len(seen) != expectedProfile {
		t.Fatalf("want %d distinct listed names, got %d", expectedProfile, len(seen))
	}
}

// TestCollisionPrecedence proves, for all 4 deliberately colliding names,
// that the config-file version wins outright: none of the credentials-file
// "note" markers ever surface through a filter.
func TestCollisionPrecedence(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	if len(collisionNotes) != numCollisions {
		t.Fatalf("want %d collision markers, have %d", numCollisions, len(collisionNotes))
	}

	for _, note := range collisionNotes {
		t.Run(note, func(t *testing.T) {
			assertFilter(t, bin, []string{note}, nil, nil, 0)
		})
	}

	// And the config version's own data is still reachable: "nimbus"'s
	// config role (ReadOnly) still matches, proving the section wasn't
	// dropped entirely, only the credentials-file copy of it.
	want := expectedCount(fullModel(), groupsOf("nimbus,readonly"), nil)
	assertFilter(t, bin, []string{"nimbus,readonly"}, nil, nil, want)
}

// ---------------------------------------------------------------------------
// 2. Filter counts computable from the generator's own rules.
// ---------------------------------------------------------------------------

func TestFilterCountsComputable(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)
	model := fullModel()

	t.Run("all_readonly", func(t *testing.T) {
		want := expectedCount(model, groupsOf("readonly"), nil)
		assertFilter(t, bin, []string{"readonly"}, nil, nil, want)
	})

	t.Run("all_org07", func(t *testing.T) {
		want := expectedCount(model, groupsOf("org07"), nil)
		if want != acctsPerOrg*rolesPerAcct {
			t.Fatalf("model sanity: want %d org07 profiles, model says %d", acctsPerOrg*rolesPerAcct, want)
		}
		assertFilter(t, bin, []string{"org07"}, nil, nil, want)
	})

	t.Run("all_readonly_exclude_org07", func(t *testing.T) {
		want := expectedCount(model, groupsOf("readonly"), []string{"org07"})
		assertFilter(t, bin, []string{"readonly"}, nil, []string{"org07"}, want)
	})

	t.Run("two_group_or", func(t *testing.T) {
		// --all org03,readonly --all org19,poweruser: two independently-ANDed
		// groups, ORed together. "readonly" is a substring of "ReadOnlyPlus"
		// too, so org03's contribution is 2 roles x 5 accounts = 10, not 5;
		// org19's is exactly 1 role x 5 accounts = 5. Total 15.
		want := expectedCount(model, groupsOf("org03,readonly", "org19,poweruser"), nil)
		if want != acctsPerOrg*2+acctsPerOrg {
			t.Fatalf("model sanity: want %d, model says %d", acctsPerOrg*2+acctsPerOrg, want)
		}
		assertFilter(t, bin, []string{"org03,readonly", "org19,poweruser"}, nil, nil, want)
	})

	t.Run("account_id_filter", func(t *testing.T) {
		// org11-acct2's account id, 12 digits: 11 + 00000000 + 02.
		id := "110000000002"
		want := expectedCount(model, groupsOf(id), nil)
		if want != rolesPerAcct {
			t.Fatalf("model sanity: want %d, model says %d", rolesPerAcct, want)
		}
		assertFilter(t, bin, []string{id}, nil, nil, want)
	})

	t.Run("near_identical_account_ids_precise", func(t *testing.T) {
		// acctclose-a/b/c share the prefix 9900000000 and differ only in the
		// last two digits (10/11/12). Filtering on the full id must select
		// exactly one, never its siblings.
		for _, id := range []string{"990000000010", "990000000011", "990000000012"} {
			want := expectedCount(model, groupsOf(id), nil)
			if want != 1 {
				t.Fatalf("model sanity: id %s should be unique, model says %d", id, want)
			}
			assertFilter(t, bin, []string{id}, nil, nil, 1)
		}
	})

	t.Run("secret_exclusion_zero_match", func(t *testing.T) {
		// These substrings exist ONLY inside shouty-scale-a/b's uppercase
		// secret VALUES. A correct faws must find nothing.
		for _, term := range []string{"scale-bait-readonly", "scale-bait-admin"} {
			want := expectedCount(model, groupsOf(term), nil)
			if want != 0 {
				t.Fatalf("model sanity: %q should match nothing, model says %d", term, want)
			}
			assertFilter(t, bin, []string{term}, nil, nil, 0)
		}
	})
}

// ---------------------------------------------------------------------------
// 3. The adversarial rows behave.
// ---------------------------------------------------------------------------

func TestAdversarialRows(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)
	model := fullModel()

	t.Run("prefix_pair_exact", func(t *testing.T) {
		// "nimbus" as a filter term substring-matches BOTH nimbus and
		// nimbus-extra: that is correct substring semantics, not
		// over-selection — assert it explicitly so a future "helpful"
		// exact-match rewrite doesn't silently break this fixture's
		// expectations either way. (Deliberately not named "app": every
		// SSO-style profile's granted_sso_start_url contains "awsapps.com",
		// which itself contains "app" — see gen-fixture.sh's comment.)
		want := expectedCount(model, groupsOf("nimbus"), nil)
		if want != 2 {
			t.Fatalf("model sanity: want 2 (nimbus, nimbus-extra), got %d", want)
		}
		out := assertFilter(t, bin, []string{"nimbus"}, nil, nil, want)
		mustContain(t, out, "nimbus-extra")
	})

	t.Run("role_substring_chain", func(t *testing.T) {
		// "deploy" matches all three: Deploy, DeployAdmin, DeployAdminPlus.
		want := expectedCount(model, groupsOf("deploy"), nil)
		if want != 3 {
			t.Fatalf("model sanity: want 3, got %d", want)
		}
		out := assertFilter(t, bin, []string{"deploy"}, nil, nil, want)
		mustContain(t, out, "roletest/Deploy")
		mustContain(t, out, "roletest/DeployAdmin")
		mustContain(t, out, "roletest/DeployAdminPlus")
	})

	t.Run("unicode_names_match_by_own_substring", func(t *testing.T) {
		out := assertFilter(t, bin, []string{"café"}, nil, nil, 1)
		mustContain(t, out, "café-prod/ReadOnly")

		// A single multi-byte CJK glyph, itself a valid substring of the
		// full two-glyph name: strings.Contains must still work at a rune
		// boundary within a UTF-8 byte sequence.
		out = assertFilter(t, bin, []string{"京"}, nil, nil, 1)
		mustContain(t, out, "東京-dev/ReadOnly")
	})

	t.Run("long_name_truncated_in_listing_display", func(t *testing.T) {
		// This name is 238 runes. Before the display-truncation fix,
		// text/tabwriter padded every row in the listing to this one
		// outlier's width (see internal/listing). The filter still matches
		// against the full, untruncated name — only the rendered listing
		// column is capped.
		longx := strings.Repeat("x", 220)
		longname := "longname-" + longx + "/ReadOnly"
		out := assertFilter(t, bin, []string{"longname-" + longx}, nil, nil, 1)

		if strings.Contains(out, longname) {
			t.Fatalf("listing rendered the full 238-rune name verbatim; display truncation regressed:\n%.300s", out)
		}
		if !strings.Contains(out, "longname-") {
			t.Fatalf("expected the leading segment to survive truncation:\n%.300s", out)
		}
		if !strings.Contains(out, "/ReadOnly") {
			t.Fatalf("expected the trailing /Role to survive truncation:\n%.300s", out)
		}
		if !strings.Contains(out, "…") {
			t.Fatalf("expected a middle ellipsis in the truncated name:\n%.300s", out)
		}
		// No listing row - this one included - should come anywhere near
		// the outlier's raw 238-rune name; that is the whole point of the fix.
		for _, line := range strings.Split(out, "\n") {
			if n := utf8.RuneCountInString(line); n > 120 {
				t.Fatalf("listing row was not capped by display truncation (%d runes): %.150s", n, line)
			}
		}
	})

	t.Run("empty_role_lists_blank_not_crash", func(t *testing.T) {
		out := assertFilter(t, bin, []string{"emptyrole-profile"}, nil, nil, 1)
		mustContain(t, out, "emptyrole-profile")
		// Blank role column: two tabs in a row somewhere on this profile's line
		// (account\t<blank-role>\tregion), and the request must still exit 0.
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "emptyrole-profile") {
				if !strings.Contains(line, "990000000040") {
					t.Fatalf("expected account id column intact: %q", line)
				}
			}
		}
	})

	t.Run("role_chains_visible_never_vended_by_default", func(t *testing.T) {
		// Visible to the filter...
		out := assertFilter(t, bin, []string{"chain-"}, nil, nil, 2)
		mustContain(t, out, "chain-a")
		mustContain(t, out, "chain-b")
		// ...but this is a filter/listing assertion only; no -o is passed
		// anywhere in this subtest, so nothing gets vended (a real vend of a
		// role_arn+source_profile chain would need genuine sts:AssumeRole).
	})

	t.Run("mfa_and_oddcase_reachable", func(t *testing.T) {
		assertFilter(t, bin, []string{"990000000060"}, nil, nil, 1)        // mfa-a
		out := assertFilter(t, bin, []string{"990000000070"}, nil, nil, 1) // casecheck-a
		mustContain(t, out, "casecheck-a/ReadOnly")
		mustContain(t, out, "990000000070") // AccountID() correctly read Granted_SSO_Account_Id despite odd case
		mustContain(t, out, "ReadOnly")     // RoleName() correctly read Granted_SSO_Role_Name despite odd case
	})
}

// ---------------------------------------------------------------------------
// 6. Scale behaviour: parse+filter+list well under a ceiling; record actual.
// ---------------------------------------------------------------------------

func TestParseFilterListTiming(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	setupFixture(t)

	const ceiling = 2 * time.Second

	start := time.Now()
	r := run(t, bin)
	elapsed := time.Since(start)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	t.Logf("parse+filter(none)+list of %d profiles: %s", expectedProfile, elapsed)
	if elapsed > ceiling {
		t.Fatalf("parse+list took %s, want under %s", elapsed, ceiling)
	}

	start = time.Now()
	r = run(t, bin, "--all", "readonly")
	elapsed = time.Since(start)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	t.Logf("parse+filter(readonly, broad)+list: %s", elapsed)
	if elapsed > ceiling {
		t.Fatalf("broad filter took %s, want under %s", elapsed, ceiling)
	}

	start = time.Now()
	r = run(t, bin, "--all", "990000000010")
	elapsed = time.Since(start)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	t.Logf("parse+filter(exact account id, narrow)+list: %s", elapsed)
	if elapsed > ceiling {
		t.Fatalf("narrow filter took %s, want under %s", elapsed, ceiling)
	}
}

// ---------------------------------------------------------------------------
// 7-9. Vend a 25-profile subset, refresh it, clear it.
// ---------------------------------------------------------------------------

// twentyFiveNames returns 25 distinct, all-vendable (credential_process
// backed) profile names: org00..org04's full 20 profiles, plus 5 more
// adversarial-but-vendable ones. Excludes chain-a/b and shouty-scale-a/b,
// which carry no credential_process and must never be vended.
func twentyFiveNames() []string {
	names := make([]string, 0, 25)
	for acct := 0; acct < acctsPerOrg; acct++ {
		for _, role := range roles {
			names = append(names, fmt.Sprintf("org00-acct%d/%s", acct, role))
		}
	}
	names = append(names,
		"nimbus", "nimbus-extra", "café-prod/ReadOnly", "東京-dev/ReadOnly", "emptyrole-profile",
	)
	sort.Strings(names) // stable, arbitrary order for the exclude-based selection below
	return names
}

func TestVend25ThenRefreshThenClear(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	names := twentyFiveNames()
	if len(names) != 25 {
		t.Fatalf("want 25 names, got %d", len(names))
	}

	// Select exactly these 25 via --any (one OR-term per name); every term
	// is an exact, non-overlapping profile name in this fixture.
	args := []string{}
	for _, n := range names {
		args = append(args, "--any", n)
	}
	out := filepath.Join(fx.vendDir, "sub25.config")
	start := time.Now()
	r := run(t, bin, append(args, "-o", out)...)
	elapsed := time.Since(start)
	if r.code != 0 {
		t.Fatalf("vend 25: want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	t.Logf("vend 25 profiles: %s total, %s/profile", elapsed, elapsed/25)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, n := range names {
		mustContain(t, content, fmt.Sprintf("[profile %s]", n))
	}
	if got := strings.Count(content, "aws_access_key_id ="); got != 25 {
		t.Fatalf("want 25 well-formed blocks, got %d access-key lines", got)
	}
	if got := strings.Count(content, "aws_secret_access_key ="); got != 25 {
		t.Fatalf("want 25 secret-key lines, got %d", got)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want mode 0600, got %o", perm)
	}
	if !strings.Contains(content, "# faws-filter: ") {
		t.Fatal("want the faws-filter marker for --refresh/--clear to recognise this file")
	}

	// --refresh: all 25 blocks must survive.
	r = run(t, bin, "--refresh", out)
	if r.code != 0 {
		t.Fatalf("refresh: want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	data, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content = string(data)
	for _, n := range names {
		mustContain(t, content, fmt.Sprintf("[profile %s]", n))
	}
	if got := strings.Count(content, "aws_access_key_id ="); got != 25 {
		t.Fatalf("after refresh: want 25 blocks, got %d", got)
	}

	// --clear: the file must go away.
	r = run(t, bin, "--clear", out)
	if r.code != 0 {
		t.Fatalf("clear: want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("want %s gone after --clear, stat err = %v", out, err)
	}
}

// ---------------------------------------------------------------------------
// 10. MFA notice at scale.
// ---------------------------------------------------------------------------

func TestMFANoticeAtScale(t *testing.T) {
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	// A filter selecting both mfa-a/ReadOnly and mfa-b/PowerUser (2 MFA
	// profiles) plus org00's 20 non-MFA profiles: 22 total, 2 requiring MFA.
	args := []string{"--any", "org00", "--any", "mfa-a/readonly", "--any", "mfa-b/poweruser"}
	out := filepath.Join(fx.vendDir, "mfa.config")
	r := run(t, bin, append(args, "-o", out)...)
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	mustContain(t, r.out, "2 of 22 profiles require MFA; you will be prompted 2 times.")

	// Progress lines are emitted in vend order: non-MFA profiles first (all
	// 20 org00 ones), MFA profiles last (mfa-a, mfa-b) — verify the two MFA
	// profiles' progress lines are the LAST two of the 22.
	var progressProfiles []string
	for _, line := range strings.Split(r.out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			// "  [N/22] profile-name"
			parts := strings.SplitN(line, "]", 2)
			if len(parts) == 2 {
				progressProfiles = append(progressProfiles, strings.TrimSpace(parts[1]))
			}
		}
	}
	if len(progressProfiles) != 22 {
		t.Fatalf("want 22 progress lines, got %d:\n%.2000s", len(progressProfiles), r.out)
	}
	lastTwo := progressProfiles[len(progressProfiles)-2:]
	if lastTwo[0] != "mfa-a/ReadOnly" || lastTwo[1] != "mfa-b/PowerUser" {
		t.Fatalf("want mfa-a/ReadOnly then mfa-b/PowerUser last, got %v", lastTwo)
	}
}

// ---------------------------------------------------------------------------
// The SLOW full-fixture vend. Gated behind FAWS_SCALE_VEND=1 so `go test
// ./...` stays fast; run explicitly to get a real timing measurement.
// ---------------------------------------------------------------------------

// vendableNames returns every config profile that carries a credential_process
// (i.e. can actually be vended without a network call): base 480 + 16 of the
// 20 adversarial ones. chain-a/b (role_arn chains) and shouty-scale-a/b
// (static-secret profiles with no resolver) are excluded on purpose.
func vendableNames() []string {
	names := make([]string, 0, basePopulation+16)
	for _, p := range baseModel() {
		names = append(names, p.name)
	}
	for _, p := range adversarialModel() {
		switch p.name {
		case "chain-a", "chain-b", "shouty-scale-a", "shouty-scale-b":
			continue
		}
		names = append(names, p.name)
	}
	return names
}

func TestFullScaleVend(t *testing.T) {
	if os.Getenv("FAWS_SCALE_VEND") != "1" {
		t.Skip("set FAWS_SCALE_VEND=1 to run the full (slow) vend")
	}
	requireAWS(t)
	bin := binary(t)
	fx := setupFixture(t)

	names := vendableNames()
	wantN := basePopulation + 16
	if len(names) != wantN {
		t.Fatalf("want %d vendable names, got %d", wantN, len(names))
	}

	args := make([]string, 0, len(names)*2+2)
	for _, n := range names {
		args = append(args, "--any", n)
	}
	out := filepath.Join(fx.vendDir, "full.config")
	args = append(args, "-o", out)

	start := time.Now()
	r := run(t, bin, args...)
	elapsed := time.Since(start)
	if r.code != 0 {
		t.Fatalf("full vend: want exit 0, got %d:\n%.2000s", r.code, r.out)
	}
	perProfile := elapsed / time.Duration(len(names))
	t.Logf("FULL VEND: %d profiles in %s (%s/profile)", len(names), elapsed, perProfile)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if got := strings.Count(content, "aws_access_key_id ="); got != len(names) {
		t.Fatalf("want %d blocks, got %d", len(names), got)
	}

	writeTimingReport(t, elapsed, len(names))
}

// writeTimingReport appends the measured full-vend numbers to
// e2e/scale/out/TIMING.txt, which is otherwise written by
// e2e/scale/run-samples.sh with the extrapolated (not measured) figure. This
// is the one place a real FAWS_SCALE_VEND=1 run overwrites that extrapolation
// with a genuine measurement.
func writeTimingReport(t *testing.T, elapsed time.Duration, n int) {
	t.Helper()
	scaleDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scaleDir, "out", "MEASURED-FULL-VEND.txt")
	perProfile := elapsed / time.Duration(n)
	body := fmt.Sprintf(
		"Measured full-scale vend (FAWS_SCALE_VEND=1), %s\n"+
			"profiles vended: %d\n"+
			"total wall time: %s\n"+
			"mean per profile: %s\n",
		time.Now().UTC().Format(time.RFC3339), n, elapsed, perProfile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
