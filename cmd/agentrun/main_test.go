package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCLIProcessEmitsReleaseOwnedVersionIdentity(t *testing.T) {
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
	if err := command.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var identity map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &identity); err != nil {
		t.Fatalf("stdout = %q, want version JSON: %v", stdout.String(), err)
	}
	for _, field := range []string{"agentrun_version", "pi_version", "javascript_version", "image_digest"} {
		if identity[field] == "" {
			t.Fatalf("version identity = %#v, missing %s", identity, field)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCLIProcessEmitsOneRunFailureObject(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	binary := filepath.Join(t.TempDir(), "agentrun")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agentrun")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	command := exec.Command(binary, "run", "untrusted-input-canary")
	command.Dir = repositoryRoot
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("Run() error = %v, want exit status 1", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one JSON result: %q (%v)", stdout.String(), err)
	}
	if result["status"] != "FAILURE" || result["error_type"] != "VALIDATION" || bytes.Contains(stdout.Bytes(), []byte("untrusted-input-canary")) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
