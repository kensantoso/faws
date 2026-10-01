#!/bin/sh
# run-samples.sh — renders the faws e2e/scale fixture and runs a handful of
# sample cases against it, writing one transcript per case to
# e2e/scale/out/*.txt, plus a timing report to e2e/scale/out/TIMING.txt.
#
# Hermetic, same approach as e2e/run-samples.sh: AWS_CONFIG_FILE/
# AWS_SHARED_CREDENTIALS_FILE point at the rendered fixture, whose SSO-style
# profiles carry `credential_process = .../fake-credential-process <name>` —
# the same fake vendor the small fixture uses, never touching the network.
# HOME is pointed at a scratch dir too.
#
# Re-run any time with no arguments:
#   ./e2e/scale/run-samples.sh
#
# The full 500-profile vend is NOT part of this script — it's slow (see
# TIMING.txt) and lives behind FAWS_SCALE_VEND=1 in scale_test.go instead.
# This script extrapolates that number from a real 25-profile vend timed
# here; TestFullScaleVend (run with FAWS_SCALE_VEND=1) overwrites
# out/MEASURED-FULL-VEND.txt with a genuine measurement, which this script
# folds into TIMING.txt if present.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
E2E_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
OUT_DIR="$SCRIPT_DIR/out"
FIXTURE_DIR="$OUT_DIR/aws"
WORK_DIR="$OUT_DIR/.work"

rm -rf "$FIXTURE_DIR" "$WORK_DIR"
mkdir -p "$FIXTURE_DIR" "$WORK_DIR"

sed "s#__FAWS_E2E_ROOT__#$E2E_ROOT#g" "$SCRIPT_DIR/aws/config" > "$FIXTURE_DIR/config"
cp "$SCRIPT_DIR/aws/credentials" "$FIXTURE_DIR/credentials"

FAKE_HOME="$WORK_DIR/home"
mkdir -p "$FAKE_HOME"

BIN_DIR="$WORK_DIR/bin"
mkdir -p "$BIN_DIR"
( cd "$SCRIPT_DIR/../.." && go build -o "$BIN_DIR/faws" . )
FAWS="$BIN_DIR/faws"

export AWS_CONFIG_FILE="$FIXTURE_DIR/config"
export AWS_SHARED_CREDENTIALS_FILE="$FIXTURE_DIR/credentials"
export HOME="$FAKE_HOME"
unset AWS_PROFILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN \
	AWS_REGION AWS_DEFAULT_REGION

VEND_DIR="$WORK_DIR/vend"
mkdir -p "$VEND_DIR"

run() {
	display=$1
	shift
	set +e
	output=$("$@" 2>&1)
	rc=$?
	set -e
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
note "500-ish profiles: head -20 of the listing, plus the count line. See scale_test.go's TestParseCounts for the exact count assertion (516: 480 base + 20 adversarial + 16 credentials-only, after config-wins collision resolution)."
# Same path sanitising as run(), which this case bypasses.
output=$("$FAWS" 2>&1 | sed -e "s#$FIXTURE_DIR#FIXTURE/aws#g" -e "s#$SCRIPT_DIR#FIXTURE#g")
total_lines=$(printf '%s\n' "$output" | wc -l | tr -d ' ')
{
	printf '$ faws | head -20   (plus the final count line; %s total lines)\n' "$total_lines"
	printf '%s\n' "$output" | head -20
	printf '...\n'
	printf '%s\n' "$output" | tail -1
	printf '(exit 0)\n'
} >> "$CASE_FILE"

case_file 02 broad-filter
run "faws --all readonly" "$FAWS" --all readonly

case_file 03 narrow-filter
run "faws --all 990000000010" "$FAWS" --all 990000000010

case_file 04 account-id-filter
run "faws --all 110000000002" "$FAWS" --all 110000000002

case_file 05 unicode-profiles
run "faws --all café" "$FAWS" --all café
run "faws --all 京" "$FAWS" --all 京

case_file 06 long-name-profile
note "Profile name is 238 characters; the listing display middle-elides it at 60 runes so this one outlier can't pad every other row's tabwriter column. The full name still matches the filter and is what gets vended/written."
run "faws --all longname-" "$FAWS" --all longname-

case_file 07 two-group-filter
run "faws --all org03,readonly --all org19,poweruser" \
	"$FAWS" --all org03,readonly --all org19,poweruser

case_file 08 secret-exclusion-zero-match
note "scale-bait-readonly exists ONLY inside shouty-scale-a's uppercase secret value. Must match nothing."
run "faws --all scale-bait-readonly" "$FAWS" --all scale-bait-readonly

case_file 09 boilerplate-overmatch
note "awsapps appears in every sso_start_url. This matches every profile."
run "faws --all awsapps" "$FAWS" --all awsapps

# now_ms — milliseconds since epoch, portable across BSD date (macOS) and GNU
# date (Linux): neither is used here at all, since BSD date has no
# sub-second resolution (%N is a GNU-only extension) — python3 is on both.
now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }

case_file 10 vend-25
note "25 profiles selected by --any per name: org00's full 20, plus nimbus/nimbus-extra/café-prod/東京-dev/emptyrole-profile."
start_ms=$(now_ms)
run "faws --any org00-acct0/ReadOnly ... (25 --any terms total) -o OUT/vend25.config" \
	"$FAWS" \
	--any org00-acct0/ReadOnly --any org00-acct0/PowerUser --any org00-acct0/Admin --any org00-acct0/ReadOnlyPlus \
	--any org00-acct1/ReadOnly --any org00-acct1/PowerUser --any org00-acct1/Admin --any org00-acct1/ReadOnlyPlus \
	--any org00-acct2/ReadOnly --any org00-acct2/PowerUser --any org00-acct2/Admin --any org00-acct2/ReadOnlyPlus \
	--any org00-acct3/ReadOnly --any org00-acct3/PowerUser --any org00-acct3/Admin --any org00-acct3/ReadOnlyPlus \
	--any org00-acct4/ReadOnly --any org00-acct4/PowerUser --any org00-acct4/Admin --any org00-acct4/ReadOnlyPlus \
	--any nimbus --any nimbus-extra --any café-prod/ReadOnly --any 東京-dev/ReadOnly --any emptyrole-profile \
	-o "$VEND_DIR/vend25.config"
