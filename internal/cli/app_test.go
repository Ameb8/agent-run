package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllV1CommandsAreRegistered(t *testing.T) {
	t.Parallel()

	commands := registeredCommands()
	if len(commands) != 8 {
		t.Fatalf("registered command count = %d, want 8", len(commands))
	}

	var stderr bytes.Buffer
	app := New(&stderr)
	for _, args := range [][]string{
		{"run"}, {"validate"}, {"inspect"},
		{"auth", "login", "openai-subscription"},
		{"auth", "logout", "openai-subscription"}, {"version"}, {"doctor"},
	} {
		if exitCode := app.Run(args); exitCode != 1 {
			t.Errorf("Run(%q) exit code = %d, want 1", args, exitCode)
		}
	}
}

func TestDiagnosticsDoNotUseStdout(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	app := New(&stderr)
	if exitCode := app.Run([]string{"unknown"}); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestValidateAndInspectUseStaticSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{definition, "--workspace", workspace}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	if code := app.Run(append([]string{"validate"}, args...)); code != 0 {
		t.Fatalf("validate exit = %d, stderr = %s", code, stderr.String())
	}
	if code := app.Run(append([]string{"inspect"}, args...)); code != 0 {
		t.Fatalf("inspect exit = %d, stderr = %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["origin"] != "path" || got["digest"] == "" || got["resources"] == nil || got["capabilities"] == nil || got["defaults"] == nil || got["paths"] == nil {
		t.Fatalf("inspect output = %#v", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(append([]string{"run"}, append(args, "--expect-agent-digest", "sha256:not-the-package")...)); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "VALIDATION") || !strings.Contains(stderr.String(), "expect-agent-digest") {
		t.Fatalf("digest mismatch diagnostic = %q", stderr.String())
	}
}
