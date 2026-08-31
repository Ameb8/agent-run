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
- An in-process library interface. AgentRun's integration surface is the CLI and its stdout contract (§7.5) — see §4 for why this is a deliberate scope narrowing, not a deferred feature.

## 4. Implementation Language

AgentRun is implemented in Go.

This spec concerns itself only with the CLI. There is no in-process library interface — orchestrators, daemons, and other long-running callers invoke AgentRun as a subprocess and parse its stdout JSON result, the same as any other caller. This is a narrower scope than earlier drafts of this spec, which pursued a Python implementation specifically to support a zero-overhead in-process `from agentrun import load_agent, run` call for Python-based orchestrators.

That tradeoff is reversed here. A self-contained AgentRun installation is the priority: callers install AgentRun once and do not separately install, select, upgrade, or configure the underlying coding-agent runtime. The Linux distribution contains the Go binary and a private runtime image containing the pinned pi and JavaScript runtimes. No system pi, Node.js installation, or independently provisioned coding-agent runtime is used. Docker Engine is the one external execution prerequisite in v1 (§4.2). The in-process ergonomics Python offered were specific to Python callers; a compiled CLI serves callers in any language identically, via the same stdout contract every caller already needs to support.

### 4.1 Runtime and Installation Ownership

AgentRun owns and distributes the pi runtime used to execute agents. Every AgentRun release includes a private OCI image that pins compatible pi and JavaScript runtime versions, and invokes those versions exclusively. The image is installed with AgentRun; `agentrun run` never pulls an image or downloads runtime dependencies. AgentRun never searches `PATH` for pi or Node.js, adopts project-local installations, or falls back to a user-managed pi runtime. Upgrading AgentRun is the only supported way to upgrade its private runtime, so the adapter and runtime can be tested and versioned together.

Users therefore install AgentRun once, optionally establish the single supported subscription login, and may then define and run any number of agents. OpenAI-compatible credentials remain caller-managed environment variables. Agent definitions select their model, skills, tools, and permissions, but creating an agent does not require another pi installation or an independent pi configuration.

AgentRun constructs a fresh pi configuration for each run from the resolved agent definition. This generated configuration is ephemeral and isolated: it does not modify pi's global settings and does not inherit global or project-local pi resources. `agentrun version` reports the AgentRun, pi, private image, and JavaScript runtime versions. `agentrun doctor` verifies the private image digest, Docker access, required sandbox behavior, egress proxy, and presence of subscription authentication without calling a model. It cannot validate credentials or endpoints named by an arbitrary agent definition; `run` reports those failures.

### 4.2 V1 Platform and Host Prerequisites

AgentRun v1 supports Linux only. A supported installation requires a working Docker Engine accessible to the invoking user. Other container engines, rootless compatibility modes, macOS, Windows, and non-container sandbox implementations are outside the v1 contract.

The AgentRun host process may communicate with Docker Engine to create and supervise a run, but the Docker socket is never mounted into the run. The private runtime image is content-addressed and verified before use. If Docker is absent, the image is unavailable or has the wrong digest, or the required mount, process, or network isolation cannot be established, AgentRun fails with `CONFIGURATION` before calling the model. It never falls back to running pi directly on the host or with weaker isolation.

AgentRun's use of “self-contained” means that its coding-agent runtime and language runtime are private and versioned with AgentRun. It does not mean that AgentRun bundles the Linux kernel or Docker Engine.

Language-specific SDKs/libraries that wrap the CLI (subprocess invocation, JSON parsing, typed results) are an explicit non-goal of this spec (§3) and may be built as separate, later, per-language projects on top of the CLI contract defined here — mirroring how callers like graph orchestrators are themselves built on top of AgentRun rather than inside it.

## 5. Core Concepts

### 5.1 Agent Definition

An **agent definition** is a YAML file describing one reusable agent role: what model it uses, what skills and tools it has access to, what filesystem permissions it's granted, and how its prompt is constructed. It does not contain per-run data (that comes from inputs at invocation time).

### 5.2 Run

A **run** is a single invocation of an agent definition against a specific workspace, with a specific set of inputs. A run has exactly one of two outcomes: `SUCCESS` or `FAILURE`.

### 5.3 Workspace

The **workspace** is the directory an agent operates on. It is passed at invocation time, not baked into the agent definition, so the same agent definition can run against any project.

### 5.4 Result

The **result** is a single JSON object emitted to stdout describing the outcome of a run. This is the entire integration surface of an AgentRun invocation.

## 6. Agent Definition Format

Agent definitions are YAML files, conventionally named `<agent-name>.yaml`.

