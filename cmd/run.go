package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/kensantoso/faws/internal/listing"
	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/filter"
	"github.com/kensantoso/faws/pkg/output"
	"github.com/kensantoso/faws/pkg/vend"
)

type opts struct {
	all       []string
	any       []string
	exclude   []string
	out       string
	format    string
	defaultTo string
	region    string
	outputFmt string
	set       []string
	refresh   string
	clear     string
	noCopy    bool
	timeout   time.Duration

	save          string // --save NAME
	forget        string // --forget NAME
	force         bool   // --force: let --save replace an existing selector
	selector      string // @NAME given on the command line
	listSelectors bool   // a bare "@"
	exec          bool   // a command followed --
	parallel      int    // --parallel
	strict        bool   // --strict
	execArgv      []string
}

// invocationLine reconstructs the whole file-shaping invocation that
// produced a selection — filter terms, plus --default/--format/--region/
// --output/-c — for recording in the output file so --refresh reproduces the
// same file, not just the same profile set. Without --default/--format/
// --region here, a file written with `--default acme-dev/ReadOnly --format
// credentials --region ap-southeast-2` would refresh back with no
// `[default]` block, as plain `config` and with no region: the default
// profile silently disappears, `[dev]` silently becomes `[profile dev]`, and
// the region vanishes.
//
// --format is only recorded when it differs from the "config" default, so a
// refresh of a plain config file still round-trips to the same (empty)
// invocation as before.
func invocationLine(o opts) string {
	var parts []string
	for _, v := range o.all {
		parts = append(parts, "--all", v)
	}
	for _, v := range o.any {
		parts = append(parts, "--any", v)
	}
	for _, v := range o.exclude {
		parts = append(parts, "--exclude", v)
	}
	if o.defaultTo != "" {
		parts = append(parts, "--default", o.defaultTo)
	}
	if o.format != "" && o.format != "config" {
		parts = append(parts, "--format", o.format)
	}
	if o.region != "" {
		parts = append(parts, "--region", o.region)
	}
	if o.outputFmt != "" {
		parts = append(parts, "--output", o.outputFmt)
	}
	for _, kv := range o.set {
		parts = append(parts, "-c", kv)
	}
	if o.noCopy {
		parts = append(parts, "--no-copy")
	}
	return strings.Join(parts, " ")
}

// parseInvocationLine is the inverse of invocationLine, for --refresh. It
// returns a fresh opts carrying only the fields invocationLine records;
// fields such as out are left zero for the caller to fill in.
func parseInvocationLine(line string) (opts, error) {
	var o opts
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		// --no-copy is the one recorded flag with no value; every other
		// known flag below takes exactly one.
		if fields[i] == "--no-copy" {
			o.noCopy = true
			continue
		}
		if i+1 >= len(fields) {
			return opts{}, fmt.Errorf("recorded filter %q ends with a flag and no value", line)
		}
		flag, value := fields[i], fields[i+1]
		i++
		switch flag {
		case "--all":
			o.all = append(o.all, value)
		case "--any":
			o.any = append(o.any, value)
		case "--exclude":
			o.exclude = append(o.exclude, value)
		case "--default":
			o.defaultTo = value
		case "--format":
			o.format = value
		case "--region":
			o.region = value
		case "--output":
			o.outputFmt = value
		case "-c":
			o.set = append(o.set, value)
		default:
			return opts{}, fmt.Errorf("recorded filter %q has unknown flag %q", line, flag)
		}
	}
	return o, nil
}

