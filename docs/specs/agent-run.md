# AgentRun Specification

**Status:** Draft
**Version:** 1.0

## 1. Purpose

AgentRun is a small, standalone command-line tool for invoking AI coding agents defined declaratively in config files. It solves one problem: given a named "agent" (a bundle of model, skills, tools, permissions, and a prompt template) and a set of inputs, run that agent safely against a workspace and report a structured result.

AgentRun is deliberately not an orchestrator, a scheduler, or a workflow engine. It has no concept of retries, escalation, multi-step pipelines, or control flow. It runs one agent, once, and tells the truth about what happened. Anything that needs to sequence multiple agent runs, decide whether to retry a failure, or coordinate agents with each other is a separate system built *on top of* AgentRun — a shell script, a build tool, a graph-based orchestrator, a polling loop, a CI job. AgentRun is the primitive they all share.

This separation is the core design principle of the whole spec: **AgentRun observes and reports; callers decide.**

## 2. Design Goals

- **Portable**: usable from any shell, any language, any orchestration system, without a bespoke SDK.
- **Declarative**: an agent's capabilities and constraints are defined in a config file, not code.
- **Safe by default**: filesystem access is explicit and enforced at the OS/container boundary, not left to the agent's own discretion.
- **Composable**: one agent definition is invokable identically whether called from a terminal or a script.
- **Honest**: the tool reports what happened in a run, not what should happen next. It does not guess at caller intent.
- **Project-agnostic**: agent definitions can be shared globally across projects or scoped to a single project, resolved consistently.

## 3. Non-Goals

- Retry logic, backoff, or escalation policy. (Caller responsibility — see §9.)
- Multi-agent coordination or shared state between runs.
- Long-running or interactive agent sessions. Each invocation is a single bounded run with a defined end.
- A GUI or dashboard. AgentRun is a CLI; visualization is a caller's concern.
- An in-process library API. AgentRun's integration surface is the CLI and its stdout contract (§7.3) — see §4 for why this is a deliberate scope narrowing, not a deferred feature.

## 4. Implementation Language

AgentRun is implemented in Go.

This spec concerns itself only with the CLI. There is no in-process library interface — orchestrators, daemons, and other long-running callers invoke AgentRun as a subprocess and parse its stdout JSON result, the same as any other caller. This is a narrower scope than earlier drafts of this spec, which pursued a Python implementation specifically to support a zero-overhead in-process `from agentrun import load_agent, run` call for Python-based orchestrators.

That tradeoff is reversed here. A single static binary is the priority: trivial distribution (`go install`, a release binary dropped in `$PATH`, no interpreter or virtualenv to provision), fast process-spawn/startup latency, and no runtime dependency for callers regardless of what language they're written in. The in-process ergonomics Python offered were specific to Python callers; a compiled CLI serves callers in any language identically, via the same stdout contract every caller already needs to support.

Language-specific SDKs/libraries that wrap the CLI (subprocess invocation, JSON parsing, typed results) are an explicit non-goal of this spec (§3) and may be built as separate, later, per-language projects on top of the CLI contract defined here — mirroring how callers like graph orchestrators are themselves built on top of AgentRun rather than inside it.

## 5. Core Concepts

### 5.1 Agent Definition

An **agent definition** is a YAML file describing one reusable agent role: what model it uses, what skills and tools it has access to, what filesystem permissions it's granted, and how its prompt is constructed. It does not contain per-run data (that comes from inputs at invocation time).

### 5.2 Run

A **run** is a single invocation of an agent definition against a specific workspace, with a specific set of inputs. A run has exactly one of two outcomes: `SUCCESS` or `FAILURE`.

### 5.3 Workspace

The **workspace** is the directory an agent operates on. It is passed at invocation time, not baked into the agent definition, so the same agent definition can run against any project.

### 5.4 Result

The **result** is a single JSON object emitted to stdout describing the outcome of a run. This is the entire integration surface of AgentRun.

## 6. Agent Definition Format

Agent definitions are YAML files, conventionally named `<agent-name>.yaml`.

