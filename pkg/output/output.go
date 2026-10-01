// Package output renders vended credentials and writes them atomically.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/kensantoso/faws/pkg/vend"
)

// FilterMarker prefixes the comment line recording the filter that produced a
// file, so --refresh can re-run it.
const FilterMarker = "# faws-filter: "

// Options controls rendering.
type Options struct {
	Format     string            // config | credentials | env | json
	Default    string            // profile that also gets a [default] block
	Extra      map[string]string // --region / --output / -c key=value
	ExtraOrder []string          // stable order for Extra
	NoCopy     bool              // --no-copy: never copy a source profile's own keys
	FilterLine string            // recorded for --refresh; file formats only

	// RecordFilter makes renderINI emit the FilterMarker line even when
	// FilterLine is empty, so a no-filter vend still produces a file that
	// --refresh and --clear recognise. Set by WriteFile only; Render to
	// stdout leaves it false.
	RecordFilter bool
}

// Render writes creds in the requested format.
func Render(w io.Writer, creds []vend.Creds, opts Options) error {
	if opts.Default != "" && (opts.Format == "json" || opts.Format == "env") {
		return fmt.Errorf("--default has no effect with --format %s; it has no concept of a default profile", opts.Format)
	}
	if opts.Default != "" && !hasProfile(creds, opts.Default) {
		return fmt.Errorf("--default %q is not among the %d resolved profiles", opts.Default, len(creds))
	}
	switch opts.Format {
	case "", "config":
		return renderINI(w, creds, opts, true)
	case "credentials":
		return renderINI(w, creds, opts, false)
	case "env":
		return renderEnv(w, creds, opts)
	case "json":
		return renderJSON(w, creds)
	default:
		return fmt.Errorf("unknown format %q (want config, credentials, env or json)", opts.Format)
	}
}

func renderINI(w io.Writer, creds []vend.Creds, opts Options, profilePrefix bool) error {
	if opts.RecordFilter {
		fmt.Fprintf(w, "%s%s\n\n", FilterMarker, opts.FilterLine)
	}
	for _, c := range creds {
		writeBlock(w, c.Profile, profilePrefix, c, opts)
	}
	if opts.Default != "" {
		for _, c := range creds {
			if c.Profile == opts.Default {
				writeBlock(w, "default", false, c, opts)
				break
			}
		}
	}
	return nil
}

