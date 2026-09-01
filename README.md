# AgentRun

AgentRun is a Go CLI for running one declaratively configured coding agent
against one workspace. Its behavioral contract is in
[the specification](docs/specs/agent-run.md).

## Development

The supported development toolchain is **Go 1.24.4**. `go.mod` records that
toolchain, so a Go installation with toolchain auto-download enabled selects it
without a separately managed Go install. No runtime implementation exists yet;
the commands below establish the repository baseline for future Go packages.

Install [Task](https://taskfile.dev/) and use these commands from the
repository root:

```sh
task build        # build all Go packages
task test         # run all Go unit tests
task format       # apply formatting
task format:check # verify formatting without modifying files
task lint         # run go vet and golangci-lint
task ci           # run format:check, lint, test, then build
```

`task ci` is the deterministic local and CI validation command. It never runs
the rewriting `format` task: `format:check` uses `golangci-lint fmt --diff` and
fails if formatting is needed. The Taskfile keeps Go's build cache and
golangci-lint's cache under ignored repository directories, which also makes
the commands usable in environments where a home-directory cache is read-only.

The Go 1.24.4 toolchain is selected from `go.mod`; `golangci-lint` v2.1.2 is
pinned directly in each Task command, so neither requires a globally installed
lint binary. Its checked-in configuration enables `gofmt`,
`gofumpt`, `errcheck`, and `staticcheck`, in addition to the v2 standard lint
set. Do not replace these commands with an unpinned global installation.

There is no CLI runtime package yet. Until a later task adds
`./cmd/agentrun`, `task run` intentionally fails with an explicit prerequisite
instead of simulating a run. Afterwards it accepts the required
`AGENT=<agent-name-or-path>` and `WORKSPACE=<path>` variables, plus optional
`INPUT_KEY=<key>` (default `request`) and `INPUT_FILE=<path>`, and executes the
equivalent `agentrun run` invocation. Export provider credential environment
variables before invoking Task; do not place secrets in command-line inputs.
This mirrors the CLI's documented preference for input files for sensitive or
large values.

The current spec-only baseline has no Go packages. Build, test, format, and
lint report that fact successfully; once packages exist, each command runs its
corresponding check over all packages.

The `.gitignore` intentionally ignores only ephemeral AgentRun paths below
`.agentrun/` (`cache`, `runs`, and `tmp`): definitions, prompts, skills, and
extensions remain visible to Git. Local `.env` files and common credential file
types are ignored; commit safe, named example files such as `.env.example`.