```yaml
name: reviewer
description: Reviews a diff and produces review comments

model:
  provider: openai-compatible       # or: openai-subscription
  endpoint: https://api.example.com/v1   # omitted for openai-subscription
  model: gpt-4.1
  api_key_env: REVIEWER_MODEL_API_KEY

skills:
  - code-review
  - security-basics

tools:
  extensions:
    - web-search
  allow: [read, grep, web_search]

network:
  mode: allowlist                    # none | allowlist
  hosts:
    - api.search.example

environment:
  allow:
    - WEB_SEARCH_API_KEY

permission: read-only                 # read-only | read-write

prompt:
  template: prompts/reviewer.md.tmpl
  inputs:
    required:
      - diff
      - pr_description
    optional: []

limits:
  max_turns: 15
  timeout_s: 600
```

### 6.1 Field Reference

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique identifier for the agent. Used for resolution (§8). |
| `description` | no | Human-readable summary. |
| `model.provider` | yes | `openai-compatible` (arbitrary endpoint + API key) or `openai-subscription` (uses an existing subscription-based auth, no endpoint/key needed). |
| `model.endpoint` | if `openai-compatible` | Base URL of the model API. |
| `model.model` | yes | Model identifier passed to the provider. |
| `model.api_key_env` | if `openai-compatible` | Name of the environment variable holding the API key. AgentRun never accepts raw keys in config files. |
| `skills` | no | Ordered list of named skill bundles to load from the resolved agent package's `skills/` directory (§6.4). |
| `tools.extensions` | no | Ordered list of package-local pi extensions to load from the resolved agent package's `extensions/` directory (§6.5). Empty/omitted means no extensions. |
| `tools.allow` | no | Explicit tool allowlist. Empty/omitted means no tools. |
| `network.mode` | no | Network access available to tools: `none` (the default) or `allowlist` (§6.6). |
| `network.hosts` | if `network.mode` is `allowlist` | Exact hostnames tools may contact. Empty means no tool network access. |
| `environment.allow` | no | Names of caller environment variables that may be passed through to the isolated agent environment (§6.7). Values never appear in the definition. |
| `permission` | yes | `read-only` or `read-write`. Declares intent; §7 covers enforcement. |
| `prompt.template` | yes | Path (relative to the agent definition file) to a template file. |
| `prompt.inputs.required` | no | Named inputs that must be supplied at invocation time. Missing required inputs are validation errors. |
| `prompt.inputs.optional` | no | Named inputs exposed to the template when supplied. Missing optional inputs render as empty values, allowing conditional template branches. |
| `limits.max_turns` | no | Upper bound on agent tool-use turns before AgentRun forces termination and reports `FAILURE`. |
| `limits.timeout_s` | no | Wall-clock timeout in seconds. |

### 6.2 Prompt Templates

Templates use Go's `text/template` syntax. A template has access only to the variables declared in `prompt.inputs.required` or `prompt.inputs.optional`, supplied via `--input key=value` at invocation time (§8.2). Missing required inputs and references to undeclared variables are template errors. Missing optional inputs evaluate as empty values so they can be used in conditional branches.

```markdown
<!-- prompts/reviewer.md.tmpl -->
You are reviewing the following diff. Focus on correctness and security.

## Context
{{.pr_description}}

## Diff
{{.diff}}
```

#### 6.2.1 Scope: Templating vs. Data Acquisition

AgentRun's templating is pure string substitution and composition over values supplied at invocation time. It does not fetch, resolve, or look up anything itself. If an input references an external resource — an issue number, a URL, a file path meaningful only to some other system — resolving that reference into actual text is the caller's responsibility, performed before invocation, with the resolved text passed as an ordinary `--input`:

```bash
issue_text=$(gh issue view 123 --json title,body -q '.title + "\n\n" + .body')

agentrun run implementer \
  --workspace "$(pwd)" \
  --input issue="$issue_text"
```

The template only ever sees `{{.issue}}` as an opaque string. AgentRun does not acquire prompt inputs or make application-level calls to the system from which they originate. It may transport explicitly allowlisted environment variables into a run and enforce network access for an agent tool (§6.6–§6.7), but input acquisition remains the caller's responsibility. This keeps an agent definition portable across callers that source the same named input differently (a GitHub issue, a GitLab issue, a local file, another agent run's output) without AgentRun needing to know or care which.

