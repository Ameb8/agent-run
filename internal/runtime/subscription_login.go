package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// SubscriptionLogin runs the selected runtime's interactive OpenAI Codex
// login flow in a throwaway Pi home. The resulting provider document is
// returned to AgentRun's credential store, never written to a host Pi home.
type SubscriptionLogin struct {
	Verifier Verifier
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	run      func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

func NewSubscriptionLogin(verifier Verifier, stdin io.Reader, stdout, stderr io.Writer) SubscriptionLogin {
	return SubscriptionLogin{Verifier: verifier, Stdin: stdin, Stdout: stdout, Stderr: stderr, run: runInteractive}
}

// Login deliberately supports only Pi's built-in OpenAI Codex subscription
// provider. The user completes /login in the terminal; unrelated credentials
// produced by Pi are discarded by the AgentRun auth store.
func (l SubscriptionLogin) Login(ctx context.Context) ([]byte, error) {
	if runtime.GOOS != "linux" {
		return nil, configurationError("AgentRun v1 requires a Linux host")
	}
	if _, err := l.Verifier.Verify(ctx); err != nil {
		return nil, err
	}
	architecture, err := HostArchitecture()
	if err != nil {
		return nil, err
	}
	image, err := l.Verifier.Manifest.ImageFor(architecture)
	if err != nil {
		return nil, err
	}
	if l.run == nil {
		return nil, configurationError("interactive login runner is unavailable")
	}
	home, err := os.MkdirTemp("", "agentrun-openai-login-")
	if err != nil {
		return nil, configurationError("create private interactive login storage")
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.Chmod(home, 0o700); err != nil {
		return nil, configurationError("secure interactive login storage")
	}
	uid, gid := os.Getuid(), os.Getgid()
	args := []string{
		"run", "--rm", "--interactive", "--tty", "--read-only",
		"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid),
		"--env", "HOME=/agentrun/home",
		"--mount", "type=bind,src=" + home + ",dst=/agentrun/home",
		"--tmpfs", "/tmp:mode=700",
		image.Image, "pi", "--provider", "openai-codex",
	}
	if _, err := fmt.Fprintln(l.Stderr, "Complete the ChatGPT Plus/Pro login in Pi: enter /login and select OpenAI Codex, then exit Pi."); err != nil {
		return nil, err
	}
	if err := l.run(ctx, "docker", args, l.Stdin, l.Stdout, l.Stderr); err != nil {
		return nil, configurationError("interactive OpenAI subscription login did not complete")
	}
	document, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "auth.json"))
	if err != nil {
		return nil, fmt.Errorf("interactive OpenAI subscription login did not produce a credential")
	}
	return document, nil
}

func runInteractive(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	return command.Run()
}
