# Workspace-Embedded Agent Example

This example demonstrates a workspace-local agent package stored in `.agentrun/agents/analyzer.yaml`.

Because workspace agents originate from within the untrusted workspace repository, invoking them requires the explicit `--allow-workspace-agent` flag.

## Features
- **Workspace Discovery**: Placed in `.agentrun/agents/` inside the target workspace.
- **Permission**: `read-only`.
- **Tools**: `read`, `grep`.

## Usage

From within this workspace:
```bash
# Validate workspace-local agent
go run ../../cmd/agentrun validate analyzer --workspace . --allow-workspace-agent

# Inspect capabilities
go run ../../cmd/agentrun inspect analyzer --workspace . --allow-workspace-agent

# List agents (shows workspace origin)
go run ../../cmd/agentrun list --workspace .
```
