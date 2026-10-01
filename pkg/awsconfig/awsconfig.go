// Package awsconfig parses the shared AWS config and credentials files,
// retaining every key/value in each section. It deliberately knows nothing
// about what the keys mean — filtering matches raw values.
package awsconfig

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// Profile is one section of an AWS config or credentials file.
type Profile struct {
	Name   string            // section name, without any "profile " prefix
	Source string            // path of the file it came from
	Keys   map[string]string // every key in the section
	Order  []string          // key order as written, for stable display

	// Nested holds settings written indented under a parent key, e.g.
	//
	//	s3 =
	//	  max_concurrent_requests = 20
	//	  addressing_style = path
	//
	// keyed by the parent ("s3" above). The parent itself still appears in
	// Keys/Order with its own (usually empty) value, exactly as written —
	// Nested only adds the children the old flat parse used to lose by
	// promoting them to bogus top-level keys (item 3 of the review-2 brief:
	// a botocore-nested setting vended as an empty `s3 =` plus two
	// unreachable top-level keys, and `aws configure get
	// s3.max_concurrent_requests` returned nothing). Not populated for
	// non-profile sections ([sso-session ...], [services ...]), same as Keys.
	Nested map[string]NestedSection
}

// NestedSection is the indented key/value block recorded under one parent
// key in Profile.Nested.
type NestedSection struct {
	Keys  map[string]string // child key -> value
	Order []string          // child key order as written, for stable display
}

// Get returns the value for key, or "".
func (p Profile) Get(key string) string { return p.Keys[key] }

// GetNested returns the value of child under parent (e.g. GetNested("s3",
// "max_concurrent_requests")), or "" if either is absent.
func (p Profile) GetNested(parent, child string) string {
	return p.Nested[parent].Keys[child]
}

// ParseFile reads one AWS config/credentials file. A missing file yields no
// profiles and no error — callers read two files and either may be absent.
func ParseFile(path string) ([]Profile, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the caller's own AWS config path
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var (
		out []Profile
		cur *Profile
		// lastTopKey is the most recently seen top-level (unindented) key in
		// the current section; an indented line attaches to it as a nested
		// child. Reset whenever a new section starts.
		lastTopKey string
	)
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			lastTopKey = ""
			name := strings.TrimSpace(line[1 : len(line)-1])
			// Only "profile" (config file) and "sso-session"/"services"
			// (also config-only) are keyword-prefixed section forms — and
			// only "profile <name>" is shlex-tokenized by botocore, which is
			// why the name gets split off here rather than treated as one
			// literal string. A bare credentials-file section can itself
			// contain a space (typically because it's quoted, e.g. a
			// section faws wrote for a profile whose own name has one — see
			// pkg/output.quoteSectionName), and that is NOT one of these
			// keyword forms, so it must fall through unsplit rather than be
			// misread as some other unknown keyword and silently dropped as
			// "not a profile" — a real regression this exact fix hit while
			// wiring up item 3/5's own tests.
			if head, rest, ok := strings.Cut(name, " "); ok {
				switch head {
				case "profile":
					name = strings.TrimSpace(rest)
				case "sso-session", "services":
					continue
				}
			}
			// botocore shlex-parses the section name, so [profile "quoted
			// name"] becomes the profile "quoted name", not "quoted name"
			// including the quote characters. Match that for the common
			// case of one matching pair of surrounding quotes; without
			// this, --profile "quoted name" (unquoted, as the CLI would
			// take it) can never match what we parsed.
			name = stripQuotes(name)
			cur = &Profile{Name: name, Source: path, Keys: map[string]string{}}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := cutKV(line)
		if !ok {
			continue
		}
		// A line indented relative to the section's own margin, under a
		// parent key already seen, is a nested setting (item 3): record it
		// under that parent instead of promoting it to a bogus top-level
		// key. leadingSpace is measured on the raw (untrimmed) line, since
		// `line` above already had it stripped.
		if leadingSpace(raw) && lastTopKey != "" {
			if cur.Nested == nil {
				cur.Nested = map[string]NestedSection{}
			}
			ns := cur.Nested[lastTopKey]
			if ns.Keys == nil {
				ns.Keys = map[string]string{}
			}
			if _, seen := ns.Keys[key]; !seen {
				ns.Order = append(ns.Order, key)
			}
			ns.Keys[key] = val
			cur.Nested[lastTopKey] = ns
			continue
		}
		if _, seen := cur.Keys[key]; !seen {
			cur.Order = append(cur.Order, key)
		}
		cur.Keys[key] = val
		lastTopKey = key
	}
	flush()
	return out, sc.Err()
}

