# AgentRun Examples

This directory contains examples demonstrating how to structure and configure AgentRun agent packages.

## Examples Overview

| Example | Permission | Provider | Capabilities / Features |
|---|---|---|---|
| [`code-reviewer`](./code-reviewer) | `read-only` | `openai-compatible` | Structured JSON output schema, code review skill, read-only inspection tools (`read`, `grep`). |
| [`bug-fixer`](./bug-fixer) | `read-write` | `openai-subscription` | Read-write workspace tools (`read`, `grep`, `edit`, `write`, `shell`), git workflow skill, turns/timeout limits. |
| [`doc-searcher`](./doc-searcher) | `read-only` | `openai-compatible` | Custom TypeScript tool extension (`doc-fetcher`), allowlist egress network access, caller environment variable passthrough. |
| [`workspace-project`](./workspace-project) | `read-only` | `openai-compatible` | Workspace-embedded agent located inside `.agentrun/agents/` requiring `--allow-workspace-agent`. |

---

## Validating and Inspecting Examples

You can validate any example package definition statically without executing model calls:

```bash
# Validate code-reviewer definition & templates
go run ./cmd/agentrun validate ./examples/code-reviewer/agents/reviewer.yaml --workspace .

# Inspect resolved capabilities and effective configuration as JSON
go run ./cmd/agentrun inspect ./examples/code-reviewer/agents/reviewer.yaml --workspace .

# Validate bug-fixer
go run ./cmd/agentrun validate ./examples/bug-fixer/agents/bug-fixer.yaml --workspace .

# Validate doc-searcher (validates definition, template, and extension imports)
go run ./cmd/agentrun validate ./examples/doc-searcher/agents/doc-searcher.yaml --workspace .

# Validate workspace-local agent from within its workspace
cd examples/workspace-project
go run ../../cmd/agentrun validate analyzer --workspace . --allow-workspace-agent
```