// extras assembles the key/value pairs written into every block, in a stable
// order: --region, --output, then each -c in the order given.
//
// --region, --output and -c values must not contain whitespace: they are
// round-tripped through invocationLine/parseInvocationLine as
// whitespace-separated fields (mirroring the same constraint pkg/filter
// already places on filter terms), so an embedded space would silently
// corrupt a later --refresh.
func (o opts) extras() (map[string]string, []string, error) {
	m := map[string]string{}
	var order []string
	add := func(k, v string) {
		if _, seen := m[k]; !seen {
			order = append(order, k)
		}
		m[k] = v
	}
	if o.region != "" {
		if strings.ContainsFunc(o.region, unicode.IsSpace) {
			return nil, nil, errf(3, "--region %q contains whitespace; it must not, so --refresh can round-trip it", o.region)
		}
		add("region", strings.TrimSpace(o.region))
	}
	if o.outputFmt != "" {
		if strings.ContainsFunc(o.outputFmt, unicode.IsSpace) {
			return nil, nil, errf(3, "--output %q contains whitespace; it must not, so --refresh can round-trip it", o.outputFmt)
		}
		add("output", strings.TrimSpace(o.outputFmt))
	}
	for _, kv := range o.set {
		if strings.ContainsFunc(kv, unicode.IsSpace) {
			return nil, nil, errf(3, "-c %q contains whitespace; -c values must not, so --refresh can round-trip them", kv)
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, nil, errf(3, "-c %q is not key=value", kv)
		}
		// The same deny-list that stops copying source keys: a key that says how
		// to obtain credentials would make the consumer re-resolve.
		if output.Denied(strings.TrimSpace(k)) {
			return nil, nil, errf(3, "-c %s is not allowed: faws never writes credential or credential-acquisition keys into a vended file", strings.TrimSpace(k))
		}
		add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return m, order, nil
}

func addRunFlags(c *cobra.Command, o *opts) {
	f := c.Flags()
	f.StringArrayVar(&o.all, "all", nil, "comma-separated terms that must all match; repeat the flag to OR")
	f.StringArrayVar(&o.any, "any", nil, "comma-separated terms, any of which may match")
	f.StringArrayVarP(&o.exclude, "exclude", "x", nil, "comma-separated terms to drop; repeatable")
	f.StringVarP(&o.out, "out", "o", "", "write credentials here ('-' for stdout); omit to list")
	f.StringVar(&o.format, "format", "config", "config | credentials | env | json")
	f.StringVar(&o.defaultTo, "default", "", "profile that also gets a [default] block")
	f.StringVar(&o.region, "region", "", "region written into every block")
	f.StringVar(&o.outputFmt, "output", "", "output setting written into every block")
	f.StringArrayVarP(&o.set, "set", "c", nil, "extra key=value written into every block; repeatable")
	f.StringVar(&o.refresh, "refresh", "", "re-vend a file faws wrote, reusing the filter recorded in it")
	f.StringVar(&o.clear, "clear", "", "delete a credentials file faws wrote")
	f.BoolVar(&o.noCopy, "no-copy", false, "never copy a source profile's own region/output/etc. into its vended block")
	f.StringVar(&o.save, "save", "", "save this invocation's filter as @NAME (lists, never vends)")
	f.StringVar(&o.forget, "forget", "", "delete the saved selector @NAME")
	f.BoolVar(&o.force, "force", false, "let --save replace an existing selector")
	f.IntVar(&o.parallel, "parallel", 8, "profiles to vend at once (MFA profiles always vend one at a time)")
	f.BoolVar(&o.strict, "strict", false, "fail on the first profile that does not vend, instead of skipping it")
	f.DurationVar(&o.timeout, "timeout", vend.Timeout, "per-profile vend timeout for profiles that do not prompt for MFA (MFA profiles always get a longer, fixed timeout)")

	// --clear and --refresh each already fully determine the destination
	// file (the path passed to the flag itself), so combining either with
	// -o, or with each other, is nonsensical rather than merely redundant:
	// --clear silently ignored -o/--refresh, and --refresh silently
	// overrode -o. Reject the combination instead of guessing which one wins.
	c.MarkFlagsMutuallyExclusive("clear", "refresh", "out")
	// --save only records a filter and --forget touches nothing else.
	c.MarkFlagsMutuallyExclusive("save", "forget", "clear", "refresh")
	c.MarkFlagsMutuallyExclusive("save", "out")
	c.MarkFlagsMutuallyExclusive("forget", "out")
}

// mfaNotice warns before vending when the selection contains profiles that
// will prompt. The AWS CLI caches per profile, so N such profiles is N
// prompts — and TOTP codes rotate every 30s and cannot be reused across
// AssumeRole calls, so they cannot be answered back to back.
func mfaNotice(matches []filter.Match) string {
	n := 0
	for _, m := range matches {
		if m.Profile.NeedsMFA() {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d of %d profiles require MFA; you will be prompted %d times.\n"+
			"Putting mfa_serial on the shared source_profile instead reduces this to one prompt.",
		n, len(matches), n)
}

// mfaAwareTimeout is FIX 2: cmd already knows which profiles carry
// mfa_serial (mfaNotice above uses the same NeedsMFA check), so it can give
// Vend a per-call deadline instead of the flat 20s that used to SIGKILL an
// MFA prompt mid-entry. A profile with mfa_serial always gets
// vend.MFATimeout, regardless of --timeout: no value that makes sense for
// ordinary network slowness is also enough time for a human to reach their
// phone. Every other profile gets o.timeout (--timeout, defaulting to
// vend.Timeout).
func mfaAwareTimeout(o opts, p awsconfig.Profile) time.Duration {
	if p.NeedsMFA() {
		return vend.MFATimeout
	}
	return o.timeout
}

// runSelection is the whole command: load, filter, then either list or vend.
// ctx is the process-lifetime context wired to SIGINT/SIGTERM in
// NewRootCmd's RunE: canceling it tears down whichever profile is currently
// vending — see vend.Vend's doc comment on ctx.
func runSelection(ctx context.Context, c *cobra.Command, o opts, r *sigRouter) error {
	if o.forget != "" {
		return forgetSelector(c.ErrOrStderr(), o.forget)
	}
	if o.parallel < 1 {
		return errf(3, "--parallel must be at least 1, got %d", o.parallel)
	}
	if o.force && o.save == "" {
		return errf(3, "--force only applies to --save")
	}
	// Changed, not o.save != "", so --save '' is an error rather than a no-op.
	if c.Flags().Changed("save") {
		if err := validSelectorName(o.save); err != nil {
			return err
		}
	}
	if o.listSelectors && (o.out != "" || o.refresh != "" || o.clear != "" || o.save != "") {
		return errf(3, "a bare @ lists selectors; it cannot be combined with -o, --refresh, --clear or --save")
	}
	// --refresh and --clear take their filter from the file, so a selector would be ignored.
	if o.selector != "" && (o.refresh != "" || o.clear != "") {
		return errf(3, "@%s cannot be combined with --refresh or --clear, which use the file's own recorded filter", o.selector)
	}
	if o.exec {
		switch {
		case len(o.execArgv) == 0:
			return errf(3, "nothing to run after --; e.g. faws @readonly -- aws sts get-caller-identity --profile NAME")
		case o.out != "" || o.refresh != "" || o.clear != "" || o.save != "":
			return errf(3, "a command after -- cannot be combined with -o, --refresh, --clear or --save")
		case o.listSelectors:
			return errf(3, "name a selector before --, e.g. faws @readonly -- CMD")
		}
	}
	if o.listSelectors {
		return listSelectors(c.OutOrStdout())
	}
	// selectorLine is the saved form; the header keeps the @name label only
	// when this invocation added nothing to it.
	selectorLine := ""
	if o.selector != "" {
		base, err := resolveSelector(o.selector)
		if err != nil {
			return err
		}
		if widens(base, o) {
			fmt.Fprintf(c.ErrOrStderr(), "note: the extra --all/--any widens @%s\n", o.selector)
		}
		o = mergeSelector(base, o, c.Flags().Changed)
		selectorLine = invocationLine(base)
	}
	// Checked after the merge, since a selector can carry --format too.
	if o.exec && o.format != "" && o.format != "config" {
		return errf(3, "a command after -- gets a config file; --format %s is not supported there", o.format)
	}

	if o.clear != "" {
		// Only ever delete a file faws itself produced. The marker is the
		// proof; without it this could destroy a real ~/.aws/credentials.
		if _, err := output.ReadFilterLine(o.clear); err != nil {
			return errf(2, "refusing to delete %s: %v", o.clear, err)
		}
		if err := os.Remove(o.clear); err != nil {
			return errf(2, "%v", err)
		}
		fmt.Fprintf(c.ErrOrStderr(), "removed %s\n", o.clear)
		return nil
	}

	// Item 7: captured BEFORE o.refresh's file is overwritten below, so a
	// shrinking refresh can be detected and reported by name. nil outside a
	// --refresh (the comparison near the WriteFile call below is skipped
	// entirely in that case).
	var refreshOldNames []string
	if o.refresh != "" {
		line, err := output.ReadFilterLine(o.refresh)
		if err != nil {
			return errf(2, "%v", err)
		}
		// The recorded flags, not the @name label, are what gets replayed:
		// editing a selector later must never widen an existing file.
		_, line = splitSelectorLabel(line)
		parsed, err := parseInvocationLine(line)
		if err != nil {
			return errf(3, "%v", err)
		}
		o.all, o.any, o.exclude = parsed.all, parsed.any, parsed.exclude
		o.defaultTo = parsed.defaultTo
		o.format, o.region, o.outputFmt, o.set = parsed.format, parsed.region, parsed.outputFmt, parsed.set
		o.noCopy = parsed.noCopy
		o.out = o.refresh
		refreshOldNames = existingProfileNames(o.refresh)
		// A refresh maintains a working file: a partial result must not
		// replace it, so a failure aborts and leaves the old file in place.
		o.strict = true
	}

	profiles, err := awsconfig.Load()
	if err != nil {
		return errf(2, "read AWS config: %v", err)
	}
	// Item 7: on a machine with no ~/.aws at all, Load returns zero profiles
	// with no error (ParseFile treats a missing file as empty, since callers
	// read two files and either may legitimately be absent) — which used to
	// fall straight through to "filter matched no profiles in <path>" below,
	// naming a path that doesn't exist and blaming a filter that was never
	// the problem. Detect the missing file specifically and say so instead.
	if len(profiles) == 0 {
		if _, statErr := os.Stat(awsconfig.ConfigPath()); os.IsNotExist(statErr) {
			return errf(2, "no AWS config file found at %s; run `aws configure` (or `aws configure sso`), or point AWS_CONFIG_FILE at one",
				awsconfig.ConfigPath())
		}
	}
	f, err := filter.New(o.all, o.any, o.exclude)
	if err != nil {
		return errf(3, "%v", err)
	}
	matches := f.Apply(profiles)
	if len(matches) == 0 {
		return errf(4, "filter matched no profiles in %s", awsconfig.ConfigPath())
	}
	// Exec is the agent path, where every role is rarely what anyone meant.
	if o.exec && len(o.all)+len(o.any) == 0 {
		fmt.Fprintf(c.ErrOrStderr(), "faws: no --all, --any or selector filter; the command gets all %d profiles\n", len(matches))
	}

	// Item 7: warn (don't fail) when a profile name exists in BOTH files.
	// awsconfig.Load already resolves the collision by preferring config —
	// the correct behaviour for faws's OWN filtering/display, since config
	// is the file users edit deliberately — but the real AWS CLI resolves
	// credential keys (aws_access_key_id/aws_secret_access_key/
	// aws_session_token) from the credentials file first regardless of what
	// config says. That means a profile faws lists/vends by its config
	// metadata (region, sso_*, ...) could have its actual STATIC key
	// material come from a different section than the one faws is showing —
	// a real divergence from botocore's merge semantics that this wave does
	// not attempt to replicate (see the README). Naming the profile lets a
	// user go verify it themselves rather than being surprised later.
	//
	// Scoped to THIS invocation's resolved set, not every collision
	// anywhere in the whole AWS config: a duplicate the current filter never
	// touched is not this run's problem to report, and warning about it
	// every time would just be noise on an otherwise unrelated command.
	if names := duplicateNamesAmong(matches); len(names) > 0 {
		verb := "appears"
		if len(names) > 1 {
			verb = "appear"
		}
		fmt.Fprintf(c.ErrOrStderr(),
			"warning: %s %s in both %s and %s; faws's own filtering/display uses config, but the AWS CLI resolves credential keys from credentials first — see the README's Limitations\n",
			strings.Join(names, ", "), verb, awsconfig.ConfigPath(), awsconfig.CredentialsPath())
	}

	if o.out == "" && !o.exec {
		if err := listing.Render(c.OutOrStdout(), matches); err != nil {
			return err
		}
		if o.save != "" {
			// A saved line is vended later, so it gets the same checks as a vend.
			if _, _, err := o.extras(); err != nil {
				return err
			}
			return saveSelector(c.ErrOrStderr(), o.save, o, o.force)
		}
		return nil
	}

	// Looked up by name once vending needs to attach each profile's own
	// copyable keys (FIX 1) and pick its timeout (FIX 2).
	profileByName := make(map[string]awsconfig.Profile, len(matches))
	for _, m := range matches {
		profileByName[m.Profile.Name] = m.Profile
	}

	// Vend non-MFA profiles first so a batch fails fast on ordinary errors
	// before anyone is asked for a code. MFA profiles vend last and serially
	// (see vendSelection): concurrent MFA prompts interleave into garbage.
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if !m.Profile.NeedsMFA() {
			names = append(names, m.Profile.Name)
		}
	}
	for _, m := range matches {
		if m.Profile.NeedsMFA() {
			names = append(names, m.Profile.Name)
		}
	}

	// Validate --region/--output/-c before vending anything. extras() is a
	// pure string check on flag values with no dependency on resolved
	// profiles — it used to run after vend.VendAll below, which meant a
	// typo'd --region sat a user through every MFA prompt before telling
	// them. Moved up next to the --default check, which is validated early
	// for the identical reason.
	extras, order, err := o.extras()
	if err != nil {
		return err
	}

	// --default is round-tripped through invocationLine/parseInvocationLine
	// as a whitespace-separated field, exactly like --region/--output/-c
	// above: an embedded space would silently corrupt a later --refresh.
	if strings.ContainsFunc(o.defaultTo, unicode.IsSpace) {
		return errf(3, "--default %q contains whitespace; it must not, so --refresh can round-trip it", o.defaultTo)
	}

	// Validate --default against the resolved set before vending anything.
	// output.Render performs the same check, but by then every profile —
	// MFA-requiring ones included — has already been vended: a typo'd
	// --default would otherwise make a user sit through N 30-second MFA
	// prompts only to be told at the very end that the write can't happen.
	if o.defaultTo != "" && !containsName(names, o.defaultTo) {
		return errf(5, "--default %q is not among the %d resolved profiles", o.defaultTo, len(names))
	}

	// FIX 6: with a [default] profile in the source config AND --default
	// naming something else, the output would hold both [profile default]
	// (opts.Default's block, named literally "default" if a resolved profile
	// is called that — see below) and a second [default] carrying different
	// credentials than the real one, and the AWS CLI silently resolves
	// "default" to whichever block it parses last. Catch that before vending
	// rather than let it shadow the real default silently.
	if o.defaultTo != "" && o.defaultTo != "default" && containsName(names, "default") {
		return errf(5, "the resolved set already contains a profile literally named %q; --default %q would create a second, conflicting [default] block", "default", o.defaultTo)
	}

	// FIX 7: --default shapes output for config/credentials but has no
	// meaning for json (which has no notion of "the default profile") or env
	// (which is already pinned to exactly one profile) — used to be silently
	// ignored for both. output.Render enforces the same rule; this is the
	// early, pre-vend duplicate every other flag check here already gets.
	if o.defaultTo != "" && (o.format == "json" || o.format == "env") {
		return errf(3, "--default has no effect with --format %s; it has no concept of a default profile", o.format)
	}

	// Validate --format before vending anything. output.Render and
	// renderEnv perform the same checks, but only after every profile —
	// MFA-requiring ones included — has already been vended: a typo'd
	// --format (or --format env against more than one match) would
	// otherwise cost N 30-second MFA prompts before a pure string/count
	// check fails. The library-level guards stay in pkg/output; this is a
	// duplicate, earlier check with identical wording and exit code.
	switch o.format {
	case "", "config", "credentials", "env", "json":
	default:
		return errf(2, "unknown format %q (want config, credentials, env or json)", o.format)
	}
	if o.format == "env" && len(matches) != 1 {
		return errf(2, "--format env needs exactly one profile, got %d; narrow the filter", len(matches))
	}

	// CheckCLI itself shells out (`aws --version`), so it runs only after
	// every pure, no-AWS-call validation above has passed — otherwise a
	// typo'd --default/--format would pay that cost (and, on a machine with
	// no AWS CLI at all, fail with the wrong error) before ever being
	// reported.
	if err := vend.CheckCLI(); err != nil {
		return errf(2, "%v", err)
	}

	if notice := mfaNotice(matches); notice != "" {
		fmt.Fprintln(c.ErrOrStderr(), notice)
	}

	creds, skipped, err := vendSelection(ctx, c, o, names, profileByName)
	if err != nil {
		return err
	}

	// FIX 1: attach each profile's own raw keys so output can copy forward
	// everything that describes how to USE the credentials (region, output,
	// ca_bundle, ...) while dropping everything that describes how to OBTAIN
	// them (sso_*, role_arn, credential_process, ...) — see
	// output.effectiveKeys, which applies that deny-list at render time.
	for i := range creds {
		p := profileByName[creds[i].Profile]
		creds[i].SourceKeys, creds[i].SourceOrder = p.Keys, p.Order
		// Item 3: carry the profile's nested (indented) settings forward
		// too, so output can re-emit them nested under the same parent
		// instead of losing them.
		if len(p.Nested) > 0 {
			creds[i].SourceNested = make(map[string]map[string]string, len(p.Nested))
			creds[i].SourceNestedOrder = make(map[string][]string, len(p.Nested))
			for parent, ns := range p.Nested {
				creds[i].SourceNested[parent] = ns.Keys
				creds[i].SourceNestedOrder[parent] = ns.Order
			}
		}
	}

	filterLine := invocationLine(o)
	if o.selector != "" && filterLine == selectorLine {
		filterLine = "@" + o.selector + " = " + filterLine
	}
	wo := output.Options{
		Format:     o.format,
		Default:    o.defaultTo,
		Extra:      extras,
		ExtraOrder: order,
		NoCopy:     o.noCopy,
		FilterLine: filterLine,
	}
	if o.exec {
		// A private dir per run: parallel sessions never share a file, and
		// the whole dir goes when the child exits.
		dir, err := os.MkdirTemp("", "faws-")
		if err != nil {
			return errf(2, "%v", err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "config")
		if err := output.WriteFile(path, creds, wo); err != nil {
			return errf(2, "%v", err)
		}
		fmt.Fprintln(c.ErrOrStderr(), execSummary(len(creds), path, skippedClause(skipped), soonestExpiryClause(creds), o.execArgv[0]))
		return runChild(ctx, c, o.execArgv, path, r)
	}
	if o.out == "-" {
		wo.FilterLine = ""
		// Exit 2 (not 5): --default is validated above, against the
		// resolved set, before any vending happens. Anything Render can
		// still reject here (or WriteFile below) is a write-time failure —
		// a full disk, a bad path — not the "--default outside the
		// resolved set" case exit 5 is reserved for.
		if err := output.Render(c.OutOrStdout(), creds, wo); err != nil {
			return errf(2, "%v", err)
		}
		return nil
	}
	// Item 7: a --refresh that resolves to fewer profiles than the file
	// already had used to rewrite it silently and report success — a
	// profile that vanished from the source config (renamed, deleted, no
	// longer matching the recorded filter) shrinks the file with no
	// indication anything is missing. Compare by name, not just count, so
	// the warning can say exactly which profile(s) disappeared.
	if len(refreshOldNames) > 0 {
		newNames := make(map[string]bool, len(creds))
		for _, cr := range creds {
			newNames[cr.Profile] = true
		}
		var missing []string
		for _, n := range refreshOldNames {
			// A skipped profile already has its own warning.
			if !newNames[n] && !containsName(skipped, n) {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			verb := "resolves"
			if len(missing) > 1 {
				verb = "resolve"
			}
			fmt.Fprintf(c.ErrOrStderr(),
				"warning: --refresh %s now has fewer profiles than before; %s no longer %s and will be missing from the refreshed file\n",
				o.out, strings.Join(missing, ", "), verb)
		}
	}

	if err := output.WriteFile(o.out, creds, wo); err != nil {
		return errf(2, "%v", err)
	}
	line := fmt.Sprintf("wrote %s (0600) · %d profile(s)%s", o.out, len(creds), skippedClause(skipped))
	if clause := soonestExpiryClause(creds); clause != "" {
		line += " · " + clause
	}
	fmt.Fprintln(c.ErrOrStderr(), line)
	return nil
}

// existingProfileNames parses path (a file faws previously wrote, config or
// credentials shaped) and returns the profile names it already contains, for
// the --refresh shrink warning above. Best-effort: a parse error yields nil,
// which simply skips the comparison rather than failing the refresh over a
// diagnostic-only check. "default" is excluded — it duplicates one of the
// other named blocks (see --default's own collision guard elsewhere in this
// file) rather than naming a distinct profile that could "disappear".
func existingProfileNames(path string) []string {
	profiles, err := awsconfig.ParseFile(path)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		if p.Name != "default" {
			names = append(names, p.Name)
		}
	}
	return names
}

// duplicateProfileNames returns, in config's own order, every profile name
// present in both the AWS config and credentials files — the set
// awsconfig.Load() silently resolves by preferring config. That resolution
// is correct for faws's own filtering/display, but the merged view Load()
// returns can no longer tell a collision apart from an ordinary config-only
// profile, so this reparses both files directly rather than threading the
// distinction out of Load.
func duplicateProfileNames() []string {
	cfg, err := awsconfig.ParseFile(awsconfig.ConfigPath())
	if err != nil {
		return nil
	}
	creds, err := awsconfig.ParseFile(awsconfig.CredentialsPath())
	if err != nil {
		return nil
	}
	inCreds := make(map[string]bool, len(creds))
	for _, p := range creds {
		inCreds[p.Name] = true
	}
	var dup []string
	for _, p := range cfg {
		if inCreds[p.Name] {
			dup = append(dup, p.Name)
		}
	}
	return dup
}

// duplicateNamesAmong narrows duplicateProfileNames() down to the ones
// actually present in matches — see runSelection's call site for why the
// warning is scoped to this invocation's resolved set rather than every
// collision anywhere in the whole AWS config.
func duplicateNamesAmong(matches []filter.Match) []string {
	dup := duplicateProfileNames()
	if len(dup) == 0 {
		return nil
	}
	inMatches := make(map[string]bool, len(matches))
	for _, m := range matches {
		inMatches[m.Profile.Name] = true
	}
	var out []string
	for _, name := range dup {
		if inMatches[name] {
			out = append(out, name)
		}
	}
	return out
}

// vendErrorProfileRE pulls the profile name back out of a vend.Vend error,
// which always starts `vend "<profile>": ...` or `vend "<profile>" timed
// out ...` — see vend.Vend's own error-construction sites.
var vendErrorProfileRE = regexp.MustCompile(`^vend "([^"]*)"`)

// ssoExpiryTokenExpiredSubstring is the stable text botocore emits for an
// expired SSO token (UnauthorizedSSOTokenError): "The SSO session associated
// with this profile has expired or is otherwise invalid." faws matches on
// it only to decide whether to append a hint — never to change control flow
// or exit codes — so a future botocore wording change just means the hint
// stops appearing, not that anything breaks.
const ssoExpiryTokenExpiredSubstring = "sso session associated with this profile has expired"

// ssoExpiryHint is item 7's SSO expiry hint: when a vend fails because its
// SSO token expired AND the failing profile uses a shared sso-session (aws
// configure sso-session, referenced via sso_session = NAME), faws already
// knows that session's name from the config it parsed — botocore's own
// error only ever suggests "run aws sso login", without the --sso-session
// flag a multi-session setup actually needs. Append the exact command. A
// no-op for every other error, including an expired LEGACY (profile-level
// sso_start_url, no sso_session) SSO profile, where botocore's own "aws sso
// login" hint is already unambiguous.
func ssoExpiryHint(err error, profileByName map[string]awsconfig.Profile) error {
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), ssoExpiryTokenExpiredSubstring) {
		return err
	}
	m := vendErrorProfileRE.FindStringSubmatch(msg)
	if m == nil {
		return err
	}
	session := profileByName[m[1]].Get("sso_session")
	if session == "" {
		return err
	}
	return fmt.Errorf("%w (run: aws sso login --sso-session %s)", err, session)
}

// containsName reports whether target is in names.
func containsName(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// soonestExpiryClause finds the soonest expiration among creds and formats
// it for the post-write summary line. Creds with no expiration, or one that
// fails to parse as RFC3339, are ignored; if none carry a usable expiration
// the result is "" and the caller omits the clause entirely.
func soonestExpiryClause(creds []vend.Creds) string {
	now := time.Now()
	var soonest time.Time
	found := false
	for _, cr := range creds {
		if cr.Expiration == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, cr.Expiration)
		if err != nil {
			continue
		}
		if !found || t.Before(soonest) {
			soonest, found = t, true
		}
	}
	if !found {
		return ""
	}
	return output.ExpiryClause(soonest.Format(time.RFC3339), now)
}