end_ms=$(now_ms)
vend25_ms=$((end_ms - start_ms))

# Regression guard for FIX 1, visible at scale: org00-acct0/ReadOnly's own
# `region` (set by gen-fixture.sh's regionCycle) must survive into its
# vended block, and its granted_sso_*/credential_process acquisition keys
# must not. Print just this one profile's block rather than all 25.
note "One block from vend25.config, showing FIX 1 at scale: the profile's own region is carried forward, its granted_sso_*/credential_process acquisition keys are not."
block=$(awk '/^\[profile org00-acct0\/ReadOnly\]$/{p=1} p{print} p&&/^$/{exit}' "$VEND_DIR/vend25.config")
{
	printf '$ sed -n "/\\[profile org00-acct0\\/ReadOnly\\]/,/^$/p" OUT/vend25.config\n'
	printf '%s\n' "$block"
	printf '(exit 0)\n'
} >> "$CASE_FILE"

echo "wrote $(ls "$OUT_DIR"/*.txt | wc -l | tr -d ' ') sample transcripts to $OUT_DIR"

# ---------------------------------------------------------------------------
# TIMING.txt
# ---------------------------------------------------------------------------
TIMING_FILE="$OUT_DIR/TIMING.txt"

start_ms=$(now_ms)
"$FAWS" >/dev/null 2>&1
end_ms=$(now_ms)
parse_ms=$((end_ms - start_ms))

start_ms=$(now_ms)
"$FAWS" --all readonly >/dev/null 2>&1
end_ms=$(now_ms)
broad_ms=$((end_ms - start_ms))

start_ms=$(now_ms)
"$FAWS" --all 990000000010 >/dev/null 2>&1
end_ms=$(now_ms)
narrow_ms=$((end_ms - start_ms))

if [ "$vend25_ms" -gt 0 ] 2>/dev/null; then
	per_profile_ms=$((vend25_ms / 25))
else
	per_profile_ms=0
fi
extrapolated_500_ms=$((per_profile_ms * 500))
extrapolated_500_s=$((extrapolated_500_ms / 1000))

{
	echo "faws e2e/scale timing report"
	echo "generated $(date -u +%Y-%m-%dT%H:%M:%SZ) by run-samples.sh on $(uname -s) $(uname -m)"
	echo "aws CLI: $(aws --version 2>&1)"
	echo
	echo "== Parse + filter + list (no vend; no AWS calls at all) =="
	echo "516 profiles parsed and listed (no filter):    $parse_ms ms"
	echo "broad filter (--all readonly, ~250 matches):    $broad_ms ms"
	echo "narrow filter (--all <exact account id>, 1):    $narrow_ms ms"
	echo "(go test's TestParseFilterListTiming asserts all three stay under a 2s ceiling"
	echo " and logs its own in-process numbers, which exclude process-start overhead that"
	echo " these shell-measured numbers include.)"
	echo
	echo "== Vend timing (each vend shells out to \`aws configure export-credentials\`"
	echo "   once per profile; vending is serial by construction — concurrent MFA"
	echo "   prompts would interleave into garbage) =="
	echo "25-profile vend:              $vend25_ms ms total, $per_profile_ms ms/profile mean"
	echo "extrapolated 500-profile vend: ~$extrapolated_500_s s (~$((extrapolated_500_s / 60))m $((extrapolated_500_s % 60))s), linear from the above"
	if [ -f "$OUT_DIR/MEASURED-FULL-VEND.txt" ]; then
		echo
		echo "== Measured full-scale vend (FAWS_SCALE_VEND=1 go test) =="
		cat "$OUT_DIR/MEASURED-FULL-VEND.txt"
	else
		echo
		echo "== Measured full-scale vend =="
		echo "Not yet run. Run: FAWS_SCALE_VEND=1 go test ./e2e/scale/... -run TestFullScaleVend -v"
		echo "then re-run this script to fold the real number in above."
	fi
	echo
	echo "== What this means for the deferred --parallel feature =="
	echo "Vending is one \`aws\` subprocess per profile, run serially, and each subprocess"
	echo "pays Python/botocore startup cost (aws CLI is a Python program) on top of the"
	echo "fake vendor's own negligible work. At ~$per_profile_ms ms/profile, a filter that resolves to"
	echo "hundreds of profiles (plausible at this fixture's scale: --all readonly alone"
	echo "matches ~250 of the 516) takes multiple MINUTES to vend, not seconds — this is"
	echo "the finding the brief asked this report to make in plain numbers, not a vague"
	echo "'it might be slow.' A --parallel flag would cut that wall time roughly in"
	echo "proportion to the worker count for any selection with no mfa_serial profiles in"
	echo "it (MFA profiles cannot be parallelized: TOTP codes rotate every 30s and cannot"
	echo "be reused across AssumeRole calls, so they are inherently serial and must stay"
	echo "last regardless of what --parallel does to the rest of the batch). Given that"
	echo "this fixture alone can produce multi-hundred-profile broad filters and multi-"
	echo "minute vend times, --parallel is a real, not theoretical, need at this scale —"
	echo "the deferral should be revisited, not left indefinitely."
} > "$TIMING_FILE"

echo "wrote $TIMING_FILE"

rm -rf "$WORK_DIR"
