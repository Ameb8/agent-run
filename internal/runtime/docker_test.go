package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestDockerInspectorUsesOnlyLocalImageInspect(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{output: []byte(`["registry.example/agentrun@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] linux amd64`)}
	inspector := DockerInspector{command: runner}
	image, err := inspector.LocalImage(context.Background(), "agentrun-runtime:private")
	if err != nil {
		t.Fatal(err)
	}
	if runner.name != "docker" || !reflect.DeepEqual(runner.args, []string{"image", "inspect", "--format", "{{json .RepoDigests}} {{.Os}} {{.Architecture}}", "agentrun-runtime:private"}) {
		t.Fatalf("command = %q %q", runner.name, runner.args)
	}
	want := []string{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if !reflect.DeepEqual(image.Digests, want) || image.OS != "linux" || image.Architecture != "amd64" {
		t.Fatalf("image = %#v, want digests %q linux/amd64", image, want)
	}
}

func TestDockerSandboxCreatesHardenedBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &sandboxRunner{}
	sandbox := DockerSandbox{
		Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{image: goodLocalImage()}},
		command:  runner,
		goos:     "linux",
		arch:     "amd64",
	}
	container, err := sandbox.Create(context.Background(), SandboxRequest{
		Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary, Permission: contract.PermissionReadOnly,
		WorkspacePackage: true, Command: []string{"pi", "--help"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if container.ID != "container-id" {
		t.Fatalf("container = %#v", container)
	}
	create := runner.calls[3]
	got := strings.Join(create.args, " ")
	for _, required := range []string{
		"create --pull=never --network none --read-only", "--cap-drop ALL", "--security-opt no-new-privileges:true",
		"--pids-limit 256", "bind-propagation=rprivate,bind-recursive=disabled,readonly",
		"dst=/agentrun/resources,readonly,bind-propagation=rprivate,bind-recursive=disabled",
		"dst=/agentrun/config,readonly,bind-propagation=rprivate,bind-recursive=disabled",
		"dst=/agentrun/tmp,bind-propagation=rprivate,bind-recursive=disabled",
		"type=tmpfs,dst=/workspace/.agentrun,tmpfs-mode=0755", "--tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("docker create arguments missing %q:\n%s", required, got)
		}
	}
	if strings.Contains(got, "--volume") || strings.Contains(got, "--net host") {
		t.Fatalf("unsafe docker arguments: %s", got)
	}
	if err := container.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls[4].args; !reflect.DeepEqual(got, []string{"rm", "--force", "container-id"}) {
		t.Fatalf("remove arguments = %q", got)
	}
}

func TestContainerStartsWaitsAndForceRemovesProcessTree(t *testing.T) {
	t.Parallel()
	runner := &sandboxRunner{}
	container := Container{ID: "container-id", command: runner}
	if err := container.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	exitCode, err := container.Wait(context.Background())
	if err != nil || exitCode != 17 {
		t.Fatalf("Wait() = %d, %v", exitCode, err)
	}
	if err := container.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls; !reflect.DeepEqual(got, []sandboxCall{{args: []string{"start", "container-id"}}, {args: []string{"wait", "container-id"}}, {args: []string{"rm", "--force", "container-id"}}}) {
		t.Fatalf("docker calls = %#v", got)
	}
}

func TestDockerSandboxFailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name   string
		goos   string
		runner *sandboxRunner
	}{
		{name: "unsupported host", goos: "darwin", runner: &sandboxRunner{}},
		{name: "old Docker API", goos: "linux", runner: &sandboxRunner{apiVersion: "1.44"}},
		{name: "rootless engine", goos: "linux", runner: &sandboxRunner{security: `["name=rootless"]`}},
		{name: "engine rejects nonrecursive bind", goos: "linux", runner: &sandboxRunner{createErr: errors.New("unknown bind-recursive")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sandbox := DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{image: goodLocalImage()}}, command: test.runner, goos: test.goos, arch: "amd64"}
			_, err := sandbox.Create(context.Background(), SandboxRequest{Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary, Permission: contract.PermissionReadWrite, Command: []string{"pi"}})
			var commandError *contract.CommandError
			if !errors.As(err, &commandError) || commandError.Category != contract.ErrorConfiguration {
				t.Fatalf("Create() error = %v, want CONFIGURATION", err)
			}
		})
	}
}

func TestDockerSandboxRejectsMountOptionInjectionPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace,readonly=false")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &sandboxRunner{}
	sandbox := DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{image: goodLocalImage()}}, command: runner, goos: "linux", arch: "amd64"}
	_, err := sandbox.Create(context.Background(), SandboxRequest{Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary, Permission: contract.PermissionReadWrite, Command: []string{"pi"}})
	var commandError *contract.CommandError
	if !errors.As(err, &commandError) || commandError.Category != contract.ErrorConfiguration {
		t.Fatalf("Create() error = %v, want CONFIGURATION", err)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "create" {
			t.Fatal("Docker create was called for an unsafe mount path")
		}
	}
}

