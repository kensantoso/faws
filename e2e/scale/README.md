# faws e2e/scale fixture

A ~500-profile fixture and test suite, separate from the small 12-profile
fixture in `e2e/`. That suite proves faws works; this one exists to find what
only breaks, degrades, or misleads once the profile count leaves toy
territory. It reuses `e2e/fake-credential-process` as its sole vendor — no
second vendor script, no network, no real `~/.aws` touched.

## The distribution

**Base population — 480 SSO-style profiles, fully algorithmic:**

- 24 orgs, `org00`..`org23`, start URL `https://org<NN>.awsapps.com/start`
- 5 accounts per org, `acct0`..`acct4`. Account id = `printf '%02d%08d%02d' <org> 0 <acct>`
  — 12 digits, org index recoverable as the first two, account index as the
  last two (e.g. org 11 acct 2 -> `110000000002`)
- 4 roles per account: `ReadOnly`, `PowerUser`, `Admin`, `ReadOnlyPlus`
- 24 x 5 x 4 = 480, profile name `org<NN>-acct<A>/<Role>`
- region cycles through 6 values (`us-east-1`, `us-west-2`, `eu-west-1`,
  `ap-southeast-2`, `eu-central-1`, `ca-central-1`) keyed by a running counter
- granted-style keys (`granted_sso_*`) plus `credential_process` pointing at
  `e2e/fake-credential-process`, exactly like the small fixture

**Then 20 hand-built adversarial profiles**, each chosen to break a naive
implementation:

| profiles | what it tests |
|---|---|
| `app`, `app-extra` | names that are prefixes of each other — substring matching does not over- or under-select |
| `roletest/Deploy`, `roletest/DeployAdmin`, `roletest/DeployAdminPlus` | role names where one contains another, three deep |
| `acctclose-a/ReadOnly`, `-b`, `-c` (ids `990000000010/11/12`) | account ids sharing a long prefix, differing only in the last two digits |
| `café-prod/ReadOnly`, `東京-dev/ReadOnly` | non-ASCII names, and substring matching at a multi-byte rune boundary |
| a 238-char name (`longname-` + 220 `x` + `/ReadOnly`) | filter still matches on the full name; the listing display truncates it (middle-elided, capped at 60 runes) so it can't blow up the tabwriter width of every other row |
| `emptyrole-profile` | `granted_sso_role_name` key present, value empty — display accessors return blank, no crash |
| `chain-a`, `chain-b` | `role_arn` + `source_profile` chains — visible to the filter, never vended (no `credential_process`; a real vend would need a genuine `sts:AssumeRole` network call) |
| `shouty-scale-a`, `shouty-scale-b` | UPPERCASE `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, secret VALUES containing common filter words (`readonly`, `admin`) — secret exclusion must hold at scale |
| `mfa-a/ReadOnly`, `mfa-b/PowerUser` | `mfa_serial` present — MFA counting/ordering at scale |
| `casecheck-a/ReadOnly`, `casecheck-b/PowerUser` | keys in unusual case (`Granted_SSO_Role_Name`, etc.) — key lowercasing |

Plus one `[sso-session ...]` and one `[services ...]` block: still skipped,
not counted as profiles.

**Credentials file — 20 bare `[name]` sections:**

- 4 deliberately collide with config profile names (`app`, `shouty-scale-a`,
  `mfa-a/ReadOnly`, `org05-acct3/Admin`), each carrying a `note =
  credswins-...-should-not-appear` marker found nowhere in the config
  version — proof the config-wins precedence rule holds at scale, not just
  proof the file merges
- 16 are new `credsonly-00`..`credsonly-15` profiles found nowhere in config

Merged total visible profiles after `Load()`: 480 + 20 + 16 = **516** (the
4 colliding credentials-file entries contribute nothing new; config wins).

## Files

| file | what it is |
|---|---|
| `gen-fixture.sh` | Deterministic generator. Re-run any time (`./e2e/scale/gen-fixture.sh`) — output is byte-for-byte identical run to run, no randomness, no timestamps. Regenerates `aws/config` and `aws/credentials`. |
| `aws/config` | ~500 profile sections, committed. `__FAWS_E2E_ROOT__` is substituted with `e2e/`'s absolute path (one level up — that's where `fake-credential-process` actually lives) when rendered by `scale_test.go` / `run-samples.sh`. |
| `aws/credentials` | 20 bare-section profiles, committed. Nothing machine-specific, so nothing to render. |
| `scale_test.go` | The tests. Every expected count is computed from an independently written model of the fixture (the same rules as `gen-fixture.sh`, expressed as Go data) plus a from-spec re-implementation of the AND/OR/exclude filter algorithm — never copied out of a run of faws itself. |
| `run-samples.sh` | Renders the fixture into `out/aws/`, builds `faws`, runs the sample cases, writes `out/*.txt`, and writes `out/TIMING.txt`. |
| `out/*.txt` | Committed sample transcripts, same shape as `e2e/out/*.txt`. |
| `out/TIMING.txt` | The timing deliverable — see below. |

## Why the test file doesn't just shell out to `gen-fixture.sh` for its expectations

The brief for this fixture is explicit: never paste a number you got from
running the tool, because that enshrines whatever it currently does, bug
included. `scale_test.go` instead carries its own small model of "what the
fixture should contain" — the same org/account/role formula, and a literal
list of the 20 adversarial profiles' non-secret values — and its own
straight-from-the-spec re-implementation of `Filter.Apply`'s AND/OR/exclude
logic (`expectedMatches`/`expectedCount`). Expected counts for any filter
query are computed by running that small independent model, then compared
against what the real `faws` binary reports. If they disagree, that's either
a bug in faws or a bug in the model — either way worth finding.

Keeping the model in sync with `gen-fixture.sh` is manual (there is no single
source of truth the two are generated from) — that tradeoff is deliberate:
duplication that a test failure will surface immediately beats a shared
helper that could silently encode the same bug into both the fixture and its
own check.

## Demonstrations in the sample transcripts

- **`09-boilerplate-overmatch.txt`** demonstrates why filters can match much more
  than expected at scale: `--all awsapps` matches 496 of 516 profiles because the
  string appears in every profile's `granted_sso_start_url`. See the design doc's
  "Consequence at scale" note and the README's "Why did my filter match everything?"
  section.

## Re-running

```sh
./e2e/scale/gen-fixture.sh    # regenerates aws/config and aws/credentials (rarely needed)
go test ./e2e/scale/...        # the fast path: parse/filter/list/25-profile-vend/refresh/clear
FAWS_SCALE_VEND=1 go test ./e2e/scale/... -run TestFullScaleVend -v   # the slow, full-fixture vend
./e2e/scale/run-samples.sh     # regenerates out/*.txt and out/aws/
```

The full vend is deliberately gated: `aws configure export-credentials`
spawns one `aws` process per profile and vending is serial by construction
(concurrent MFA prompts would interleave into garbage), so vending several
hundred profiles takes real wall-clock minutes. See `out/TIMING.txt` for the
actual numbers measured on the machine this was built on, and what they mean
for the deferred `--parallel` feature.