The test for what belongs in AgentRun versus the caller: **would supporting this require AgentRun to hold credentials or perform I/O unrelated to running the agent itself?** If yes, it stays outside AgentRun.

#### 6.2.2 Composition and Conditional Inputs

What templating *does* meaningfully provide, within that scope, is composition: combining fixed scaffolding (role instructions, constraints, output format) with multiple named inputs, including conditionally.

```markdown
<!-- prompts/implementer.md.tmpl -->
You are implementing a GitHub issue in this codebase. Follow the existing
code style. Do not modify files outside src/ and tests/. Write tests for
any new behavior.

## Issue
{{.issue}}

{{if .prior_feedback}}
## Feedback from previous attempt
Address the following before resubmitting:

{{.prior_feedback}}
{{end}}
```

```yaml
prompt:
  template: prompts/implementer.md.tmpl
  inputs:
    required:
      - issue
    optional:
      - prior_feedback
```

A caller running this agent for the first time supplies only `issue`. A caller retrying after a prior failure (feeding back that run's `error` message, or a human's review comment) additionally supplies `prior_feedback`, and the template's conditional branch includes it. AgentRun does not know or care where `prior_feedback` originated — only that the template declares it as an available input and renders accordingly when present.

This is the actual value templating adds over a caller hand-assembling prompt strings itself: the scaffolding and branching logic are versioned once, alongside the agent definition, and every caller gets improvements to them automatically rather than each caller re-implementing prompt construction independently.

#### 6.2.3 Template Composition

For scaffolding shared across multiple agent definitions (e.g. a standing "you are a careful coding agent operating under these constraints" preamble), templates may use Go template's standard `{{template "name" .}}` mechanism, with shared fragments defined via `{{define "name"}}...{{end}}` and resolved relative to the including template's directory:

```markdown
<!-- prompts/_base.md.tmpl -->
{{define "base"}}
You are a careful coding agent. Make minimal, well-tested changes.
Do not modify files outside the permitted scope.
{{end}}
```

```markdown
<!-- prompts/reviewer.md.tmpl -->
{{template "base" .}}

## Diff to review
{{.diff}}
```

This remains pure composition — no I/O, no external resolution — and is scoped the same way as any other template feature under §6.2.1.

### 6.3 Validation

Before running, AgentRun validates an agent definition and its inputs:

- All required fields present.
- `prompt.template` resolves to an existing file.
- Every name in `prompt.inputs.required` has a corresponding `--input` at invocation time.
- Every supplied input is declared in either `prompt.inputs.required` or `prompt.inputs.optional`.
- Every skill name is a simple identifier rather than a path, resolves within the agent package's `skills/` directory, and names a directory containing `SKILL.md` (§6.4).
- No skill name appears more than once.
- Every extension name is a simple identifier, resolves within the agent package's `extensions/` directory, and names a directory containing `index.ts` (§6.5).
- Every name in `tools.allow` is registered by the built-in provider or one of the declared extensions.
- No two declared providers register the same tool name. Built-in tool overrides are not supported in v1.
- `network.mode` and `network.hosts` form a valid network policy; `hosts` is rejected when the mode is `none`.
- Every entry in `environment.allow` is a syntactically valid environment-variable name. Missing variables are configuration failures before the model is called.

Validation failures are reported in the same shape as run failures (§7.3) and always occur before any model call. Structural validation is completed before the sandbox starts. Because discovering the tools registered by an extension requires executing trusted extension code, that specific validation occurs only after the isolated environment has been established (§7.2), but still before the prompt is sent to the model.

### 6.4 Skills

A **skill** is a named bundle of instructions and optional supporting resources made available to the underlying coding agent for one run. Agent definitions select skills by name; they do not contain filesystem paths or configure search paths.

An AgentRun package uses the following conventional layout:

