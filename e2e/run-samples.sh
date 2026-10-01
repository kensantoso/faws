#!/bin/sh
# run-samples.sh — renders the faws e2e fixture and runs every sample case
# against it, writing one transcript per case to e2e/out/*.txt.
#
# Hermetic: AWS_CONFIG_FILE/AWS_SHARED_CREDENTIALS_FILE point at the rendered
# fixture, whose SSO-style profiles carry `credential_process =
# .../fake-credential-process <name>` — a local script that never touches
# the network. HOME is pointed at a scratch dir too, so nothing here can read
# or write the real ~/.aws.
#
# Re-run any time with no arguments:
#   ./e2e/run-samples.sh
#
# Output is deterministic except for the vended tokens' Expiration
# timestamps (~1 hour ahead of "now") and the fake key/secret/token suffixes,
# which are derived from the profile name and so ARE stable run to run; only
# the expiration clock text changes. That's expected and fine to commit.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT_DIR="$SCRIPT_DIR/out"
FIXTURE_DIR="$OUT_DIR/aws"
WORK_DIR="$OUT_DIR/.work"

rm -rf "$FIXTURE_DIR" "$WORK_DIR"
mkdir -p "$FIXTURE_DIR" "$WORK_DIR"

# Render the config template (committed to e2e/out/aws/config so the user can
# read the real, absolute-pathed thing) and copy the credentials file
# (nothing machine-specific to render there).
sed "s#__FAWS_E2E_DIR__#$SCRIPT_DIR#g" "$SCRIPT_DIR/aws/config" > "$FIXTURE_DIR/config"
cp "$SCRIPT_DIR/aws/credentials" "$FIXTURE_DIR/credentials"

# A private HOME, distinct from both the fixture and the real one, so a bug
# that fell back to "~/.aws" instead of honoring AWS_CONFIG_FILE would show
# up as a missing/empty result here rather than silently reading (or
# writing!) the operator's real AWS config.
FAKE_HOME="$WORK_DIR/home"
mkdir -p "$FAKE_HOME"

BIN_DIR="$WORK_DIR/bin"
mkdir -p "$BIN_DIR"
( cd "$SCRIPT_DIR/.." && go build -o "$BIN_DIR/faws" . )
FAWS="$BIN_DIR/faws"

export AWS_CONFIG_FILE="$FIXTURE_DIR/config"
export AWS_SHARED_CREDENTIALS_FILE="$FIXTURE_DIR/credentials"
export HOME="$FAKE_HOME"

# Safe by construction, not by subtlety: faws always passes --profile, so an
# ambient AWS_PROFILE/AWS_ACCESS_KEY_ID/etc never actually gets used today —
# but that's a property of faws's current code, not of this fixture. Clear
# them explicitly so hermeticity doesn't quietly depend on that.
unset AWS_PROFILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN \
	AWS_REGION AWS_DEFAULT_REGION

# Case 12/14/15 write here; case 17 clears one of them; case 20 must NOT.
VEND_DIR="$WORK_DIR/vend"
mkdir -p "$VEND_DIR"

# run DISPLAY_CMD REAL_ARGV...
#
# Runs REAL_ARGV, appends a "$ DISPLAY_CMD" / output / "(exit N)" block to
# $CASE_FILE. DISPLAY_CMD is the human-facing command line: it stands in for
# $FAWS (shown as "faws") and for $VEND_DIR / $FIXTURE_DIR (shown as OUT/ and
# FIXTURE/) so the committed transcripts read the same on every machine
# instead of embedding this run's temp paths.
run() {
	display=$1
	shift
	set +e
	output=$("$@" 2>&1)
	rc=$?
	set -e
	# faws itself echoes real paths back (e.g. "wrote $VEND_DIR/... (0600)",
	# "refusing to delete $FIXTURE_DIR/..."), and so does ls/cat on failure.
	# Sanitize those back to the same OUT/ and FIXTURE/aws/ placeholders used
	# in the display line, longest path first, so the committed transcript
	# never embeds this run's volatile temp directory.
	output=$(printf '%s\n' "$output" | sed \
		-e "s#$VEND_DIR#OUT#g" \
		-e "s#$FIXTURE_DIR#FIXTURE/aws#g" \
		-e "s#$SCRIPT_DIR#FIXTURE#g")
	{
		printf '$ %s\n' "$display"
		if [ -n "$output" ]; then
			printf '%s\n' "$output"
		fi
		printf '(exit %d)\n' "$rc"
	} >> "$CASE_FILE"
}

# show_file DISPLAY_PATH REAL_PATH — appends a "$ cat ..." block with the
# file's literal contents (used after a vend, to show what landed on disk).
show_file() {
	run "cat $1" cat "$2"
}

# show_gone DISPLAY_PATH REAL_PATH — appends an "$ ls ..." block proving a
# path is no longer present (case 17) or still present (case 18).
show_gone() {
	run "ls $1" ls "$2"
}

# note TEXT — appends a "# TEXT" line to $CASE_FILE, above the command/output
# it explains. Used where a spec-correct result (e.g. groups ORing together)
# reads, out of context, like a filter bug.
note() {
	printf '# %s\n' "$1" >> "$CASE_FILE"
}

case_file() {
	n=$1
	slug=$2
	CASE_FILE="$OUT_DIR/$n-$slug.txt"
	: > "$CASE_FILE"
}

case_file 01 list-all
run "faws" "$FAWS"

case_file 02 by-name
run "faws --all acme-dev" "$FAWS" --all acme-dev

case_file 03 by-org
run "faws --all acme" "$FAWS" --all acme

