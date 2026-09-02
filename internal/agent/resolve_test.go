package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestResolveNamedPrecedenceAndWorkspaceTrust(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	workspaceDefinition := fixture.definition("workspace", "reviewer", "name: reviewer\n")
	userDefinition := fixture.definition("user", "reviewer", "name: reviewer\n")

	_, err := Resolve(fixture.options("reviewer"))
	assertValidation(t, err, "--allow-workspace-agent", workspaceDefinition)

	resolved, err := Resolve(fixture.options("reviewer", withAllowWorkspaceAgent()))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Origin != contract.PackageOriginWorkspace || resolved.DefinitionPath != workspaceDefinition {
		t.Fatalf("workspace resolution = %#v", resolved)
	}
	if resolved.PackageRoot != filepath.Join(fixture.workspace, ".agentrun") {
		t.Fatalf("workspace package root = %q", resolved.PackageRoot)
	}

	if err := os.Remove(workspaceDefinition); err != nil {
		t.Fatal(err)
	}
	resolved, err = Resolve(fixture.options("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Origin != contract.PackageOriginUser || resolved.DefinitionPath != userDefinition {
		t.Fatalf("user resolution = %#v", resolved)
	}
}

func TestResolveMissingAndAbsentUserConfiguration(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	_, err := Resolve(ResolveOptions{Workspace: fixture.workspace, Selection: "missing", CurrentDir: fixture.root, UserHome: fixture.user})
	assertValidation(t, err, "missing")
}

func TestResolveDirectPathIsExplicitAndCanonical(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	direct := fixture.definition("direct", "chosen", "name: unrelated\n")
	link := filepath.Join(fixture.root, "definition-link.yaml")
	if err := os.Symlink(direct, link); err != nil {
		t.Fatal(err)
	}
	workspaceLink := filepath.Join(fixture.root, "workspace-link")
	if err := os.Symlink(fixture.workspace, workspaceLink); err != nil {
		t.Fatal(err)
	}

	options := fixture.options("definition-link.yaml")
	options.Workspace = "workspace-link"
	resolved, err := Resolve(options)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Origin != contract.PackageOriginPath || resolved.DefinitionPath != direct {
		t.Fatalf("direct resolution = %#v", resolved)
	}
	if resolved.PackageRoot != filepath.Join(fixture.direct, ".agentrun") {
		t.Fatalf("direct package root = %q", resolved.PackageRoot)
	}
	if resolved.Workspace != fixture.workspace {
		t.Fatalf("canonical workspace = %q, want %q", resolved.Workspace, fixture.workspace)
	}
}

func TestResolveNamedRejectsNameAndFilenameDisagreement(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.definition("user", "reviewer", "name: other\n")
	_, err := Resolve(fixture.options("reviewer"))
	assertValidation(t, err, "does not match requested name")

	fixture = newFixture(t)
	actual := fixture.definition("user", "other", "name: reviewer\n")
	requested := filepath.Join(fixture.user, ".agentrun", "agents", "reviewer.yaml")
	if err := os.Symlink(actual, requested); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(fixture.options("reviewer"))
	assertValidation(t, err, "filename")
}

func TestResolveRejectsDefinitionsOutsideAgentsDirectory(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	path := filepath.Join(fixture.root, "outside.yaml")
	if err := os.WriteFile(path, []byte("name: reviewer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(fixture.options(path))
	assertValidation(t, err, "agents directory")
}

func TestResolveNamedRejectsSymlinkEscapes(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	escaped := filepath.Join(fixture.root, "escaped.yaml")
	if err := os.WriteFile(escaped, []byte("name: reviewer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(fixture.user, ".agentrun", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escaped, candidate); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(fixture.options("reviewer"))
	assertValidation(t, err, "escapes its agents directory")

	fixture = newFixture(t)
	outsideConfig := filepath.Join(fixture.root, "outside-config")
	if err := os.MkdirAll(filepath.Join(outsideConfig, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideConfig, "agents", "reviewer.yaml"), []byte("name: reviewer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideConfig, filepath.Join(fixture.user, ".agentrun")); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(fixture.options("reviewer"))
	assertValidation(t, err, "escapes its owner")
}

type fixture struct {
	root, workspace, user, direct string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	fixture := fixture{
		root: root, workspace: filepath.Join(root, "workspace"), user: filepath.Join(root, "user"), direct: filepath.Join(root, "direct"),
	}
	for _, path := range []string{fixture.workspace, fixture.user, fixture.direct} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (f fixture) definition(scope, name, content string) string {
	var root string
	switch scope {
	case "workspace":
		root = f.workspace
	case "user":
		root = f.user
	case "direct":
		root = f.direct
	default:
		panic("unknown scope")
	}
	path := filepath.Join(root, ".agentrun", "agents", name+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		panic(err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		panic(err)
	}
	return canonical
}

type option func(*ResolveOptions)

func withAllowWorkspaceAgent() option {
	return func(options *ResolveOptions) { options.AllowWorkspaceAgent = true }
}

func (f fixture) options(selection string, modifiers ...option) ResolveOptions {
	options := ResolveOptions{Workspace: f.workspace, Selection: selection, CurrentDir: f.root, UserHome: f.user}
	for _, modifier := range modifiers {
		modifier(&options)
	}
	return options
}

func assertValidation(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("Resolve() error = nil, want validation failure")
	}
	var commandErr *contract.CommandError
	if !errors.As(err, &commandErr) || commandErr.Category != contract.ErrorValidation {
		t.Fatalf("error = %v, want VALIDATION", err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(commandErr.Message, fragment) {
			t.Errorf("error %q does not contain %q", commandErr.Message, fragment)
		}
	}
}