```yaml
schema_version: 1
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
| `schema_version` | yes | Agent definition schema version. The only v1 value is `1`. |
| `name` | yes | Agent identifier, checked against the requested name and definition filename during named resolution (§8). |
| `description` | no | Human-readable summary. |
| `model.provider` | yes | `openai-compatible` (arbitrary endpoint + API key) or `openai-subscription` (uses AgentRun-managed user authentication established once for that provider account; no per-agent endpoint/key needed). |
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
| `prompt.template` | yes | Path relative to the package root containing the `agents/` directory. |
| `prompt.includes` | no | Ordered list of additional package-root-relative template files to parse before the main template. |
| `prompt.inputs.required` | no | Named inputs that must be supplied at invocation time. Missing required inputs are validation errors. |
| `prompt.inputs.optional` | no | Named inputs exposed to the template when supplied. Missing optional inputs render as empty values, allowing conditional template branches. |
| `output.schema` | no | Package-root-relative JSON Schema for the agent's final output (§6.2.4). Omitted means the final output is an unconstrained string. |
| `limits.max_turns` | no | Upper bound on model turns as defined in §7.4.1. Defaults to `25`. |
| `limits.timeout_s` | no | Wall-clock timeout in seconds. Defaults to `900`. |

### 6.1.1 Provider Authentication

Authentication belongs to the AgentRun installation and user, not to an individual agent definition or ephemeral pi configuration.

- For `openai-subscription`, the user authenticates through `agentrun auth login openai-subscription`. V1 supports one active subscription account per Linux user. AgentRun stores the resulting credential in its user configuration directory with owner-only permissions and makes it available only as the model credential for runs selecting that provider. Any number of agents may reuse that authentication. Raw subscription credentials never appear in agent definitions, generated pi configuration files, or the sandbox environment. Headless callers that cannot complete the interactive login flow use `openai-compatible` with a caller-managed API key instead.
- For `openai-compatible`, the definition names an environment variable in `model.api_key_env`. Multiple agents may name the same variable, so the caller supplies a provider credential once in its environment or secret manager rather than configuring each agent separately. AgentRun reads the value immediately before the run and supplies it only to the provider transport; it is not injected into the sandbox environment.
- Agent definitions choose which provider and model to use; they cannot embed, create, overwrite, or select arbitrary stored secret values.

Logging out or replacing a stored provider credential affects subsequent runs using that provider account, not agent definitions. Authentication material must be redacted from logs, diagnostics, results, and rendered configuration.

For `openai-compatible`, `model.endpoint` must be an absolute HTTP or HTTPS URL without embedded user information. AgentRun sends model requests only to that configured origin; redirects to another origin are rejected. Model credentials are attached by AgentRun's provider transport and are not made available to extensions. V1 does not support multiple named accounts for one provider, per-agent stored credentials, or automatic selection among accounts.

### 6.2 Prompt Templates

Templates use Go's `text/template` syntax and its standard predefined functions; AgentRun adds no template functions in v1. Template and include paths are relative to the package root, must remain inside it after symlink resolution, and are never resolved relative to the caller's current directory. Input names must match `[A-Za-z_][A-Za-z0-9_]*` and may not appear in both the required and optional lists. Before rendering, AgentRun creates one template data map containing every declared input: supplied values are strings and each missing optional value is the empty string. Templates are parsed with `missingkey=error`, and validation walks all parsed templates, including named templates, so any reference to an undeclared top-level input is a `VALIDATION` failure even when that branch is not executed. Missing required inputs are also `VALIDATION` failures.

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

For scaffolding shared across multiple agent definitions (e.g. a standing "you are a careful coding agent operating under these constraints" preamble), templates may use Go template's standard `{{template "name" .}}` mechanism. Shared fragments are declared explicitly in `prompt.includes`; AgentRun parses those files in order, then parses and executes `prompt.template`. Duplicate template definitions are validation errors.

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

```yaml
prompt:
  template: prompts/reviewer.md.tmpl
  includes:
    - prompts/_base.md.tmpl
  inputs:
    required: [diff]