```text
.agentrun/
├── agents/
│   └── reviewer.yaml
├── prompts/
│   └── reviewer.md.tmpl
└── skills/
    ├── code-review/
    │   └── SKILL.md
    └── security-basics/
        └── SKILL.md
```

Each selected skill is a directory whose required entry point is `SKILL.md`. A skill may also contain supporting files, such as `references/`, `scripts/`, and `assets/`; their meaning is defined by the underlying coding-agent adapter. AgentRun treats the selected directory as one opaque bundle and exposes no unselected skill directories to the agent.

```text
skills/code-review/
├── SKILL.md
├── references/
├── scripts/
└── assets/
```

#### 6.4.1 Package-Anchored Resolution

Skill resolution is anchored to the agent definition that was resolved in §8.1:

- An agent loaded from `./.agentrun/agents/` resolves its skills only from `./.agentrun/skills/`.
- An agent loaded from `~/.agentrun/agents/` resolves its skills only from `~/.agentrun/skills/`.
- An agent passed by path must reside in an `agents/` directory and resolves its skills only from the sibling `skills/` directory under that package root. For example, `/opt/reviewer/agents/reviewer.yaml` resolves `code-review` as `/opt/reviewer/skills/code-review/`.

AgentRun does not perform an independent local-then-global search for each skill. In particular, a global agent cannot be made to load a project-local skill with the same name. This keeps an agent package reproducible and prevents a workspace from replacing the instructions of a trusted global agent.

Skill names are simple identifiers, such as `code-review`, not relative or absolute paths. AgentRun rejects names containing path separators, `.` or `..` path components, or any value that resolves outside the package's `skills/` directory. It canonicalizes the skill root and selected directory, including symlink resolution, before checking containment.

The `skills` list is ordered and that order is preserved when skills are supplied to the underlying agent. AgentRun does not attempt to interpret prose deeply enough to detect or resolve conflicting skill instructions; authors are responsible for composing compatible skills. Runtime restrictions always take precedence: a skill cannot add tools, broaden filesystem permissions, inject credentials, or otherwise expand capabilities beyond the agent definition and AgentRun execution environment.

#### 6.4.2 Portability and Scope

Reusable agents should be distributed as complete packages containing their definitions, prompts, skills, and any declared extensions:

```text
reviewer-package/
├── agents/reviewer.yaml
├── prompts/reviewer.md.tmpl
├── skills/code-review/SKILL.md
└── extensions/web-search/index.ts
```

Installing the package under either a project's `.agentrun/` directory or the user's `~/.agentrun/` directory therefore requires no path rewriting.

Custom skill roots, arbitrary skill paths, environment-variable expansion in skill references, and ordered cross-package search paths are not supported in v1. If later experience demonstrates a need to combine skills from multiple scopes, that feature should use explicit qualified references (for example, `project:code-review` or `user:security-basics`) so provenance remains visible rather than depending on implicit search precedence.

### 6.5 Tools and Extensions

AgentRun distinguishes a tool's implementation from its activation. Built-in tools are supplied by the pi coding-agent adapter. Custom tools are registered by package-local pi extensions. `tools.extensions` determines which extension code is loaded; `tools.allow` is the complete set of tools exposed to the model after those extensions load.

An extension is executable TypeScript and is therefore trusted code, not data. A tool allowlist limits what the model may invoke, but it is not a security boundary around extension code: an extension can execute during loading or from a pi lifecycle event. AgentRun consequently loads only explicitly declared, package-local extensions and relies on the execution sandbox, network policy, and environment policy to limit their effects.

Extensions use this package layout:

```text
.agentrun/
├── agents/
│   └── researcher.yaml
├── extensions/
│   └── web-search/
│       └── index.ts
└── prompts/
    └── researcher.md.tmpl
```

Extension names follow the same anchored-resolution rules as skills: they are simple identifiers, resolve only from the `extensions/` directory sibling to the resolved `agents/` directory, and are canonicalized after symlink resolution. Absolute paths, path traversal, environment expansion, and cross-package lookup are rejected. Project-local and user-global pi extension auto-discovery is disabled for AgentRun processes, so workspace or global pi configuration cannot add undeclared code or tools to a run.

