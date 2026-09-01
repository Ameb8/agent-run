package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestParseAndValidateDefaultsAndResources(t *testing.T) {
	t.Parallel()
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "hello")
	f.write("prompts/base.tmpl", "base")
	f.write("schemas/result.json", `{}`)
	f.write("skills/review/SKILL.md", "instructions")
	f.write("extensions/search/index.ts", "export default {}")
	f.definition(`schema_version: 1
name: reviewer
model:
  provider: openai-compatible
  endpoint: https://models.example/v1
  model: gpt-test
  api_key_env: MODEL_KEY
skills: [review]
tools:
  extensions: [search]
  allow: [read, search_web]
network:
  mode: allowlist
  hosts: [api.example]
environment:
  allow: [SEARCH_KEY]
permission: read-write
prompt:
  template: prompts/main.tmpl
  includes: [prompts/base.tmpl]
  inputs:
    required: [issue]
    optional: [context]
output:
  schema: schemas/result.json
`)

	got, err := ParseAndValidate(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.Limits.MaxTurns != 25 || got.Agent.Limits.TimeoutS != 900 || got.Agent.Network.Mode != contract.NetworkNone && got.Agent.Network.Mode != contract.NetworkAllowlist {
		t.Fatalf("defaults/effective policy = %#v", got.Agent)
	}
	if got.PromptTemplate != f.path("prompts/main.tmpl") || len(got.PromptIncludes) != 1 || got.OutputSchema != f.path("schemas/result.json") || len(got.Skills) != 1 || len(got.Extensions) != 1 {
		t.Fatalf("resolved resources = %#v", got)
	}
}

func TestParseAndValidateAppliesDefaults(t *testing.T) {
	t.Parallel()
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "hello")
	f.definition(validDefinition())
	got, err := ParseAndValidate(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.Network.Mode != contract.NetworkNone || got.Agent.Limits.MaxTurns != contract.DefaultMaxTurns || got.Agent.Limits.TimeoutS != contract.DefaultTimeoutSeconds || got.Agent.Tools.Allow != nil || got.Agent.Tools.Extensions != nil {
		t.Fatalf("effective defaults = %#v", got.Agent)
	}
}

func TestParseAndValidateRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, change, want string }{
		{"unknown field", "unknown: true\n", "field unknown"},
		{"unsupported version", "schema_version: 2\n", "schema_version"},
		{"missing provider conditional", "model:\n  provider: openai-compatible\n  model: x\n", "model.endpoint"},
		{"bad endpoint", "model:\n  provider: openai-compatible\n  endpoint: ftp://example\n  model: x\n  api_key_env: KEY\n", "model.endpoint"},
		{"subscription extras", "model:\n  provider: openai-subscription\n  endpoint: https://example\n  model: x\n", "not supported"},
		{"invalid permission", "permission: maybe\n", "permission"},
		{"hosts with none", "network:\n  hosts: [example.com]\n", "network.hosts"},
		{"empty network mode", "network:\n  mode: ''\n", "network.mode"},
		{"invalid host", "network:\n  mode: allowlist\n  hosts: [127.0.0.1]\n", "network.hosts"},
		{"invalid environment", "environment:\n  allow: [BAD-NAME]\n", "environment.allow"},
		{"model credential cannot enter sandbox", "model:\n  provider: openai-compatible\n  endpoint: https://models.example/v1\n  model: x\n  api_key_env: MODEL_KEY\nenvironment:\n  allow: [MODEL_KEY]\n", "environment.allow must not include model.api_key_env"},
		{"duplicate skills", "skills: [review, review]\n", "duplicate"},
		{"duplicate extension", "tools:\n  extensions: [search, search]\n", "duplicate"},
		{"duplicate input", "prompt:\n  template: prompts/main.tmpl\n  inputs:\n    required: [issue, issue]\n", "duplicate"},
		{"overlapping input", "prompt:\n  template: prompts/main.tmpl\n  inputs:\n    required: [issue]\n    optional: [issue]\n", "both"},
		{"readonly write", "tools:\n  allow: [write]\n", "read-only"},
		{"readonly edit", "tools:\n  allow: [edit]\n", "read-only"},
		{"unknown builtin without extension", "tools:\n  allow: [not_a_tool]\n", "not a v1 built-in"},
		{"nonpositive limits", "limits:\n  max_turns: -1\n", "positive"},
		{"zero limits", "limits:\n  timeout_s: 0\n", "positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDefinitionFixture(t)
			f.write("prompts/main.tmpl", "x")
			f.write("skills/review/SKILL.md", "x")
			f.write("extensions/search/index.ts", "x")
			content := validDefinition()
			if strings.HasPrefix(tc.change, "schema_version:") || strings.HasPrefix(tc.change, "model:") || strings.HasPrefix(tc.change, "permission:") || strings.HasPrefix(tc.change, "prompt:") {
				content = replaceTopLevel(content, tc.change)
			} else {
				content += tc.change
			}
			f.definition(content)
			_, err := ParseAndValidate(f.resolution())
			assertDefinitionValidation(t, err, tc.want)
		})
	}
}

