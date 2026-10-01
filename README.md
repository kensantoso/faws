# faws

Filter your AWS roles, writes multiple the credentials into a file.

faws picks profiles from your AWS config and writes their credentials to a static file so that your AI agents can assume and make api calls to multiple aws accounts. 
It uses `aws configure export-credentials` so that `credentials_process` etc will work and you don't need to give the sandbox access to key stores. It's designed to give an agent in a sandbox access to different profiles to do it's job. e.g. Readonly, cloudtrail + investigator, etc

## Install

Needs the AWS CLI v2 (2.9.0 or later) on your `PATH`.

```sh
go install github.com/kensantoso/faws@latest
```

## Use

List what a filter matches. This makes no AWS calls:

```sh
faws                              # every profile
faws --all readonly               # profiles matching "readonly"
faws --all 111111111111,readonly  # both terms must match (AND)
faws --any dev,staging            # either term (OR)
faws --all readonly -x prod       # drop anything matching "prod"
```

Terms are case-insensitive substrings, matched against the profile name and its config
values. Account IDs are the most precise terms.

Vend credentials:

```sh
faws --all readonly -o ./agent.config   # write a config file
faws --all readonly -o -                # or print to stdout
AWS_CONFIG_FILE=./agent.config aws sts get-caller-identity --profile Dev/ReadOnly

faws --refresh ./agent.config           # re-vend with the same filter
faws --clear ./agent.config             # delete it
```

Save a filter and reuse it:

```sh
faws --all readonly -x prod --save readonly
faws @readonly -o ./agent.config
faws @                                  # list saved selectors
faws --forget readonly
```

Run a command with the credentials. faws vends into a temp file, sets
`AWS_CONFIG_FILE`, replaces `{}` with the file's path, and deletes the file when the
command exits:

```sh
faws @readonly -- aws s3 ls --profile Dev/ReadOnly
faws @readonly -- nono run -p my-profile --read-file {} --allow-cwd -- claude
```

Other flags: `--format config|credentials|env|json`, `--region`, `--default PROFILE`,
`-c key=value`, `--parallel N` (default 8), `--strict` (fail if any profile fails,
instead of skipping it). See `faws --help`.

If you use an AI agent to set this up, point it at [AGENTS.md](AGENTS.md).

## License

MIT. See [LICENSE](LICENSE).