AgentRun loads declared extensions into a fresh per-run pi configuration, collects their registered tool names, validates `tools.allow`, and activates exactly that allowlist. An omitted or empty allowlist exposes no tools, including built-ins. Duplicate names and built-in overrides are validation failures. Loading one extension does not automatically activate all tools it registers.

Extensions are bundled with the AgentRun package in v1. Declaring an extension never downloads or installs code, and `agentrun run` performs no implicit dependency installation. A missing extension fails validation. Registry packages, Git sources, lockfiles, managed extension caches, and an explicit `install` or `prepare` command are deferred until a concrete distribution need exists.

#### 6.5.1 Concurrent and Orchestrated Runs

Extension loading and tool activation are per run. AgentRun never copies extensions into `~/.pi/agent/extensions`, changes global pi settings, or enables a tool for another run. Multiple agents with different extension sets can therefore run sequentially or concurrently without capability leakage:

```text
researcher  -> built-ins + web-search -> read, web_search
implementer -> built-ins only         -> read, grep, edit, write
summarizer  -> no extensions          -> no tools
```

The agent package may be shared read-only by concurrent runs, but each run receives its own pi configuration, environment, sandbox, active-tool set, and lifecycle. Any future downloaded-extension cache must be immutable and content-addressed; sharing cached bytes must never imply sharing activation or mutable extension state.

### 6.6 Network Access

Tool availability and network access are independent capabilities. Allowing `web_search` lets the model request that tool; it does not itself grant the process unrestricted network access.

- `network.mode: none` is the default and denies tool-originated network access.
- `network.mode: allowlist` permits connections only to exact hostnames listed in `network.hosts`.
- Redirects, resolved IP addresses, alternate ports, and DNS rebinding must not allow a connection to escape the declared policy. Enforcement occurs at the sandbox or egress-proxy boundary, not by asking the extension to comply.
- Connectivity required for AgentRun's configured model provider is treated as runner infrastructure and is permitted separately from the tool network policy. It does not grant general egress to extension code.

The hostname allowlist is intentionally narrow in v1. Wildcards, CIDR ranges, per-tool host policies, and unrestricted network mode are not supported.

### 6.7 Environment and Tool Credentials

The isolated agent environment begins empty except for runtime variables required by AgentRun and the model credential configured under `model`. A caller variable is passed through only when its name appears in `environment.allow`. Definitions contain names, never secret values.

AgentRun reads an allowlisted value from its own environment immediately before starting the run and passes it only to that run. It must redact these values from logs, diagnostics, results, and rendered configuration. A declared variable that is absent is a `CONFIGURATION` failure. Environment allowlists, like extensions and active tools, are per run, so concurrent agents do not inherit one another's credentials.

This mechanism transports credentials to trusted extension code; it does not make that code safe. Authors should grant an extension only the variables and hosts it requires.

## 7. Execution Model

### 7.1 Permission Enforcement

AgentRun does not trust the underlying agent process to respect `read-only` on its own. Enforcement happens at the filesystem/container boundary:

- The workspace is bind-mounted into an isolated execution environment (container or equivalent sandbox) with the mode derived directly from `permission`.
- `read-only` means the mount is genuinely read-only at the OS level — a write attempt fails at the syscall, not by the agent choosing not to try.
- This is the only filesystem permission model AgentRun implements. It does not offer partial/path-scoped filesystem permissions in v1; an agent definition is read-only or read-write for the entire workspace. Network and environment capabilities are governed separately by §6.6 and §6.7.
- The mount mode cannot be overridden independently of `permission`; the declaration and its enforcement therefore cannot disagree.

### 7.2 Invocation Flow

1. Resolve the agent definition (§8.1).
2. Resolve package-local skills and extension entry points, then structurally validate the definition, policies, and supplied inputs (§6.3–§6.7).
3. Render the prompt template with supplied inputs.
4. Prepare a fresh isolated execution environment: mount the workspace per `permission`, establish the network policy, inject the model credential and explicitly allowlisted environment variables, and disable pi's global and project resource auto-discovery.
5. Start the underlying coding agent process with only the selected skill bundles and declared extensions. Load the extensions inside the isolated environment, collect their registered tool names, reject duplicates or unknown allowed names, and activate exactly `tools.allow`.
6. Run the rendered prompt subject to `limits.max_turns` and `limits.timeout_s`.
7. Capture the agent's output and exit status.
8. Emit a single JSON result object to stdout (§7.3).
9. Exit `0` if `status: SUCCESS`, nonzero otherwise.

