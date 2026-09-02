# Doc Searcher Agent Example

This agent demonstrates custom TypeScript tool extensions, isolated network egress allowlists, and caller environment variable passthrough.

## Features
- **Custom Tool Extension**: `extensions/doc-fetcher/index.ts` registers `fetch_docs`.
- **Network Egress**: Restricted allowlist (`docs.github.com`, `api.github.com`).
- **Environment Passthrough**: Allows `GITHUB_TOKEN` from the caller environment into the sandbox.
- **Permission**: `read-only`.

## Usage

```bash
# Validate definition and extension imports
go run ./cmd/agentrun validate ./examples/doc-searcher/agents/doc-searcher.yaml --workspace .

# Inspect capabilities
go run ./cmd/agentrun inspect ./examples/doc-searcher/agents/doc-searcher.yaml --workspace .
```