```

This remains pure composition — no I/O, no external resolution — and is scoped the same way as any other template feature under §6.2.1.

#### 6.2.4 Optional Structured Output

By default, an agent's final output is an unconstrained string. An agent definition may instead declare `output.schema`, naming a JSON Schema file within the same package. AgentRun then requires the final output to be exactly one JSON value conforming to that schema and returns the parsed value in `result`. Markdown fences or surrounding prose are not stripped. Invalid JSON or a schema mismatch is an `OUTPUT` failure.

This validates shape, not truth. AgentRun does not decide whether the agent's conclusions are correct or whether workspace changes satisfy the caller's task. JSON Schema Draft 2020-12 is the only supported schema dialect in v1; schemas must be self-contained and may not resolve remote references.

### 6.3 Validation

Before running, AgentRun validates an agent definition and its inputs:

- All required fields present.
- `schema_version` is supported, and unknown fields are rejected.
- For named resolution, `name` matches the requested name and the definition filename.
- `prompt.template` resolves to an existing file.
- Every `prompt.includes` and `output.schema` path resolves inside the same package.
- Every name in `prompt.inputs.required` has a corresponding invocation input.
- Every supplied input is declared in either `prompt.inputs.required` or `prompt.inputs.optional`.
- Every skill name is a simple identifier rather than a path, resolves within the agent package's `skills/` directory, and names a directory containing `SKILL.md` (§6.4).
- No skill name appears more than once.
- Every extension name is a simple identifier, resolves within the agent package's `extensions/` directory, and names a directory containing `index.ts` (§6.5).
- Every name in `tools.allow` is a v1 built-in tool or is registered by one of the declared extensions.
- `write` and `edit` are not allowed when `permission` is `read-only`.
- No two declared extensions register the same tool name. Built-in tool overrides are not supported in v1.
- `network.mode` and `network.hosts` form a valid network policy; `hosts` is rejected when the mode is `none`.
- Every entry in `environment.allow` is a syntactically valid environment-variable name. Missing variables are configuration failures before the model is called.
- Limits are positive integers after defaults are applied.

Validation failures are reported in the same shape as run failures (§7.5) and always occur before any model call. Structural validation is completed before the sandbox starts. Because discovering the tools registered by an extension requires executing trusted extension code, that specific validation occurs only after the isolated environment has been established (§7.3), but still before the prompt is sent to the model.

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

- An agent loaded from `<workspace>/.agentrun/agents/` resolves its skills only from `<workspace>/.agentrun/skills/`.
- An agent loaded from `~/.agentrun/agents/` resolves its skills only from `~/.agentrun/skills/`.
- An agent passed by path must reside in an `agents/` directory and resolves its skills only from the sibling `skills/` directory under that package root. For example, `/opt/reviewer/agents/reviewer.yaml` resolves `code-review` as `/opt/reviewer/skills/code-review/`.

AgentRun does not perform an independent local-then-global search for each skill. In particular, a global agent cannot be made to load a project-local skill with the same name. This keeps package composition explicit and prevents a workspace from replacing the instructions of a trusted global agent.

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

AgentRun distinguishes a tool's implementation from its activation. Built-in tools are supplied by the pi coding-agent adapter under stable AgentRun tool names. These names and their broad behavior are part of the v1 definition contract rather than raw pi names. Custom tools are registered under names chosen by package-local pi extensions. `tools.extensions` determines which extension code is loaded; `tools.allow` is the complete set of tools exposed to the model after those extensions load.

An extension is executable TypeScript and is therefore trusted code, not data. A tool allowlist limits what the model may invoke, but it is not a security boundary around extension code: an extension can execute during loading or from a pi lifecycle event, read any workspace content visible to the run, spawn processes available in the private image, and read every variable in `environment.allow`. AgentRun consequently loads only explicitly declared, package-local extensions and relies on the execution sandbox, network policy, and environment policy to limit their effects. Selecting a package containing an extension is an explicit decision to trust that code within those limits.

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

Extensions are bundled with the AgentRun package in v1. An extension may import files by relative path from its own extension directory and may use only the extension interface and runtime modules documented as built into AgentRun's pinned runtime. Bare third-party package imports, package manifests, and dependency installation are not supported. Declaring an extension never downloads or installs code, and `agentrun run` performs no implicit dependency installation. A missing extension or unsupported import fails validation before the model is called. Registry packages, Git sources, lockfiles, managed extension caches, and an explicit `install` or `prepare` command are deferred until a concrete distribution need exists.

#### 6.5.1 V1 Built-In Tools

The complete v1 built-in tool set is:

- `read`: read a file or list a directory inside the workspace.
- `grep`: search file contents inside the workspace.
- `write`: create or replace a workspace file.
- `edit`: apply a targeted change to a workspace file.
- `shell`: execute a non-interactive command with the workspace as its working directory inside the same sandbox.

`read` and `grep` work with either workspace permission. Declaring `write` or `edit` for a `read-only` agent is a `VALIDATION` failure. `shell` is valid in either mode; the OS-level workspace mount determines whether commands can modify workspace files. Commands may write to the run's private temporary storage in either mode. Built-in tools cannot access paths, environment variables, executables, or network destinations beyond the run's existing sandbox capabilities. New built-in names require a future definition schema version; compatible bug fixes do not.

#### 6.5.2 Concurrent and Orchestrated Runs

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
- The runtime container has no direct external network route. Tool traffic passes through an AgentRun-controlled egress proxy that resolves the destination and enforces the hostname allowlist on every connection and redirect. An allowed hostname permits its default port for the requested scheme only: port 80 for HTTP and port 443 for HTTPS. Literal IP addresses and URLs containing user information are rejected. Wildcard DNS, redirects to another hostname, and connections to resolved private, loopback, link-local, or Docker-host addresses are denied.
- Model-provider traffic uses a separate AgentRun-controlled transport restricted to the configured provider origin. AgentRun attaches the model credential outside extension-visible configuration. Provider connectivity therefore does not grant general egress or reveal the credential value to extension code. Because extensions execute inside the agent runtime, a malicious extension could still misuse that transport to make requests to the configured model provider; selecting an extension remains a trust decision.

The hostname allowlist is intentionally narrow in v1. Only HTTP and HTTPS tool traffic is supported; arbitrary TCP and UDP egress are denied. Wildcards, CIDR ranges, custom ports, per-tool host policies, private-network destinations, and unrestricted network mode are not supported.

### 6.7 Environment and Tool Credentials

The isolated agent environment begins empty except for non-secret runtime variables required by AgentRun. Model credentials are handled by the provider transport and are not placed in the environment. A caller variable is passed through only when its name appears in `environment.allow`. Definitions contain names, never secret values.

AgentRun reads an allowlisted value from its own environment immediately before starting the run and passes it only to that run. It must redact these values from logs, diagnostics, results, and rendered configuration. Every name in `environment.allow` is required in v1; a declared variable that is absent is a `CONFIGURATION` failure. Optional environment variables are not supported. Environment allowlists, like extensions and active tools, are per run, so concurrent agents do not inherit one another's credentials.

This mechanism transports credentials to trusted extension code; it does not make that code safe. Authors should grant an extension only the variables and hosts it requires.

## 7. Execution Model

### 7.1 Security Model

AgentRun treats the model and model-generated tool requests as untrusted. It uses filesystem, network, environment, and tool policies to limit what a run can do even when the model ignores its instructions or workspace content contains prompt injection.

Agent packages are a separate trust decision. Skills and prompts control what workspace information is sent to the model, and extensions are executable code that may run during loading; users must trust the package they select. AgentRun constrains an extension with the run sandbox, but does not claim to make malicious extension code safe. `agentrun inspect` exists so callers can see a resolved package's code, origin, digest, and effective capabilities before running it. Workspace-local packages selected by name require the explicit `--allow-workspace-agent` flag (§8.1), preventing an untrusted workspace from silently shadowing a user-level agent.

The v1 safety guarantees are deliberately limited:

- `read-only` prevents workspace modification; it does not prevent the model from reading permitted workspace content.
- Workspace content included in prompts or tool results is sent to the configured model provider. `network.mode: none` blocks tool-originated egress, not model-provider communication, and is not a data-locality guarantee.
- A `read-write` run may modify any file in the workspace, including `.git` and executable project files. Callers that need review or rollback should run against a disposable worktree, copy, or version-controlled checkout.
- Workspace changes are not transactional. A failed, timed-out, limited, cancelled, or invalid-output run may leave partial changes behind, and AgentRun does not roll them back. The result reports runtime outcome, not workspace cleanliness.
- AgentRun isolates the run from undeclared host files, environment variables, credentials, sockets, and network destinations. Selected extensions remain trusted with respect to runtime internals and any credentials explicitly made available to their run. AgentRun does not verify the correctness of the agent's answer or resulting workspace changes.

### 7.2 Permission Enforcement

AgentRun does not trust the underlying agent process to respect `read-only` on its own. Enforcement happens at the filesystem/container boundary:

- The workspace is bind-mounted into the private runtime container with the mode derived directly from `permission`.
- `read-only` means the mount is genuinely read-only at the OS level — a write attempt fails at the syscall, not by the agent choosing not to try.
- This is the only filesystem permission model AgentRun implements. It does not offer partial/path-scoped filesystem permissions in v1; an agent definition is read-only or read-write for the entire workspace. Network and environment capabilities are governed separately by §6.6 and §6.7.
- The mount mode cannot be overridden independently of `permission`; the declaration and its enforcement therefore cannot disagree.

### 7.3 Sandbox Contract

AgentRun v1 enforces this contract with Docker Engine on Linux:

- The canonical workspace is the only host directory mounted for general agent access, with the declared read-only or read-write mode. Only the immutable snapshot of the selected definition and its resolved prompt files, output schema, skills, and extensions is exposed separately as a read-only resource; unselected package contents are not mounted. For a workspace-local package, the underlying `.agentrun` tree is masked from the general workspace mount so a read-write run cannot modify its own or another agent's source package.
- Nested host mount points beneath the workspace are not propagated into the container. If AgentRun cannot prevent such propagation for the selected workspace, it fails with `CONFIGURATION`.
- Symlinks and path traversal do not grant access to host paths outside those mounts.
- Each run receives private temporary storage and cannot observe another concurrent run's temporary files, generated configuration, environment, or processes.
- Host home directories, configuration, credential stores, and process-control sockets are not exposed. Only values explicitly named by `environment.allow` enter the sandbox environment.
- The container has no direct external network route. Network access is available only through the separate provider and allowlisted-tool transports described in §6.6; neither the Docker socket nor host network namespace is exposed.
- On timeout, limit, cancellation, or runtime exit, AgentRun terminates the run's full process tree and removes its ephemeral configuration and temporary storage.

The private image contains the runtime and dependencies needed by built-in tools. Host executables are not inherited. Any additional toolchain must already be present in the private image; v1 has no package field for requesting more system dependencies. `agentrun doctor` reports the tools present in that image. If AgentRun cannot establish this sandbox contract, it fails with `CONFIGURATION` before calling the model rather than running with weaker isolation.

### 7.4 Invocation Flow

1. Create the `run_id`, resolve the agent definition (§8.1), resolve package-local prompts, skills, extension entry points, and an optional output schema, and reject traversal or symlink targets outside the package before reading their contents.
2. Copy the selected package resources into a private immutable snapshot, compute its digest, compare it with `--expect-agent-digest` when supplied, then structurally validate the snapshot, policies, and supplied inputs (§6.2–§6.7). All subsequent steps use the snapshot rather than mutable source files.
3. Render the prompt template with supplied inputs.
4. Prepare a fresh isolated execution environment: mount the workspace per `permission`, establish the network policy, inject explicitly allowlisted environment variables, and disable pi's global and project resource auto-discovery. Model credentials remain in the provider transport outside the runtime container.
5. Start AgentRun's pinned, bundled pi runtime with only the selected skill bundles and declared extensions. Load the extensions inside the isolated environment, collect their registered tool names, reject duplicates or unknown allowed names, and activate exactly `tools.allow`.
6. Run the rendered prompt subject to the turn and timeout semantics in §7.4.1.
7. Capture the agent's final output and exit status, and validate structured output when declared.
8. Emit a single JSON result object to stdout (§7.5).
9. Exit `0` if `status: SUCCESS`, nonzero otherwise.

#### 7.4.1 Turn and Limit Semantics

A **turn** is one model request accepted for processing by the provider and its corresponding assistant response. The initial prompt consumes one turn. After tool calls complete, each follow-up model request containing their results consumes another turn. Individual tool calls, extension hooks, and transport retries of the same model request do not consume additional turns. `turns_used` increments when the provider accepts a request, even if that request later fails.

When `limits.max_turns` has been consumed, AgentRun does not send another model request. If no valid final response has been produced, the run fails with `LIMIT`. The wall-clock timeout includes sandbox startup, extension loading, provider and tool activity, process termination, and cleanup, but not definition resolution, input reading, package snapshotting, structural validation, or prompt rendering. It starts immediately before sandbox preparation. If timeout and another limit are observed together, `TIMEOUT` takes precedence.

AgentRun handles `SIGINT` and `SIGTERM` as cancellation requests: it terminates the full run process tree, performs bounded cleanup, and emits a `CANCELLED` failure when cleanup completes. A second signal or an uncatchable `SIGKILL` may terminate AgentRun without a result object.

### 7.5 Result Contract

Exactly one JSON object is written to stdout for every failure AgentRun handles and for every completed run. AgentRun creates `run_id` before resolution so handled resolution and setup failures also receive one. No child-process output is forwarded directly to stdout: AgentRun captures it, while its own logs, progress, and diagnostics go to stderr. Forced termination by the operating system, a host crash, or an unrecovered AgentRun process crash may prevent any JSON object from being emitted; callers must treat missing or malformed stdout as an invocation failure outside the result contract.

**Success:**
```json
{
  "schema_version": 1,
  "run_id": "01J...",
  "status": "SUCCESS",
  "result": "<agent's final output>",
  "agent": {
    "name": "reviewer",
    "digest": "sha256:..."
  },
  "runtime": {
    "agentrun_version": "1.0.0",
    "pi_version": "...",
    "javascript_version": "...",
    "image_digest": "sha256:..."
  },
  "model": {
    "provider": "openai-compatible",
    "requested": "gpt-4.1"
  },
  "turns_used": 4,
  "duration_s": 42.1
}
```

**Failure:**
```json
{
  "schema_version": 1,
  "run_id": "01J...",
  "status": "FAILURE",
  "error_type": "TIMEOUT",
  "error": "timeout after 600s",
  "agent": {
    "name": "reviewer",
    "digest": "sha256:..."
  },
  "runtime": {
    "agentrun_version": "1.0.0",
    "pi_version": "...",
    "javascript_version": "...",
    "image_digest": "sha256:..."
  },
  "model": {
    "provider": "openai-compatible",
    "requested": "gpt-4.1"
  },
  "turns_used": 15,
  "duration_s": 600.0
}
```

There are exactly two values for `status`: `SUCCESS` and `FAILURE`. `SUCCESS` means the runtime reached a normal final response and, when `output.schema` is declared, that response passed structural validation. It does not mean that the agent's claims are true, its workspace changes are correct, or the caller's larger task is complete. A tool-call error produces `FAILURE` only when the runtime cannot recover and produce a valid final response. AgentRun does not emit any status implying a caller-side decision (e.g. whether to retry, escalate, or block) — see §9 for why this seam is deliberate.

`schema_version` versions this result contract. Callers must reject unsupported values. `run_id` is unique per invocation and is suitable for correlating stdout with diagnostics on stderr. `runtime` is always present and identifies the exact private runtime; `agent` and `model` are omitted when a failure occurs before they can be resolved. `turns_used` follows §7.4.1 and is `0` when the provider accepted no model request. `duration_s` measures wall-clock time from `run_id` creation through cleanup, including validation time; unlike `limits.timeout_s`, it describes the whole invocation. Without `output.schema`, `result` is a JSON string. With `output.schema`, it is the parsed JSON value validated against that schema and may therefore be an object, array, string, number, boolean, or null.

`agent.digest` identifies the effective package content used for the run; it is not a claim that the model's behavior is reproducible. The digest input contains the resolved definition and every selected prompt, output schema, skill, and extension file. Each entry is encoded as the UTF-8 package-relative path, a NUL byte, the decimal byte length, a NUL byte, and the exact file bytes; entries are sorted by their UTF-8 paths and the concatenation is hashed with SHA-256. Directories, timestamps, ownership, permissions, source absolute paths, and unselected package files are excluded. Symlinks are resolved before snapshotting and contribute the target file bytes under the selected package-relative path. Duplicate resolved paths and any target outside the package are validation failures.

The digest covers package content, but not workspace state, inputs, environment values, credentials, operating-system state, or mutable provider model aliases. Results separately report the requested provider and model and the pinned runtime versions. Callers needing package pinning use `--expect-agent-digest`; AgentRun fails with `VALIDATION` before extension execution or model access if the resolved digest differs.

`error_type` is a stable, machine-readable category. It describes what failed without prescribing what the caller should do next. The v1 values are:

- `VALIDATION`: the definition, inputs, or template are invalid.
- `CONFIGURATION`: local runtime or execution-environment configuration is invalid or unavailable.
- `AUTHENTICATION`: provider credentials are missing or rejected.
- `PROVIDER`: the model provider failed for a reason not covered by authentication.
- `TOOL`: an allowed tool failed and caused the run to fail.
- `OUTPUT`: the final response did not satisfy the declared structured-output contract.
- `TIMEOUT`: the wall-clock timeout was reached.
- `LIMIT`: another configured run limit, such as `max_turns`, was reached.
- `CANCELLED`: AgentRun received `SIGINT` or `SIGTERM` and terminated the run.
- `EXECUTION`: the underlying agent process could not start or exited unsuccessfully without a more specific category.
- `INTERNAL`: AgentRun encountered an unexpected internal failure.

New categories may be added in future versions, so callers must handle unknown values. Existing category meanings must not change incompatibly.

`error` is a short human-readable explanation with details specific to this failure. Callers use `error_type`, rather than matching `error` text, for programmatic categorization. AgentRun does not include prompt inputs, model output, environment values, credentials, or raw provider response bodies in `error` or default stderr diagnostics; redaction of arbitrary secrets that the model independently copies into ordinary workspace files or tool output is not guaranteed.

### 7.6 Progress and Streaming

The v1 stdout contract is atomic: AgentRun emits exactly one JSON object when the run completes. It does not stream model output or intermediate machine-readable events to stdout. Human-readable logs and progress may be written to stderr while a run is in progress.

Machine-readable streaming may be introduced in a future version only as an explicit, opt-in output format (for example, JSON Lines with typed events and exactly one terminal event). Such a mode must not change the default single-object JSON contract.

AgentRun does not treat incomplete model output as a successful or recoverable result. A future result schema may expose partial output for diagnostics, but it must identify that output as non-final.

This JSON-on-stdout contract is, by design (§4), the entire integration surface — the same one used by a shell script, a poll loop, or a language-specific SDK wrapping the binary.

## 8. Invocation Interface

### 8.1 Agent Resolution

AgentRun canonicalizes `--workspace` before resolving a named agent. It then uses workspace-local-then-global precedence:

1. `<workspace>/.agentrun/agents/<name>.yaml` (project-local)
2. `~/.agentrun/agents/<name>.yaml` (global/user-level)

A workspace-local definition is used by name only when the invocation includes `--allow-workspace-agent`. Without that flag, the presence of a matching local definition is a `VALIDATION` failure rather than silently falling through to the global definition; the error identifies the local path and explains the required flag. This makes shadowing visible while preserving local-first resolution for callers that deliberately opt in.

A full or relative path may also be passed directly instead of a name, bypassing name resolution. Supplying a path is itself an explicit package selection and does not require `--allow-workspace-agent`. Relative paths are resolved from the caller's current directory. In all cases, the parent of the definition's `agents/` directory is its package root. For named resolution, the definition's `name` must match both the requested name and filename, excluding `.yaml`.

### 8.2 CLI

```
agentrun run <agent-name-or-path> \
  --workspace <path> \
  [--allow-workspace-agent] \
  [--expect-agent-digest <sha256:digest>] \
  [--input <key>=<value> ...] \
  [--input-file <key>=<path-or-> ...] \
  [--inputs-json <path-or->] \
  [--output-format json]
