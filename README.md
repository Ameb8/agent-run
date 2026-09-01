# AgentRun

AgentRun is a Go CLI for running one declaratively configured coding agent
against one workspace. Its behavioral contract is in
[the specification](docs/specs/agent-run.md).

## Development

The supported development toolchain is **Go 1.24.4**. `go.mod` records that
toolchain, so a Go installation with toolchain auto-download enabled selects it
without a separately managed Go install. No runtime implementation exists yet;
the commands below establish the repository baseline for future Go packages.

Use these commands from the repository root:

```sh
# Apply repository formatting.
go fmt ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.2 fmt

# Check formatting and baseline static analysis without installing tools globally.
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.2 fmt --diff
go vet ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.2 run

# Run package tests and build future packages.
go test ./...
go build ./...
```

`golangci-lint` v2.1.2 is pinned directly in each command; Go caches its module
outside the repository. Its checked-in configuration enables `gofmt`,
`gofumpt`, `errcheck`, and `staticcheck`, in addition to the v2 standard lint
set. Do not replace these commands with an unpinned global installation.

The `.gitignore` intentionally ignores only ephemeral AgentRun paths below
`.agentrun/` (`cache`, `runs`, and `tmp`): definitions, prompts, skills, and
extensions remain visible to Git. Local `.env` files and common credential file
types are ignored; commit safe, named example files such as `.env.example`.