func TestDockerSandboxRejectsMissingOrOverlappingRunPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range []SandboxRequest{
		{Workspace: workspace, Resources: resources, Temporary: temporary, Permission: contract.PermissionReadOnly, Command: []string{"pi"}},
		{Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: workspace, Permission: contract.PermissionReadOnly, Command: []string{"pi"}},
	} {
		runner := &sandboxRunner{}
		sandbox := DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{image: goodLocalImage()}}, command: runner, goos: "linux", arch: "amd64"}
		_, err := sandbox.Create(context.Background(), request)
		var commandError *contract.CommandError
		if !errors.As(err, &commandError) || commandError.Category != contract.ErrorConfiguration {
			t.Fatalf("Create() error = %v, want CONFIGURATION", err)
		}
		for _, call := range runner.calls {
			if len(call.args) > 0 && call.args[0] == "create" {
				t.Fatal("Docker create was called for invalid run paths")
			}
		}
	}
}

func TestDockerSandboxDerivesWorkspaceMountModeOnlyFromPermission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sandbox := DockerSandbox{Verifier: Verifier{Manifest: testManifest()}, goos: "linux"}
	for _, permission := range []contract.Permission{contract.PermissionReadOnly, contract.PermissionReadWrite} {
		args, err := sandbox.createArgs(workspace, resources, configuration, temporary, SandboxRequest{Permission: permission, Command: []string{"pi"}})
		if err != nil {
			t.Fatal(err)
		}
		var workspaceOption string
		for i := range args {
			if strings.Contains(args[i], "dst=/workspace,") {
				workspaceOption = args[i]
			}
		}
		readonly := strings.Contains(workspaceOption, ",readonly")
		if readonly != (permission == contract.PermissionReadOnly) {
			t.Fatalf("permission %q workspace mount %q", permission, workspaceOption)
		}
	}
}

func TestDockerSandboxInjectsOnlyRunEnvironment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{workspace, resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment, err := ReadEnvironment([]string{"DECLARED"}, func(string) (string, bool) { return "run-secret-canary", true })
	if err != nil {
		t.Fatal(err)
	}
	sandbox := DockerSandbox{Verifier: Verifier{Manifest: testManifest()}, goos: "linux"}
	args, err := sandbox.createArgs(workspace, resources, configuration, temporary, SandboxRequest{Permission: contract.PermissionReadOnly, Environment: environment, Command: []string{"pi"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--env\x00DECLARED=run-secret-canary") {
		t.Fatalf("Docker arguments do not inject declared environment: %q", args)
	}
	if strings.Contains(joined, "HOST_SECRET") || strings.Contains(joined, "MODEL_KEY") {
		t.Fatalf("Docker arguments inherited an undeclared host variable: %q", args)
	}
}

type sandboxCall struct{ args []string }

type sandboxRunner struct {
	calls      []sandboxCall
	security   string
	apiVersion string
	createErr  error
}

func (r *sandboxRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	r.calls = append(r.calls, sandboxCall{args: args})
	if len(args) == 0 {
		return nil, errors.New("missing docker subcommand")
	}
	switch args[0] {
	case "start":
		return []byte("container-id\n"), nil
	case "wait":
		return []byte("17\n"), nil
	case "version":
		if strings.Contains(strings.Join(args, " "), "APIVersion") {
			if r.apiVersion != "" {
				return []byte(r.apiVersion + "\n"), nil
			}
			return []byte("1.45\n"), nil
		}
		return []byte("linux\n"), nil
	case "info":
		if r.security != "" {
			return []byte(r.security), nil
		}
		return []byte(`["name=seccomp"]`), nil
	case "create":
		if r.createErr != nil {
			return nil, r.createErr
		}
		return []byte("container-id\n"), nil
	case "rm":
		return []byte("container-id\n"), nil
	default:
		return nil, fmt.Errorf("unexpected docker arguments %q", args)
	}
}

func TestDockerInspectorRejectsUnavailableOrMalformedMetadata(t *testing.T) {
	t.Parallel()

	for _, runner := range []*recordingRunner{{err: errors.New("missing")}, {output: []byte(`not-json`)}} {
		if _, err := (DockerInspector{command: runner}).LocalImage(context.Background(), "image"); err == nil {
			t.Fatal("LocalImage() succeeded")
		}
	}
}

type recordingRunner struct {
	output []byte
	err    error
	name   string
	args   []string
}

func (r *recordingRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return r.output, r.err
}
