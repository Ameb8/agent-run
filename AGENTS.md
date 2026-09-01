# AgentRun contributor guide

`docs/specs/agent-run.md` is the canonical product and behavioral specification. Read the relevant sections before changing behavior, interfaces, or security boundaries. Do not edit the spec unless the assigned work explicitly calls for a specification change.

## Source-of-truth precedence

1. The assigned issue or task defines the implementation slice and acceptance criteria.
2. `docs/specs/agent-run.md` defines v1 behavior, terminology, interfaces, security guarantees, and non-goals.
3. Existing code and tests define repository conventions and integration details.

If an issue conflicts with the spec, preserve the spec and report the conflict instead of inventing a compromise.

## Product boundaries

- AgentRun is a Go CLI that runs one declaratively configured coding agent once against one workspace.
- It observes and reports; callers decide. Retry, escalation, scheduling, workflows, and multi-agent coordination belong outside AgentRun.
- The CLI and its single JSON object on stdout are the integration surface. Keep logs, progress, and child-process output off stdout.
- `SUCCESS` means the runtime produced a normal final response (and optional schema validation passed), not that the answer or workspace changes are correct.
- V1 supports Linux with Docker Engine and AgentRun's pinned private runtime image. Never fall back to host execution or weaker isolation.

## Security invariants

- Treat the model and model-generated tool requests as untrusted. Treat a selected agent package, especially its extensions, as trusted only within the enforced sandbox.
- Fail closed when required filesystem, process, network, environment, credential, package-resolution, or runtime isolation cannot be established.
- Enforce read-only versus read-write access at the container/filesystem boundary.
- Expose only explicitly selected package resources, tools, environment variables, and network destinations. Do not inherit host or pi configuration implicitly.
- Keep model credentials outside the sandbox environment and redact credentials, prompt inputs, and model output from errors and default diagnostics.
- Preserve package-root containment after canonicalization and symlink resolution.
- Do not add rollback semantics: failed read-write runs may leave partial workspace changes, as specified.

## Working conventions

- Inspect the current worktree and relevant history before editing; preserve unrelated user changes.
- Implement the smallest cohesive change that satisfies the task. Avoid speculative abstractions and deferred v1 features.
- Build on dependency issues' public seams rather than reimplementing them.
- Add tests for happy paths, failures, limits, and adversarial boundary cases relevant to the change.
- Prefer stable machine-readable error categories over matching human-readable error text.
- Do not create commits, push, open pull requests, or change GitHub issues unless explicitly asked.

## Verification

Run focused tests while iterating. Before handing off, run `task ci` when available; otherwise run every configured formatter, linter, test, and build relevant to the changed files. Review the final diff for scope, accidental generated files, and secret exposure, and report any verification that could not be completed.