func TestParseAndValidateRejectsResourceEscapesAndMissingEntries(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, setup, field, want string }{
		{"missing prompt", "", "prompt:\n  template: prompts/missing.tmpl\n", "prompt.template"},
		{"prompt symlink escape", "prompt-link", "prompt:\n  template: prompts/link.tmpl\n", "outside"},
		{"skill entry missing", "no-skill-entry", "skills: [review]\n", "SKILL.md"},
		{"extension entry missing", "no-extension-entry", "tools:\n  extensions: [search]\n", "index.ts"},
		{"skill symlink escape", "skill-link", "skills: [review]\n", "outside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDefinitionFixture(t)
			f.write("prompts/main.tmpl", "x")
			outside := filepath.Join(f.root, "outside")
			if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch tc.setup {
			case "prompt-link":
				if err := os.Symlink(outside, f.path("prompts/link.tmpl")); err != nil {
					t.Fatal(err)
				}
			case "skill-link":
				if err := os.MkdirAll(f.path("skills"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), f.path("skills/review")); err != nil {
					t.Fatal(err)
				}
			case "no-extension-entry":
				if err := os.MkdirAll(f.path("extensions/search"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "no-skill-entry":
				if err := os.MkdirAll(f.path("skills/review"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			f.definition(replaceTopLevel(validDefinition(), tc.field))
			_, err := ParseAndValidate(f.resolution())
			assertDefinitionValidation(t, err, tc.want)
		})
	}
}

type definitionFixture struct {
	t                                 *testing.T
	root, packageRoot, definitionPath string
}

func newDefinitionFixture(t *testing.T) definitionFixture {
	root := t.TempDir()
	packageRoot := filepath.Join(root, ".agentrun")
	return definitionFixture{t, root, packageRoot, filepath.Join(packageRoot, "agents", "reviewer.yaml")}
}
func (f definitionFixture) path(name string) string { return filepath.Join(f.packageRoot, name) }
func (f definitionFixture) write(name, content string) {
	path := f.path(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}
func (f definitionFixture) definition(content string) { f.write("agents/reviewer.yaml", content) }
func (f definitionFixture) resolution() Resolution {
	return Resolution{DefinitionPath: f.definitionPath, PackageRoot: f.packageRoot}
}
func assertDefinitionValidation(t *testing.T, err error, fragment string) {
	t.Helper()
	var commandErr *contract.CommandError
	if err == nil || !errors.As(err, &commandErr) || commandErr.Category != contract.ErrorValidation || !strings.Contains(commandErr.Message, fragment) {
		t.Fatalf("error = %v, want VALIDATION containing %q", err, fragment)
	}
}

func validDefinition() string {
	return "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: x\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
}
func replaceTopLevel(original, replacement string) string {
	lines := strings.Split(original, "\n")
	key := strings.Split(replacement, ":")[0] + ":"
	start := -1
	for i, line := range lines {
		if line == key || strings.HasPrefix(line, key+" ") {
			start = i
			break
		}
	}
	if start < 0 {
		return original + replacement
	}
	end := start + 1
	for end < len(lines) && (lines[end] == "" || strings.HasPrefix(lines[end], " ")) {
		end++
	}
	result := make([]string, 0, len(lines)+len(strings.Split(replacement, "\n")))
	result = append(result, lines[:start]...)
	result = append(result, strings.Split(strings.TrimSuffix(replacement, "\n"), "\n")...)
	result = append(result, lines[end:]...)
	return strings.Join(result, "\n")
}