```

- `--workspace`: path to the directory the agent operates on. Required.
- `--allow-workspace-agent`: permits named resolution to select an agent definition from `<workspace>/.agentrun/`. It has no effect when the selected definition is global or passed by path.
- `--expect-agent-digest`: requires the resolved package snapshot to have exactly this digest. This provides simple package pinning for scripts and CI without introducing a package registry or lockfile.
- `--input`: repeatable. Supplies one short named value directly. Because command arguments may be visible to other local processes and are subject to OS size limits, this form should not be used for secrets or large content.
- `--input-file`: repeatable. Reads one named value from a file, or from stdin when the path is `-`. This is the preferred form for large or sensitive values.
- `--inputs-json`: reads a JSON object whose keys are input names and whose values are strings, from a file or stdin when the path is `-`.
- `--output-format`: `json` (default and only format in v1; reserved flag for future formats).

Each input name may be supplied exactly once across all input forms. At most one option may consume stdin. Duplicate names, non-string JSON values, invalid UTF-8, and input values containing a NUL byte are `VALIDATION` failures. Each value is limited to 16 MiB and all input values together are limited to 32 MiB in v1. The rendered prompt and captured final model output are each limited to 32 MiB; exceeding one of these limits is a `LIMIT` failure. AgentRun reads all inputs before sandbox creation; input files are not implicitly exposed to the agent.

Exit code `0` means `SUCCESS`; exit code `1` means AgentRun emitted a handled `FAILURE`. No other exit code is part of the v1 contract because process startup failure, forced termination, or a host-level crash may be reported by the operating system or invoking shell. Callers should parse stdout when it contains a result object and otherwise treat the subprocess invocation itself as failed.

Supporting authoring and installation-management commands are:

```text
agentrun list [--workspace <path>]
agentrun validate <agent-name-or-path> --workspace <path>
agentrun inspect <agent-name-or-path> --workspace <path>
agentrun auth login openai-subscription
agentrun auth logout openai-subscription
agentrun version
agentrun doctor
```

Agents are created by authoring the conventional package layout; v1 has no interactive generator. `list` reports workspace-local and global agent names and shows shadowing, but does not imply that a local agent is trusted. `validate` performs package-level structural validation without calling a model, requiring run inputs, starting Docker, or executing extension code. Its success means only that the static package is well formed; extension registration and runtime availability are validated during `run`, while host prerequisites are checked by `doctor`. `inspect` emits the resolved paths, origin (`workspace`, `user`, or `path`), effective defaults, declared capabilities, selected resources, and agent digest as JSON without executing extensions.

Agent packages are ordinary, unversioned directories in v1. A package name may therefore refer to different content after files are changed or replaced. Registry discovery, dependency resolution, signing, semantic package versions, and install/update/remove commands are outside this specification; packages may be copied into a workspace or user package root by the caller's chosen distribution mechanism. Callers that require immutable package selection pin `--expect-agent-digest`.

AgentRun is distributed as a versioned Linux bundle containing its private runtime image. Users never install pi or Node.js separately and AgentRun never depends on either executable from `PATH`; Docker Engine remains the host prerequisite defined in §4.2.

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
result=$(git diff main | agentrun run reviewer \
  --workspace "$(pwd)" \
  --input-file diff=- \
  --input pr_description="$1")

status=$(echo "$result" | jq -r '.status')
if [ "$status" = "SUCCESS" ]; then
  echo "$result" | jq -r '.result'
else
  echo "Review failed: $(echo "$result" | jq -r '.error')" >&2
  exit 1
fi
```

