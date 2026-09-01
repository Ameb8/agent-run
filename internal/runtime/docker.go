package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
)

// DockerInspector reads Docker's local RepoDigests metadata. docker image
// inspect does not pull images, and this implementation never invokes a run,
// search, tag substitution, pi, or Node.js command.
type DockerInspector struct {
	command commandRunner
}

type commandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func NewDockerInspector() DockerInspector {
	return DockerInspector{command: execRunner{}}
}

func (d DockerInspector) LocalImageDigests(ctx context.Context, image string) ([]string, error) {
	if d.command == nil {
		return nil, fmt.Errorf("docker command runner is unavailable")
	}
	output, err := d.command.Output(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return nil, err
	}
	var references []string
	if err := json.Unmarshal(output, &references); err != nil {
		return nil, fmt.Errorf("decode docker image digests: %w", err)
	}
	digests := make([]string, 0, len(references))
	for _, reference := range references {
		at := strings.LastIndexByte(reference, '@')
		if at < 0 || at == len(reference)-1 {
			continue
		}
		digests = append(digests, reference[at+1:])
	}
	return digests, nil
}

const (
	workspaceMount     = "/workspace"
	resourcesMount     = "/agentrun/resources"
	configurationMount = "/agentrun/config"
	temporaryMount     = "/agentrun/tmp"
)

// SandboxRequest contains the already-resolved, immutable paths that may be
// exposed to a run. The command is deliberately supplied by the runtime
// adapter; this package never discovers a host executable.
type SandboxRequest struct {
	Workspace        string
	Resources        string
	Configuration    string
	Temporary        string
	Permission       contract.Permission
	WorkspacePackage bool
	Command          []string
}

// Container is a created, but not started, sandbox. Keeping construction and
// start separate lets the execution layer install its generated configuration
// without ever weakening the mount boundary.
type Container struct {
	ID      string
	command commandRunner
}

// Remove tears down a created container. It is safe to call after a failed or
// completed run and intentionally uses Docker rather than a host namespace.
func (c Container) Remove(ctx context.Context) error {
	if c.ID == "" || c.command == nil {
		return nil
	}
	if _, err := c.command.Output(ctx, "docker", "rm", "--force", c.ID); err != nil {
		return configurationError("remove sandbox container")
	}
	return nil
}

// DockerSandbox establishes the v1 Docker boundary. It verifies the bundled
// image itself, rather than trusting a caller to have done so earlier.
type DockerSandbox struct {
	Verifier Verifier
	command  commandRunner
	goos     string
}

func NewDockerSandbox(verifier Verifier) DockerSandbox {
	return DockerSandbox{Verifier: verifier, command: execRunner{}, goos: runtime.GOOS}
}

// Create verifies the Linux Docker Engine and private image, then creates a
// container whose only host-backed mounts are the workspace and resource
// snapshot. Docker must accept bind-recursive=disabled; this is the fail-closed
// guard that stops pre-existing nested host mounts from entering the run.
func (s DockerSandbox) Create(ctx context.Context, request SandboxRequest) (Container, error) {
	if s.goos == "" {
		s.goos = runtime.GOOS
	}
	if s.goos != "linux" {
		return Container{}, configurationError("AgentRun v1 requires a Linux host")
	}
	if s.command == nil {
		return Container{}, configurationError("docker command runner is unavailable")
	}
	if _, err := s.Verifier.Verify(ctx); err != nil {
		return Container{}, err
	}
	if err := s.verifyEngine(ctx); err != nil {
		return Container{}, err
	}
	workspace, resources, configuration, temporary, err := canonicalSandboxPaths(request)
	if err != nil {
		return Container{}, err
	}
	args, err := s.createArgs(workspace, resources, configuration, temporary, request)
	if err != nil {
		return Container{}, err
	}
	output, err := s.command.Output(ctx, "docker", args...)
	if err != nil || strings.TrimSpace(string(output)) == "" {
		// In particular, an older engine which cannot enforce
		// bind-recursive=disabled reaches this branch. Never retry with a
		// recursively propagated or weaker bind mount.
		return Container{}, configurationError("Docker Engine cannot establish the required sandbox mounts")
	}
	return Container{ID: strings.TrimSpace(string(output)), command: s.command}, nil
}