### 7.3 Result Contract

Exactly one JSON object is written to stdout per run. No other run output goes to stdout — logs, progress, and diagnostics go to stderr.

**Success:**
```json
{
  "status": "SUCCESS",
  "result": "<agent's final output>",
  "turns_used": 4,
  "duration_s": 42.1
}
```

**Failure:**
```json
{
  "status": "FAILURE",
  "error_type": "TIMEOUT",
  "error": "timeout after 600s",
  "turns_used": 15,
  "duration_s": 600.0
}
```

There are exactly two values for `status`: `SUCCESS` and `FAILURE`. AgentRun does not emit any status implying a caller-side decision (e.g. whether to retry, escalate, or block) — see §9 for why this boundary is deliberate.

`error_type` is a stable, machine-readable category. It describes what failed without prescribing what the caller should do next. The v1 values are:

- `VALIDATION`: the definition, inputs, or template are invalid.
- `CONFIGURATION`: local runtime or execution-environment configuration is invalid or unavailable.
- `AUTHENTICATION`: provider credentials are missing or rejected.
- `PROVIDER`: the model provider failed for a reason not covered by authentication.
- `TOOL`: an allowed tool failed and caused the run to fail.
- `TIMEOUT`: the wall-clock timeout was reached.
- `LIMIT`: another configured run limit, such as `max_turns`, was reached.
- `EXECUTION`: the underlying agent process could not start or exited unsuccessfully without a more specific category.
- `INTERNAL`: AgentRun encountered an unexpected internal failure.

New categories may be added in future versions, so callers must handle unknown values. Existing category meanings must not change incompatibly.

`error` is a short human-readable explanation with details specific to this failure. Callers use `error_type`, rather than matching `error` text, for programmatic categorization.

### 7.4 Progress and Streaming

The v1 stdout contract is atomic: AgentRun emits exactly one JSON object when the run completes. It does not stream model output or intermediate machine-readable events to stdout. Human-readable logs and progress may be written to stderr while a run is in progress.

Machine-readable streaming may be introduced in a future version only as an explicit, opt-in output format (for example, JSON Lines with typed events and exactly one terminal event). Such a mode must not change the default single-object JSON contract.

AgentRun does not treat incomplete model output as a successful or recoverable result. A future result schema may expose partial output for diagnostics, but it must identify that output as non-final.

This JSON-on-stdout contract is, by design (§4), the entire integration surface — the same one used by a shell script, a poll loop, or a language-specific SDK wrapping the binary.

## 8. Invocation Interface

### 8.1 Agent Resolution

AgentRun resolves an agent by name using local-then-global precedence, mirroring conventions from tools like `git`:

1. `./.agentrun/agents/<name>.yaml` (project-local)
2. `~/.agentrun/agents/<name>.yaml` (global/user-level)

A full or relative path may also be passed directly instead of a name, bypassing resolution.

### 8.2 CLI

```
agentrun run <agent-name-or-path> \
  --workspace <path> \
  --input <key>=<value> [--input <key>=<value> ...] \
  [--output-format json]
```

- `--workspace`: path to the directory the agent operates on. Required.
- `--input`: repeatable. Supplies one named value for the prompt template.
- `--output-format`: `json` (default and only format in v1; reserved flag for future formats).

Exit code `0` on `SUCCESS`, nonzero on `FAILURE` or a validation/setup error prior to running. All nonzero paths still emit a `FAILURE`-shaped JSON object to stdout where possible (e.g. validation errors), so callers can rely on stdout parsing rather than branching on exit code semantics.

Distributed as a single static Go binary (e.g. via `go install`, or a release binary dropped in `$PATH`) — no runtime or interpreter provisioning required on the caller's side.