// sectionNameNeedsQuoting reports whether name would not round-trip through
// botocore's shlex-based section-header parsing unquoted. botocore splits
// `[profile NAME]`'s contents with Python's shlex, which treats whitespace
// (and quote/backslash characters) specially; a name containing any of them,
// written bare, gets silently mis-split (a space, in particular, was
// confirmed to make `aws configure list-profiles` omit the profile entirely
// — not error, just silently drop it).
func sectionNameNeedsQuoting(name string) bool {
	return strings.ContainsFunc(name, unicode.IsSpace) || strings.ContainsAny(name, `"'\`)
}

// quoteSectionName double-quotes name if it needs it (see
// sectionNameNeedsQuoting), escaping embedded backslashes and double quotes
// the way shlex expects inside a double-quoted token. Left alone otherwise,
// so the overwhelming majority of profile names round-trip byte for byte.
func quoteSectionName(name string) string {
	if !sectionNameNeedsQuoting(name) {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range name {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func writeBlock(w io.Writer, name string, profilePrefix bool, c vend.Creds, opts Options) {
	header := quoteSectionName(name)
	if profilePrefix {
		header = "profile " + header
	}
	fmt.Fprintf(w, "[%s]\n", header)
	fmt.Fprintf(w, "aws_access_key_id = %s\n", c.AccessKeyID)
	fmt.Fprintf(w, "aws_secret_access_key = %s\n", c.SecretAccessKey)
	if c.SessionToken != "" {
		fmt.Fprintf(w, "aws_session_token = %s\n", c.SessionToken)
	}
	values, order := effectiveKeys(c, opts)
	for _, k := range order {
		fmt.Fprintf(w, "%s = %s\n", k, values[k])
		writeNested(w, k, c, opts)
	}
	if c.Expiration != "" {
		fmt.Fprintf(w, "# expires %s\n", c.Expiration)
	}
	fmt.Fprintln(w)
}

// writeNested re-emits a botocore-nested setting (item 3) indented under its
// parent key, keeping faws's not-lossy principle: parsing a nested block and
// re-emitting it as bare top-level keys would silently detach the tuning
// from the parent the CLI expects it under (`aws configure get
// s3.max_concurrent_requests` reads the nested form, not a same-named
// top-level key).
//
// Only runs for a key that was actually COPIED from the source profile:
// --no-copy suppresses it like every other copied key, and a key the caller
// overrode with an explicit --region/--output/-c of the same name (unlikely
// for something like "s3", but not impossible) keeps only that flat value —
// the nested children belonged to the copied value, not the explicit one
// replacing it.
func writeNested(w io.Writer, parent string, c vend.Creds, opts Options) {
	if opts.NoCopy {
		return
	}
	if _, explicit := opts.Extra[parent]; explicit {
		return
	}
	keys := c.SourceNested[parent]
	if len(keys) == 0 {
		return
	}
	for _, ck := range c.SourceNestedOrder[parent] {
		fmt.Fprintf(w, "  %s = %s\n", ck, keys[ck])
	}
}

// denyKeys are the keys that describe how a profile's credentials were
// OBTAINED (SSO, role assumption, credential_process, MFA, EC2 metadata
// tuning) or that ARE the credentials themselves. effectiveKeys never copies
// these into a vended block: copying an acquisition key would make the
// consumer try to re-resolve, which is the exact thing faws exists to
// prevent, and copying a credential key would duplicate (or contradict) the
// ones already vended.
//
// This is a deny-list, not an allow-list, deliberately: an allow-list would
// silently start losing information the moment AWS adds a new client
// setting — region, output, ca_bundle, retry_mode, max_attempts, endpoint_url,
// use_fips_endpoint, use_dualstack_endpoint, sts_regional_endpoints,
// parameter_validation, s3_*, and anything unrecognised are all meant to
// survive. That lossiness — silently dropping a profile's own region — is the
// bug this deny-list fixes.
var denyKeys = map[string]bool{
	"sso_start_url":                      true,
	"sso_region":                         true,
	"sso_account_id":                     true,
	"sso_role_name":                      true,
	"sso_session":                        true,
	"sso_registration_scopes":            true,
	"role_arn":                           true,
	"source_profile":                     true,
	"credential_source":                  true,
	"credential_process":                 true,
	"mfa_serial":                         true,
	"external_id":                        true,
	"duration_seconds":                   true,
	"role_session_name":                  true,
	"web_identity_token_file":            true,
	"ec2_metadata_service_endpoint":      true,
	"ec2_metadata_service_endpoint_mode": true,
	"ec2_metadata_v1_disabled":           true,
	"metadata_service_num_attempts":      true,
	"metadata_service_timeout":           true,
	"aws_access_key_id":                  true,
	"aws_secret_access_key":              true,
	"aws_session_token":                  true,

	// services references a [services NAME] section elsewhere in the source
	// config; faws never copies that section along (copying whole
	// non-profile sections is a bigger change, deliberately out of scope for
	// this wave — see the README). Copying the bare `services = NAME` key
	// without it produces a file the real AWS CLI rejects outright ("the
	// services configuration does not exist"), which is worse than silently
	// omitting a rarely-used key: a broken file fails loudly for every
	// command the consumer runs, not just the one that touches the missing
	// section.
	"services": true,
}

// grantedSSOPrefix catches every granted_sso_* key (granted's own acquisition
// keys: granted_sso_start_url, granted_sso_region, granted_sso_account_id,
// granted_sso_role_name, and any future one it adds) — the "every
// granted_sso_*" entry in the deny-list.
const grantedSSOPrefix = "granted_sso_"

func copyable(key string) bool {
	return !denyKeys[key] && !strings.HasPrefix(key, grantedSSOPrefix)
}

// Denied reports whether key is on the deny-list, so callers can reject it
// in explicit flags too, not only when copying source keys.
func Denied(key string) bool {
	return !copyable(strings.ToLower(key))
}

// effectiveKeys merges c's own source keys (vend.Creds.SourceKeys/
// SourceOrder: every key its source profile carried, credentials and
// acquisition keys included — see cmd's assembly of Creds) with the explicit
// --region/--output/-c values in opts, and returns the result as a value map
// plus a stable write order. Deny-listed and secret keys from the source are
// dropped here, at the one point everything destined for a vended block
// passes through, rather than trusting every caller to have filtered first.
//
// Precedence: a copied source value loses to an explicit flag of the same
// name — the rule the review brief states directly: "copied source value <
// --region/--output/-c (explicit flags win)". --no-copy suppresses the
// copied values entirely but never the explicit ones, since it only
// disables copying, not the flags a user typed on this exact invocation.
//
// Both config AND credentials formats call this: the AWS CLI reads region
// and output from the credentials file too (verified: `aws configure get
// region --profile X` against a credentials-only profile returns it), so
// there is no format-specific reason to withhold them there.
func effectiveKeys(c vend.Creds, opts Options) (values map[string]string, order []string) {
	values = map[string]string{}
	if !opts.NoCopy {
		for _, k := range c.SourceOrder {
			if !copyable(k) {
				continue
			}
			values[k] = c.SourceKeys[k]
			order = append(order, k)
		}
	}
	for _, k := range opts.ExtraOrder {
		if _, seen := values[k]; !seen {
			order = append(order, k)
		}
		values[k] = opts.Extra[k] // explicit flags always win
	}
	return values, order
}

// renderEnv writes shell export lines. Unlike JSON, "#" is a valid shell
// comment, so an env file CAN carry the FilterMarker and be refreshed/cleared
// like config and credentials files.
func renderEnv(w io.Writer, creds []vend.Creds, opts Options) error {
	if len(creds) != 1 {
		return fmt.Errorf("--format env needs exactly one profile, got %d; narrow the filter", len(creds))
	}
	if opts.RecordFilter {
		fmt.Fprintf(w, "%s%s\n\n", FilterMarker, opts.FilterLine)
	}
	c := creds[0]
	fmt.Fprintf(w, "export AWS_ACCESS_KEY_ID=%s\n", c.AccessKeyID)
	fmt.Fprintf(w, "export AWS_SECRET_ACCESS_KEY=%s\n", c.SecretAccessKey)
	if c.SessionToken != "" {
		fmt.Fprintf(w, "export AWS_SESSION_TOKEN=%s\n", c.SessionToken)
	}
	// env can only express what has an AWS_* environment variable, but that
	// still includes the profile's own region — dropping it would leave this
	// format with the exact "You must specify a region" failure the rest of
	// FIX 1 corrects for config/credentials.
	if values, _ := effectiveKeys(c, opts); values["region"] != "" {
		fmt.Fprintf(w, "export AWS_REGION=%s\n", values["region"])
	}
	return nil
}

// renderJSON never emits the FilterMarker: a "#" line makes the output
// invalid JSON, so --format json files can never be refreshed or cleared.
// This is a genuine limitation of the format, not an oversight — see
// ReadFilterLine's error for what that means for the caller.
func renderJSON(w io.Writer, creds []vend.Creds) error {
	type item struct {
		Profile         string `json:"Profile"`
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken,omitempty"`
		Expiration      string `json:"Expiration,omitempty"`
	}
	out := make([]item, 0, len(creds))
	for _, c := range creds {
		out = append(out, item{c.Profile, c.AccessKeyID, c.SecretAccessKey, c.SessionToken, c.Expiration})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func hasProfile(creds []vend.Creds, name string) bool {
	for _, c := range creds {
		if c.Profile == name {
			return true
		}
	}
	return false
}

// WriteFile renders to a temp file in the destination directory and renames
// it into place. The rename is what makes a concurrent reader safe: it never
// observes a half-written credentials file.
func WriteFile(path string, creds []vend.Creds, opts Options) error {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory; -o needs a file path to write, not a directory to write into", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	opts.RecordFilter = true
	var buf strings.Builder
	if err := Render(&buf, creds, opts); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".faws-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(buf.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
