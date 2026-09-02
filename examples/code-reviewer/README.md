# Code Reviewer Agent Example

This agent performs read-only analysis of a provided diff against a workspace and produces structured review feedback according to a JSON Schema.

## Features
- **Permission**: `read-only` (workspace modifications are prevented).
- **Tools**: `read`, `grep`.
- **Output Validation**: Structured according to `schemas/review-output.json`.
- **Skills**: Domain guidelines defined in `skills/code-review/SKILL.md`.

## Usage

```bash
# Validate the definition and prompt template
go run ./cmd/agentrun validate ./examples/code-reviewer/agents/reviewer.yaml --workspace .

# Inspect configuration and capabilities
go run ./cmd/agentrun inspect ./examples/code-reviewer/agents/reviewer.yaml --workspace .
```