## 9. Why AgentRun Has No Retry, Block, or Escalation States

This is worth stating explicitly since it's a common point of confusion when integrating AgentRun into a larger system.

A tempting design is to have AgentRun emit richer statuses — `RETRY`, `BLOCKED`, `NEEDS_HUMAN` — so callers get more out-of-the-box behavior. This is wrong, because AgentRun does not have the information needed to make those judgments correctly:

- Whether a failure is worth retrying depends on the caller's retry budget and how many times this logical task has already failed — state AgentRun doesn't have and shouldn't have, since it would tie a stateless primitive to caller-specific bookkeeping.
- Whether a failure should escalate to a human depends on caller-specific policy (how many failures is too many, what counts as a blocking condition) — not a property of a single run.
- Different callers legitimately want different policies for the identical failure. A polling loop might retry indefinitely with backoff; a graph-based orchestrator might route to a different node after two attempts; a CI job might fail the build immediately. AgentRun emitting one opinionated status would force all callers toward whichever policy that status implies.

By reporting only `SUCCESS`/`FAILURE` plus a factual `error_type` and descriptive `error`, AgentRun stays a fact-reporting primitive rather than a policy engine, and every caller — regardless of whether it's a simple poll loop, a graph orchestrator, or something else entirely — implements retry/escalation logic against the same two-value contract without AgentRun needing to anticipate their control-flow model.

## 10. Example: End-to-End

**Agent definition** (`~/.agentrun/agents/reviewer.yaml`): as in §6.1.

**Invocation from a shell script:**
```bash
#!/usr/bin/env bash
result=$(agentrun run reviewer \
  --workspace "$(pwd)" \
  --input diff="$(git diff main)" \
  --input pr_description="$1")

status=$(echo "$result" | jq -r '.status')
if [ "$status" = "SUCCESS" ]; then
  echo "$result" | jq -r '.result'
else
  echo "Review failed: $(echo "$result" | jq -r '.error')" >&2
  exit 1
fi
```

**Invocation from a program:** callers in any language shell out to the `agentrun` binary as a subprocess, parse the stdout JSON object (§7.3), and map `SUCCESS`/`FAILURE` onto their own control-flow model (retry counters, conditional branching, alerting, etc.) as fits their own design. A language-specific SDK may wrap this subprocess-and-parse pattern, but that wrapper lives outside this spec (§4).

## 11. Resolved Decisions

- **Resolved — skill resolution:** Skills are selected by simple name and resolved only within the package containing the resolved agent definition (§6.4). AgentRun does not use per-agent search paths or independently mix project and user skill scopes. Arbitrary roots and qualified cross-scope references are deferred until demonstrated by real use cases.
- **Resolved — extensions and custom tools:** Custom tools come only from explicitly declared, package-local pi extensions. Extensions are executable trusted code, are loaded and activated per run, and are never implicitly installed or auto-discovered from pi's global or project configuration (§6.5).
- **Resolved — tool selection:** `tools.allow` is a complete allowlist with an empty default. There is no denylist, implicit default tool set, or built-in override in v1 (§6.5).
- **Resolved — runtime capabilities:** Tool activation, network egress, environment-variable passthrough, and workspace access are independent permissions and must all permit an operation. These policies are isolated per run (§6.5–§7.1).
- **Resolved — structured errors:** Failures include the stable `error_type` category defined in §7.3. Callers use it for programmatic categorization while `error` remains human-readable detail.
- **Resolved — path-scoped permissions:** Path-scoped permissions are deferred beyond v1. If introduced, they should be modeled as explicit workspace-relative mounts with a default access mode, not as glob rules. Paths must be canonicalized, remain within the workspace after symlink resolution, and reject traversal. Overlap and precedence semantics must be specified before this feature is added.
- **Resolved — streaming and partial output:** Machine-readable streaming and diagnostic partial output are deferred beyond v1 under the compatibility requirements in §7.4.
- **Resolved — language SDKs:** Per-language SDKs remain separate projects outside this specification. They should be considered only after real callers demonstrate recurring subprocess, platform, or schema-versioning needs, and must remain thin wrappers without orchestration policy.
