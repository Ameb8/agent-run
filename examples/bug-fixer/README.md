# Bug Fixer Agent Example

This agent operates with `read-write` permission to investigate, edit, and test bug fixes directly in a workspace.

## Features
- **Permission**: `read-write` (allows file modifications and shell execution).
- **Tools**: Full tool set (`read`, `grep`, `edit`, `write`, `shell`).
- **Provider**: Uses `openai-subscription` managed authentication.
- **Skills**: Isolated workflow guidelines in `skills/git-workflow/SKILL.md`.

## Usage

```bash
# Validate definition
go run ./cmd/agentrun validate ./examples/bug-fixer/agents/bug-fixer.yaml --workspace .

# Inspect capabilities
go run ./cmd/agentrun inspect ./examples/bug-fixer/agents/bug-fixer.yaml --workspace .
```