case_file 04 groups-or
note "Groups OR together: a profile matching EITHER group below is listed. This is correct — not a filter bug."
run "faws --any acme,globex --all readonly" "$FAWS" --any acme,globex --all readonly

case_file 05 groups-or-2
note "Groups OR together: a profile matching EITHER group below is listed. This is correct — not a filter bug."
run "faws --any readonly,poweruser --all acme" "$FAWS" --any readonly,poweruser --all acme

case_file 06 org-and-role
run "faws --all globex,readonly" "$FAWS" --all globex,readonly

case_file 07 by-account-number
run "faws --all 333333333333" "$FAWS" --all 333333333333

case_file 08 per-account-roles
run "faws --all 222222222222,readonly --all 333333333333,poweruser" \
	"$FAWS" --all 222222222222,readonly --all 333333333333,poweruser

case_file 09 exclude
run "faws --all readonly --exclude prod" "$FAWS" --all readonly --exclude prod

case_file 10 substring-gotcha
run "faws --all readonly" "$FAWS" --all readonly

case_file 11 secrets-never-matched
run "faws --all uppercase-secret-containing-readonly-bait" \
	"$FAWS" --all uppercase-secret-containing-readonly-bait

case_file 12 vend-config
run "faws --all 111111111111,readonly -o OUT/agent.config" \
	"$FAWS" --all 111111111111,readonly -o "$VEND_DIR/agent.config"
show_file "OUT/agent.config" "$VEND_DIR/agent.config"

case_file 13 vend-credentials-format
# NOTE deliberate deviation from the brief's literal `--format credentials
# --region ap-southeast-2` (no filter): with no --all/--any, faws selects
# every profile in the fixture, including legacy-chain — a role_arn +
# source_profile chain that a real `aws configure export-credentials` would
# resolve by actually calling sts:AssumeRole over the network. That would
# break hermeticity (and fail, since there's no network here). Adding --all
# globex-prod keeps this to the one profile the case needs, in its natural
# eu-west-1 region, which also makes the --region override in the output
# obviously an override rather than a no-op.
#
# --default globex-prod/Admin is also deliberate here, not incidental: it
# makes this case double as the regression guard for the --refresh-drops
# --default bug (case 16 re-vends this same file and must still carry the
# [default] block).
run "faws --all globex-prod --format credentials --region ap-southeast-2 --default globex-prod/Admin -o OUT/agent.credentials" \
	"$FAWS" --all globex-prod --format credentials --region ap-southeast-2 --default globex-prod/Admin -o "$VEND_DIR/agent.credentials"
show_file "OUT/agent.credentials" "$VEND_DIR/agent.credentials"

case_file 14 vend-env
run "faws --all acme-dev,readonly --format env -o -" \
	"$FAWS" --all acme-dev,readonly --format env -o -

case_file 15 vend-json
run "faws --all acme-dev,readonly --format json -o OUT/agent.json" \
	"$FAWS" --all acme-dev,readonly --format json -o "$VEND_DIR/agent.json"
show_file "OUT/agent.json" "$VEND_DIR/agent.json"

case_file 16 refresh
# Regression guard: a file written with --format credentials --region
# ap-southeast-2 --default globex-prod/Admin (case 13) must refresh back
# STILL in credentials format (bare [name] headers, no "profile " prefix),
# STILL carrying the region, and STILL carrying the [default] block.
run "faws --refresh OUT/agent.credentials" \
	"$FAWS" --refresh "$VEND_DIR/agent.credentials"
show_file "OUT/agent.credentials" "$VEND_DIR/agent.credentials"

case_file 17 clear
run "faws --clear OUT/agent.config" "$FAWS" --clear "$VEND_DIR/agent.config"
show_gone "OUT/agent.config" "$VEND_DIR/agent.config"

case_file 18 clear-refusal
# Safety demonstration: --clear refuses any file that doesn't carry the
# faws-filter marker it itself writes, so pointing it at a real-looking
# credentials file must refuse AND leave the file on disk.
run "faws --clear FIXTURE/aws/credentials" "$FAWS" --clear "$FIXTURE_DIR/credentials"
show_gone "FIXTURE/aws/credentials" "$FIXTURE_DIR/credentials"

case_file 19 no-match
run "faws --all nothing-matches-this" "$FAWS" --all nothing-matches-this

case_file 20 bad-default
run "faws --all readonly --default nope -o OUT/x.config" \
	"$FAWS" --all readonly --default nope -o "$VEND_DIR/x.config"
show_gone "OUT/x.config" "$VEND_DIR/x.config"

case_file 21 quoted-name
note "item 5 regression guard: [profile \"quoted space/Ops\"] is read with its quotes stripped (matching botocore's shlex parsing), and must be re-quoted on write below — an unquoted 'quoted space/Ops' header would make the real AWS CLI's list-profiles silently drop the profile."
run "faws --all 888888888888 -o OUT/quoted.config" \
	"$FAWS" --all 888888888888 -o "$VEND_DIR/quoted.config"
show_file "OUT/quoted.config" "$VEND_DIR/quoted.config"

case_file 22 nested-settings
note "item 3 regression guard: the profile's indented 's3' settings (max_concurrent_requests, addressing_style) must be re-emitted nested under 's3' below, not flattened into bogus top-level keys — the CLI's own 'aws configure get s3.max_concurrent_requests' only ever reads the nested form."
run "faws --all nested-settings -o OUT/nested.config" \
	"$FAWS" --all nested-settings -o "$VEND_DIR/nested.config"
show_file "OUT/nested.config" "$VEND_DIR/nested.config"

rm -rf "$WORK_DIR"

echo "wrote $(ls "$OUT_DIR"/*.txt | wc -l | tr -d ' ') sample transcripts to $OUT_DIR"
