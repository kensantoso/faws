# faws e2e fixture

A hermetic end-to-end suite: real `faws` binary, real `aws configure
export-credentials` code path, fake tokens, no network, no real AWS
credentials touched.

## Why this is hermetic

`faws` never talks to AWS itself — it shells out to
`aws configure export-credentials` for every vend. Point `AWS_CONFIG_FILE`
at `aws/config` in this directory and the *real* AWS CLI resolves every
SSO-style profile's credentials by running
[`fake-credential-process`](./fake-credential-process), a small `sh` script
that prints an obviously-fake `credential_process` JSON payload
(`ASIAFAKE...` / `fake-secret-...` / `fake-session-...`) and never touches
the network. The code path is genuine end to end; only the vendor at the
bottom is fake.

Both `run-samples.sh` and `e2e_test.go` also explicitly clear `AWS_PROFILE`,
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`,
`AWS_REGION` and `AWS_DEFAULT_REGION` from the environment before running
faws. `faws` always passes `--profile` explicitly, so an ambient credential
env var was never actually going to be picked up — but that's a property of
faws's current code, not of this fixture, so hermeticity is made safe by
construction rather than resting on that.

## Files

| file | what it is |
|---|---|
| `aws/config` | Template AWS config. `__FAWS_E2E_DIR__` is substituted with this directory's absolute path when rendered, so `credential_process` resolves regardless of where the repo is checked out. Granted-style: `granted_sso_*` keys (for faws's own filtering/display) plus `credential_process` (what the AWS CLI actually resolves) — never real `sso_*` keys alongside `credential_process`, which would create resolution ambiguity. |
| `aws/credentials` | A second, plain AWS credentials file with two more static-key profiles, proving faws reads and merges both files. |
| `fake-credential-process` | The fake vendor. POSIX `sh`, executable, takes a profile name as `$1`, derives a short deterministic suffix from it (so different profiles get distinguishable, stable fake keys), and prints the `credential_process` contract with an expiration ~1 hour out. |
| `run-samples.sh` | Renders the fixture into `out/aws/`, builds `faws`, runs all 20 sample cases, and writes one transcript per case to `out/*.txt`. Re-run any time: `./e2e/run-samples.sh`. |
| `out/*.txt` | Committed sample transcripts — open any of them in `nano`. Each starts with the command line, then verbatim stdout+stderr, then `(exit N)`. `OUT/...` in a command line stands for a scratch directory the script vends into (not committed); `FIXTURE/aws/...` stands for this directory's rendered fixture (`out/aws/`, which is *not* committed — see below). |
| `out/aws/` | The rendered fixture as `run-samples.sh` actually used it — real absolute `credential_process` paths, not the `__FAWS_E2E_DIR__` placeholder. **Not committed** (see `.gitignore`): a real absolute path bakes the checkout location, and whoever last ran the machine's username, into every `credential_process` line. Running `./e2e/run-samples.sh` regenerates it locally, from the templates in `e2e/aws/`, every time. |
| `e2e_test.go` | The same 20 cases as Go subtests, asserting exit codes and the substrings that matter. Skips cleanly (`t.Skip`) when `aws` isn't on `PATH`. |

## Re-running

```sh
./e2e/run-samples.sh   # regenerates out/*.txt and out/aws/
go test ./e2e/...       # runs the same cases as assertions
```

Both need the AWS CLI (v2 ≥ 2.9.0, same as `faws` itself) on `PATH` — the Go
test skips itself if it isn't; the shell script requires it, since sample
output without it would just be "aws CLI not found" for every vending case.

Output is deterministic enough to commit except for the vended tokens'
`Expiration` timestamps (~1 hour ahead of "now" each run) — everything else,
including the fake key/secret/token suffixes (derived from the profile name,
not random), is stable run to run.

## What each sample case demonstrates

| # | slug | demonstrates |
|---|---|---|
| 01 | list-all | bare `faws` lists everything in both AWS files, merged |
| 02 | by-name | `--all acme-dev` |
| 03 | by-org | `--all acme` |
| 04 | groups-or | `--any`/`--all` groups OR together — a profile matching either group is listed, which is correct, not a filter bug (each transcript carries a note saying so) |
| 05 | groups-or-2 | same OR semantics, a different combination of groups |
| 06 | org-and-role | comma-separated terms within one `--all` AND together |
| 07 | by-account-number | matching on `granted_sso_account_id` |
| 08 | per-account-roles | two `--all` groups, each ANDed internally, ORed together |
| 09 | exclude | `--exclude` subtracted last, always wins |
| 10 | substring-gotcha | `readonly` also matches `ReadOnlyPlus` — a real substring hazard, not a bug |
| 11 | secrets-never-matched | **regression guard.** `shouty-static`'s secret VALUE contains `readonly` and its keys are UPPERCASE; filtering on that string must still find nothing (exit 4), proving `cutKV` lowercasing plus `SecretKeys` work together |
| 12 | vend-config | default `config` format: `[profile NAME]` headers |
| 13 | vend-credentials-format | `credentials` format + `--region`: bare `[NAME]` headers, region written into the block |
| 14 | vend-env | `--format env` to stdout via `-o -` |
| 15 | vend-json | `--format json` |
| 16 | refresh | **regression guard.** Refreshing case 13's file must reproduce credentials format WITH the region — the bug this fix caught was a refresh silently reverting to `config` format and dropping the region |
| 17 | clear | `--clear` deletes a file faws itself wrote |
| 18 | clear-refusal | **safety demonstration.** `--clear` pointed at a real-looking credentials file (no `# faws-filter:` marker) refuses AND leaves the file on disk — this is `--clear`'s one safeguard against destroying a file it didn't write. `--clear` is not the tool's only destructive command, though: `-o` overwrites its target unconditionally, with no marker check, so pointing it at a real credentials file replaces that file with no confirmation |
| 19 | no-match | exit 4 |
| 20 | bad-default | a typo'd `--default` fails (exit 5) before anything is vended — no output file is created |

Case 13's command adds `--all globex-prod`, a deliberate narrowing from an
entirely unfiltered vend: this fixture's `legacy-chain` profile is a
`role_arn`/`source_profile` chain, and resolving it for real would require
an actual `sts:AssumeRole` network call — the one thing this suite must
never do. Narrowing to one profile keeps the format+region round-trip case
13/16 exist to demonstrate while staying hermetic.

Case 13 also adds `--default globex-prod/Admin`, so cases 13/16 double as the
regression guard for the bug where `--refresh` silently dropped `--default`:
case 16 re-vends case 13's file from its recorded invocation, and the
`[default]` block must still be there afterward.