// leadingSpace reports whether raw starts with a space or tab — i.e. is
// indented relative to a section's own left margin.
func leadingSpace(raw string) bool {
	return len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t')
}

// cutKV splits "key = value". It does NOT strip a trailing # or ; from the
// value: botocore does not do that either (verified: `region = us-east-1 #
// prod comment` returns the full string "us-east-1 # prod comment" from
// `aws configure get region`), so stripping here would make our parse
// diverge from what the CLI actually resolves — for example, truncating
// `credential_process = sh -c 'a; b'` at the semicolon. Whole-line comments
// (a line whose first non-space character is # or ;) are still skipped, in
// ParseFile's scan loop; that part matches the CLI too.
//
// The key is lowercased. AWS config keys are conventionally lowercase, and
// every internal lookup (Get, AccountID, RoleName, Region, NeedsMFA, and
// filter.SecretKeys) uses lowercase literals — without normalising here, a
// section written with e.g. AWS_SECRET_ACCESS_KEY in uppercase would bypass
// the secret-key exclusion in pkg/filter and get matched against, reinstating
// the oracle that exclusion exists to close.
func cutKV(line string) (key, val string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	val = strings.TrimSpace(line[i+1:])
	return key, val, key != ""
}

// stripQuotes removes one matching pair of surrounding quotes (double or
// single) from s, leaving anything else untouched.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ConfigPath honours AWS_CONFIG_FILE, else ~/.aws/config.
func ConfigPath() string { return awsPath("AWS_CONFIG_FILE", "config") }

// CredentialsPath honours AWS_SHARED_CREDENTIALS_FILE, else ~/.aws/credentials.
func CredentialsPath() string { return awsPath("AWS_SHARED_CREDENTIALS_FILE", "credentials") }

func awsPath(envVar, base string) string {
	if p := os.Getenv(envVar); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return base
	}
	return filepath.Join(home, ".aws", base)
}

// Load reads both shared files. When a profile name appears in both, the
// config file wins and the credentials entry is dropped — config is the file
// users edit deliberately.
func Load() ([]Profile, error) {
	cfg, err := ParseFile(ConfigPath())
	if err != nil {
		return nil, err
	}
	creds, err := ParseFile(CredentialsPath())
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(cfg))
	for _, p := range cfg {
		seen[p.Name] = true
	}
	for _, p := range creds {
		if !seen[p.Name] {
			cfg = append(cfg, p)
		}
	}
	return cfg, nil
}

// AccountID returns the account number for display: sso_account_id (or the
// granted_ variant), else the account embedded in role_arn. Empty when the
// profile carries neither, as static-key profiles do.
func (p Profile) AccountID() string {
	for _, k := range []string{"sso_account_id", "granted_sso_account_id"} {
		if v := p.Keys[k]; v != "" {
			return v
		}
	}
	acct, _ := parseRoleARN(p.Keys["role_arn"])
	return acct
}

// RoleName returns the role for display: sso_role_name (or the granted_
// variant), else the role embedded in role_arn.
func (p Profile) RoleName() string {
	for _, k := range []string{"sso_role_name", "granted_sso_role_name"} {
		if v := p.Keys[k]; v != "" {
			return v
		}
	}
	_, role := parseRoleARN(p.Keys["role_arn"])
	return role
}

// Region returns the profile's operating region. Never sso_region, which is
// where the Identity Center instance lives, not where workloads run.
func (p Profile) Region() string { return p.Keys["region"] }

// NeedsMFA reports whether resolving this profile will prompt for an MFA code.
func (p Profile) NeedsMFA() bool { return p.Keys["mfa_serial"] != "" }

// parseRoleARN pulls the account id and role name out of
// arn:aws:iam::222222222222:role/AdminRole. Returns empty strings when arn
// is not that shape.
func parseRoleARN(arn string) (account, role string) {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || parts[0] != "arn" {
		return "", ""
	}
	account = parts[4]
	resource := parts[5]
	name, ok := strings.CutPrefix(resource, "role/")
	if !ok {
		return account, ""
	}
	// role/path/to/Name -> Name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return account, name
}
