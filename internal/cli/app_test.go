package cli

import (
	"bytes"
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
		{"run"}, {"list"}, {"validate"}, {"inspect"},
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
