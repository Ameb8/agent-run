package main_test

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCLIProcessKeepsDiagnosticsOffStdout(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	command := exec.Command("go", "run", "./cmd/agentrun", "version")
	command.Dir = repositoryRoot
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("Run() error = %v, want exit status 1", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got := stderr.String(); got == "" {
		t.Fatal("stderr is empty")
	}
}
