# AGENTS.md

Instructions for AI agents that install faws, set it up for a user, or work on its code.

## Install

1. Check the AWS CLI: `aws --version` must be v2, 2.9.0 or later.
2. Check Go: `go version`.
3. Install: `go install github.com/kensantoso/faws@latest`.
4. Check that `faws --version` runs. If it is not found, add `$(go env GOPATH)/bin` to `PATH`.

## Set up selectors for a user

A selector decides which AWS roles an agent gets. Treat it as a permission grant.

1. Run `faws` with no flags to see the user's profiles. Listing makes no AWS calls and
   prints no credentials, so it is always safe.
2. Ask the user what each role is for, for example "read-only in every account" or
   "write in dev only".
3. Draft a filter and list it (no `-o`). Show the user the matched profiles and the
   `← key` column, which says why each one matched.
4. Prefer account IDs and full role names as terms. Terms match as substrings, so
   `dev/admin` also matches `Network-Dev/Admin`, and words such as `awsapps` match every
   SSO profile.
5. Save the selector only after the user confirms the list:
   `faws <filter> --save NAME`.

## Rules

- Never vend (`-o`, `--refresh` or a command after `--`) unless the user asks.
- Never print vended credentials into the conversation. Do not run `-o -` to a
  terminal, and do not read a vended file back.
- Never widen a saved selector, and never use `--force` on `--save`, without the user's
  confirmation.
- Default to read-only roles. Exclude production and admin roles unless the user asks
  for them.
- Do not add keys such as `credential_process`, `role_arn` or `sso_*` with `-c`. faws
  rejects them, because they make the consumer resolve credentials again.

## Use with a sandbox

faws writes a temp file and replaces `{}` with its path. Give the sandbox read access to
that file only, and block `~/.aws`, the SSO cache and the keychain:

```sh
faws @readonly -- nono run -p my-profile --read-file {} --allow-cwd -- claude
```

Other sandboxes use `{}` the same way, for example nsjail `-R {}` or Docker
`-v {}:{}:ro`.

## Work on the code

- Build: `make build`. Install to `GOBIN`: `make install`.
- Test: `go test ./...`. The e2e tests need the AWS CLI and use a fake credential
  process, so they never touch real credentials.
- Never run tests against a real `~/.aws`. Use a fake `aws` on `PATH`, as the existing
  tests do.
- faws has no subcommands, and it never resolves credentials itself: it always shells
  out to `aws configure export-credentials`.