func (s DockerSandbox) verifyEngine(ctx context.Context) error {
	output, err := s.command.Output(ctx, "docker", "version", "--format", "{{.Server.Os}}")
	if err != nil || strings.TrimSpace(string(output)) != "linux" {
		return configurationError("a Linux Docker Engine is required")
	}
	output, err = s.command.Output(ctx, "docker", "version", "--format", "{{.Server.APIVersion}}")
	if err != nil || !supportsNonRecursiveBind(string(output)) {
		return configurationError("Docker Engine API 1.45 or newer is required for non-recursive bind mounts")
	}
	output, err = s.command.Output(ctx, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return configurationError("Docker Engine security options are unavailable")
	}
	var options []string
	if err := json.Unmarshal(output, &options); err != nil {
		return configurationError("Docker Engine security options are invalid")
	}
	for _, option := range options {
		if strings.EqualFold(option, "name=rootless") {
			return configurationError("rootless Docker is unsupported in AgentRun v1")
		}
	}
	return nil
}

func supportsNonRecursiveBind(version string) bool {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 1 || major == 1 && minor >= 45)
}

func canonicalSandboxPaths(request SandboxRequest) (string, string, string, string, error) {
	if request.Permission != contract.PermissionReadOnly && request.Permission != contract.PermissionReadWrite {
		return "", "", "", "", configurationError("sandbox permission is invalid")
	}
	if len(request.Command) == 0 {
		return "", "", "", "", configurationError("sandbox command is required")
	}
	workspace, err := canonicalDirectory(request.Workspace)
	if err != nil {
		return "", "", "", "", configurationError("workspace: %v", err)
	}
	resources, err := canonicalDirectory(request.Resources)
	if err != nil {
		return "", "", "", "", configurationError("selected resource snapshot: %v", err)
	}
	configuration, err := canonicalDirectory(request.Configuration)
	if err != nil {
		return "", "", "", "", configurationError("generated configuration: %v", err)
	}
	temporary, err := canonicalDirectory(request.Temporary)
	if err != nil {
		return "", "", "", "", configurationError("private temporary storage: %v", err)
	}
	paths := []string{workspace, resources, configuration, temporary}
	for i, path := range paths {
		for _, other := range paths[i+1:] {
			if overlappingPaths(path, other) {
				return "", "", "", "", configurationError("workspace, resources, configuration, and temporary storage must be separate")
			}
		}
	}
	return workspace, resources, configuration, temporary, nil
}

func overlappingPaths(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("is not a directory")
	}
	// docker --mount uses commas as its option separator even when supplied as
	// one argv element. A comma in a host path could otherwise add mount
	// options (including a weaker access mode), so this CLI adapter cannot
	// safely express such a path and rejects it before container creation.
	if strings.ContainsRune(path, ',') {
		return "", fmt.Errorf("path contains an unsupported comma")
	}
	return path, nil
}

func (s DockerSandbox) createArgs(workspace, resources, configuration, temporary string, request SandboxRequest) ([]string, error) {
	workspaceOptions := "type=bind,src=" + workspace + ",dst=" + workspaceMount + ",bind-propagation=rprivate,bind-recursive=disabled"
	if request.Permission == contract.PermissionReadOnly {
		workspaceOptions += ",readonly"
	}
	args := []string{
		"create", "--pull=never", "--network", "none", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "256",
		"--workdir", workspaceMount,
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--mount", workspaceOptions,
		"--mount", "type=bind,src=" + resources + ",dst=" + resourcesMount + ",readonly,bind-propagation=rprivate,bind-recursive=disabled",
		"--mount", "type=bind,src=" + configuration + ",dst=" + configurationMount + ",readonly,bind-propagation=rprivate,bind-recursive=disabled",
		"--mount", "type=bind,src=" + temporary + ",dst=" + temporaryMount + ",bind-propagation=rprivate,bind-recursive=disabled",
	}
	if request.WorkspacePackage {
		args = append(args, "--mount", "type=tmpfs,dst=/workspace/.agentrun,tmpfs-mode=0755")
	}
	args = append(args, s.Verifier.Manifest.Image)
	args = append(args, request.Command...)
	return args, nil
}