**Invocation from a program:** callers in any language shell out to the `agentrun` binary as a subprocess, parse the stdout JSON object (§7.5), and map `SUCCESS`/`FAILURE` onto their own control-flow model (retry counters, conditional branching, alerting, etc.) as fits their own design. A language-specific SDK may wrap this subprocess-and-parse pattern, but that wrapper lives outside this spec (§4).

## 11. Resolved Decisions

- **Resolved — v1 platform:** AgentRun v1 supports Linux with Docker Engine only. Releases include a content-addressed private runtime image and never fall back to host execution or a weaker sandbox (§4.1–§4.2).
- **Resolved — skill resolution:** Skills are selected by simple name and resolved only within the package containing the resolved agent definition (§6.4). AgentRun does not use per-agent search paths or independently mix project and user skill scopes. Arbitrary roots and qualified cross-scope references are deferred until demonstrated by real use cases.
- **Resolved — workspace package trust:** Named workspace-local agents require `--allow-workspace-agent`; direct paths remain an explicit selection. This prevents silent local shadowing without adding a trust database or signing system (§8.1).
- **Resolved — extensions and custom tools:** Custom tools come only from explicitly declared, package-local pi extensions. Extensions are executable trusted code, may use only bundled runtime modules and relative package files, are loaded and activated per run, and are never implicitly installed or auto-discovered from pi's global or project configuration (§6.5).
- **Resolved — tool selection:** `tools.allow` is a complete allowlist with an empty default. There is no denylist, implicit default tool set, or built-in override in v1 (§6.5).
- **Resolved — success semantics:** `SUCCESS` reports normal runtime completion and optional output-shape validation, not correctness or completion of a caller's larger task (§7.5).
- **Resolved — optional structured output:** Definitions may supply a package-local JSON Schema. AgentRun validates only the final output's shape and leaves its meaning and correctness to callers (§6.2.4).
- **Resolved — runtime capabilities:** Tool activation, network egress, environment-variable passthrough, and workspace access are independent permissions and must all permit an operation. These policies are isolated per run (§6.5–§7.3).
- **Resolved — security scope:** The model is untrusted, selected packages are trusted, model-provider traffic is distinct from tool egress, and a read-write workspace should be treated as fully mutable (§7.1–§7.3).
- **Resolved — structured errors:** Failures include the stable `error_type` category defined in §7.5. Callers use it for programmatic categorization while `error` remains human-readable detail.
- **Resolved — package identity, not full reproducibility:** Results identify the exact snapshotted agent-package content, bundled runtime, provider, and requested model. Callers may pin the package digest, but AgentRun does not claim reproducible model behavior or hash workspace and input state (§7.4–§8.2).
- **Resolved — lifecycle and mutation:** Turns count accepted model requests, timeouts include sandbox execution and cleanup, cancellation is a factual failure category, and failed read-write runs may leave partial workspace changes (§7.1, §7.4.1, §7.5).
- **Resolved — path-scoped permissions:** Path-scoped permissions are deferred beyond v1. If introduced, they should be modeled as explicit workspace-relative mounts with a default access mode, not as glob rules. Paths must be canonicalized, remain within the workspace after symlink resolution, and reject traversal. Overlap and precedence semantics must be specified before this feature is added.
- **Resolved — streaming and partial output:** Machine-readable streaming and diagnostic partial output are deferred beyond v1 under the compatibility requirements in §7.6.
- **Resolved — language SDKs:** Per-language SDKs remain separate projects outside this specification. They should be considered only after real callers demonstrate recurring subprocess, platform, or schema-versioning needs, and must remain thin wrappers without orchestration policy.
- **Resolved — runtime and installation ownership:** AgentRun owns, pins, and distributes its pi and JavaScript runtimes in a private image. V1 supports one interactive subscription account per Linux user; headless callers use caller-managed OpenAI-compatible credentials (§4.1, §6.1.1).
- **Resolved — package lifecycle:** Agents are authored as ordinary packages, with lightweight `list`, `validate`, and `inspect` commands. Generation, distribution, and installation remain caller concerns (§8.2).
