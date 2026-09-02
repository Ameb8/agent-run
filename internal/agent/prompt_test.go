package agent

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestReadInputsCombinesAllForms(t *testing.T) {
	t.Parallel()
	file := t.TempDir() + "/input"
	jsonFile := t.TempDir() + "/inputs.json"
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonFile, []byte(`{"json":"from json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInputs(InputOptions{Values: []string{"direct=from flag"}, Files: []string{"file=" + file}, JSONFiles: []string{jsonFile}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"direct": "from flag", "file": "from file", "json": "from json"}
	if len(got) != len(want) {
		t.Fatalf("inputs = %#v, want %#v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("input %q = %q, want %q", name, got[name], value)
		}
	}
}

func TestReadInputsRejectsInvalidSources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options InputOptions
	}{
		{"duplicate forms", InputOptions{Values: []string{"x=one"}, Files: []string{"x=-"}, Stdin: strings.NewReader("two")}},
		{"two stdin consumers", InputOptions{Files: []string{"x=-"}, JSONFiles: []string{"-"}, Stdin: strings.NewReader("x")}},
		{"nonstring json", InputOptions{JSONFiles: []string{"-"}, Stdin: strings.NewReader(`{"x":1}`)}},
		{"null json", InputOptions{JSONFiles: []string{"-"}, Stdin: strings.NewReader(`{"x":null}`)}},
		{"nul json", InputOptions{JSONFiles: []string{"-"}, Stdin: strings.NewReader(`{"x":"\u0000"}`)}},
		{"invalid direct utf8", InputOptions{Values: []string{"x=" + string([]byte{0xff})}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInputs(tc.options)
			assertPromptError(t, err, contract.ErrorValidation)
		})
	}
}

func TestRenderPromptCompositionAndOptionalInput(t *testing.T) {
	t.Parallel()
	f := newDefinitionFixture(t)
	f.write("prompts/base.tmpl", `{{define "base"}}base: {{.issue}}{{end}}`)
	f.write("prompts/main.tmpl", "{{template \"base\" .}}{{if .feedback}} feedback: {{.feedback}}{{end}}")
	f.definition(replaceTopLevel(validDefinition(), "prompt:\n  template: prompts/main.tmpl\n  includes: [prompts/base.tmpl]\n  inputs:\n    required: [issue]\n    optional: [feedback]\n"))
	definition, err := ParseAndValidate(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	without, err := RenderPrompt(definition, map[string]string{"issue": "one"})
	if err != nil || without != "base: one" {
		t.Fatalf("without optional = %q, %v", without, err)
	}
	with, err := RenderPrompt(definition, map[string]string{"issue": "one", "feedback": "two"})
	if err != nil || with != "base: one feedback: two" {
		t.Fatalf("with optional = %q, %v", with, err)
	}
}

func TestRenderPromptRejectsInvalidReferencesAndDefinitions(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, include, main string }{
		{"unexecuted undeclared", `{{define "unused"}}{{.secret}}{{end}}`, "main"},
		{"undeclared template argument", `{{define "base"}}base{{end}}`, `{{template "base" .secret}}`},
		{"duplicate definition", `{{define "base"}}one{{end}}`, `{{define "base"}}two{{end}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDefinitionFixture(t)
			f.write("prompts/include.tmpl", tc.include)
			f.write("prompts/main.tmpl", tc.main)
			f.definition(replaceTopLevel(validDefinition(), "prompt:\n  template: prompts/main.tmpl\n  includes: [prompts/include.tmpl]\n"))
			definition, err := ParseAndValidate(f.resolution())
			if err != nil {
				t.Fatal(err)
			}
			_, err = RenderPrompt(definition, nil)
			assertPromptError(t, err, contract.ErrorValidation)
		})
	}
}

func TestRenderPromptValidatesInputsAndSize(t *testing.T) {
	t.Parallel()
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "x{{.required}}{{.optional}}")
	f.definition(replaceTopLevel(validDefinition(), "prompt:\n  template: prompts/main.tmpl\n  inputs:\n    required: [required]\n    optional: [optional]\n"))
	definition, err := ParseAndValidate(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	_, err = RenderPrompt(definition, nil)
	assertPromptError(t, err, contract.ErrorValidation)
	_, err = RenderPrompt(definition, map[string]string{"required": "x", "other": "x"})
	assertPromptError(t, err, contract.ErrorValidation)
	_, err = RenderPrompt(definition, map[string]string{
		"required": strings.Repeat("x", MaxInputValueBytes),
		"optional": strings.Repeat("x", MaxInputValueBytes),
	})
	assertPromptError(t, err, contract.ErrorLimit)
}

func assertPromptError(t *testing.T, err error, category contract.ErrorCategory) {
	t.Helper()
	var commandErr *contract.CommandError
	if err == nil || !errors.As(err, &commandErr) || commandErr.Category != category {
		t.Fatalf("error = %v, want %s", err, category)
	}
}
