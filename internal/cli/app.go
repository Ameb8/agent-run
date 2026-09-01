// Package cli owns command dispatch and human diagnostics.
// Machine-readable run results are intentionally not emitted here yet.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ameb8/agent-run/internal/agent"
	"github.com/Ameb8/agent-run/internal/auth"
	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
	agentruntime "github.com/Ameb8/agent-run/internal/runtime"
)

type Command struct {
	Path    []string
	Execute func(args []string) error
}

type App struct {
	commands          []Command
	stderr            io.Writer
	stdout            io.Writer
	runtimeVerifier   runtimeVerifier
	subscriptionStore auth.Store
	subscriptionLogin subscriptionLogin
	authSetupError    error
	lookupEnv         func(string) (string, bool)
	prepareProvider   func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error)
}

type runtimeVerifier interface {
	Verify(context.Context) (contract.RuntimeIdentity, error)
}

type subscriptionLogin interface {
	Login(context.Context) ([]byte, error)
}

func New(stderr io.Writer) *App {
	return NewWithWriters(os.Stdout, stderr)
}

// NewWithWriters is useful to embedding callers and command tests.
func NewWithWriters(stdout, stderr io.Writer) *App {
	app := &App{stderr: stderr, stdout: stdout, lookupEnv: os.LookupEnv, prepareProvider: provider.Prepare}
	app.subscriptionStore, app.authSetupError = auth.NewStore()
	verifier, err := agentruntime.NewVerifier(agentruntime.NewDockerInspector(), agentruntime.BuildVersion)
	if err == nil {
		app.runtimeVerifier = verifier
		app.subscriptionLogin = agentruntime.NewSubscriptionLogin(*verifier, os.Stdin, stdout, stderr)
	} else if app.authSetupError == nil {
		app.authSetupError = err
	}
	app.commands = app.registeredCommands()
	return app
}

// Run dispatches a command. Diagnostics only ever go to stderr; stdout is
// reserved for the single JSON run result introduced by the runtime layer.
func (a *App) Run(args []string) int {
	command, commandArgs, ok := a.find(args)
	if !ok {
		a.diagnostic("unknown command")
		return 1
	}
	if err := command.Execute(commandArgs); err != nil {
		a.diagnostic(err.Error())
		return 1
	}
	return 0
}

func (a *App) find(args []string) (Command, []string, bool) {
	for _, command := range a.commands {
		if len(args) < len(command.Path) || !equalPath(args[:len(command.Path)], command.Path) {
			continue
		}
		return command, args[len(command.Path):], true
	}
	return Command{}, nil, false
}

func (a *App) diagnostic(message string) {
	_, _ = fmt.Fprintf(a.stderr, "agentrun: %s\n", message)
}

func equalPath(left, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func (a *App) registeredCommands() []Command {
	return []Command{
		{Path: []string{"run"}, Execute: a.run},
		{Path: []string{"list"}, Execute: a.list},
		{Path: []string{"validate"}, Execute: a.validate},
		{Path: []string{"inspect"}, Execute: a.inspect},
		{Path: []string{"auth", "login", "openai-subscription"}, Execute: a.loginOpenAISubscription},
		{Path: []string{"auth", "logout", "openai-subscription"}, Execute: a.logoutOpenAISubscription},
		stub("version"),
		stub("doctor"),
	}
}

func (a *App) loginOpenAISubscription(args []string) error {
	if len(args) != 0 {
		return validationError("usage: auth login openai-subscription")
	}
	if a.authSetupError != nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "subscription authentication is unavailable"}
	}
	if a.subscriptionLogin == nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "subscription authentication is unavailable"}
	}
	document, err := a.subscriptionLogin.Login(context.Background())
	if err != nil {
		return err
	}
	if err := a.subscriptionStore.Replace(document); err != nil {
		return &contract.CommandError{Category: contract.ErrorAuthentication, Message: "OpenAI subscription login did not produce a usable credential"}
	}
	return nil
}

func (a *App) logoutOpenAISubscription(args []string) error {
	if len(args) != 0 {
		return validationError("usage: auth logout openai-subscription")
	}
	if a.authSetupError != nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "subscription authentication is unavailable"}
	}
	if err := a.subscriptionStore.Logout(); err != nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "subscription authentication storage is unavailable"}
	}
	return nil
}

// run performs the package-selection boundary that must precede all trusted
// extension and model work. The runtime itself is supplied by a later layer.
func (a *App) run(args []string) error {
	var expected string
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--expect-agent-digest" {
			if i+1 >= len(args) {
				return validationError("--expect-agent-digest requires a digest")
			}
			i++
			expected = args[i]
			continue
		}
		filtered = append(filtered, args[i])
	}
	resolution, err := resolveFromArgs(filtered)
	if err != nil {
		return err
	}
	scope, err := agentruntime.NewRunScope()
	if err != nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: fmt.Sprintf("private run storage: %v", err)}
	}
	defer scope.Close()
	snapshot, err := agent.CreateSnapshotIn(resolution, scope.Resources)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	if err := agent.VerifyExpectedDigest(snapshot, expected); err != nil {
		return err
	}
	// Runtime verification happens after static package validation but before any
	// extension or model work. It never discovers a host pi or JavaScript binary.
	if a.runtimeVerifier == nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "private runtime manifest is unavailable"}
	}
	if _, err := a.runtimeVerifier.Verify(context.Background()); err != nil {
		return err
	}
	// Construct Pi's private resource view before provider or sandbox work. The
	// execution adapter consumes this run-local configuration when it creates
	// the pinned-runtime container; no mutable package or user Pi state is ever
	// consulted to discover skills.
	if _, err := agentruntime.GeneratePiConfiguration(scope.Configuration, scope.Temporary, scope.Resources, snapshot); err != nil {
		return err
	}
	// Read extension credentials only after all static validation and runtime
	// checks, immediately before provider/sandbox setup. The returned value is
	// run-local and will be supplied to the Docker sandbox by the execution
	// adapter; it is deliberately distinct from the provider credential below.
	environment, err := agentruntime.ReadEnvironment(snapshot.Definition.Agent.Environment.Allow, a.lookupEnv)
	if err != nil {
		return err
	}
	var subscription auth.Handle
	if snapshot.Definition.Agent.Model.Provider == contract.ProviderOpenAISubscription {
		var credentialErr error
		subscription, credentialErr = a.subscriptionStore.Open()
		if credentialErr != nil {
			if errors.Is(credentialErr, fs.ErrNotExist) {
				return &contract.CommandError{Category: contract.ErrorAuthentication, Message: "OpenAI subscription authentication is required"}
			}
			return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "subscription authentication storage is unavailable"}
		}
	}
	if _, err := a.prepareProvider(snapshot.Definition.Agent.Model, a.lookupEnv, subscription); err != nil {
		return environment.Redactor().RedactError(err)
	}
	return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "command is not implemented"}
}

func registeredCommands() []Command { return NewWithWriters(io.Discard, io.Discard).commands }

func (a *App) list(args []string) error {
	workspace := ""
	if len(args) == 2 && args[0] == "--workspace" {
		workspace = args[1]
	} else if len(args) != 0 {
		return validationError("usage: list [--workspace <path>]")
	}
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return validationError("workspace: %v", err)
		}
	}
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return validationError("workspace: %v", err)
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		if err != nil {
			return validationError("workspace: %v", err)
		}
		return validationError("workspace: is not a directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return validationError("user configuration: %v", err)
	}
	local, err := agentNames(filepath.Join(workspace, ".agentrun", "agents"))
	if err != nil {
		return validationError("workspace agents: %v", err)
	}
	global, err := agentNames(filepath.Join(home, ".agentrun", "agents"))
	if err != nil {
		return validationError("user agents: %v", err)
	}
	names := map[string]bool{}
	for name := range local {
		names[name] = true
	}
	for name := range global {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if local[name] {
			shadow := ""
			if global[name] {
				shadow = " (shadows user)"
			}
			if _, err := fmt.Fprintf(a.stdout, "%s\tworkspace%s\n", name, shadow); err != nil {
				return err
			}
		}
		if global[name] {
			shadow := ""
			if local[name] {
				shadow = " (shadowed by workspace)"
			}
			if _, err := fmt.Fprintf(a.stdout, "%s\tuser%s\n", name, shadow); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentNames(directory string) (map[string]bool, error) {
	result := map[string]bool{}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			result[strings.TrimSuffix(entry.Name(), ".yaml")] = true
		}
	}
	return result, nil
}

func (a *App) validate(args []string) error {
	_, snapshot, err := snapshotFromArgs(args)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	if err := agent.ValidatePrompt(snapshot.Definition); err != nil {
		return err
	}
	return nil
}

func (a *App) inspect(args []string) error {
	resolution, snapshot, err := snapshotFromArgs(args)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	resources, err := snapshotResources(snapshot.Root)
	if err != nil {
		return err
	}
	result := struct {
		Paths struct {
			Definition  string `json:"definition"`
			PackageRoot string `json:"package_root"`
		} `json:"paths"`
		Origin       contract.PackageOrigin     `json:"origin"`
		Defaults     contract.EffectiveDefaults `json:"defaults"`
		Capabilities struct {
			Permission  contract.Permission `json:"permission"`
			Tools       []string            `json:"tools"`
			Extensions  []string            `json:"extensions"`
			Network     contract.Network    `json:"network"`
			Environment []string            `json:"environment"`
		} `json:"capabilities"`
		Resources []string `json:"resources"`
		Digest    string   `json:"digest"`
	}{Origin: resolution.Origin, Defaults: contract.EffectiveDefaults{NetworkMode: snapshot.Definition.Agent.Network.Mode, MaxTurns: snapshot.Definition.Agent.Limits.MaxTurns, TimeoutS: snapshot.Definition.Agent.Limits.TimeoutS}, Resources: resources, Digest: snapshot.Digest}
	result.Paths.Definition, result.Paths.PackageRoot = resolution.DefinitionPath, resolution.PackageRoot
	result.Capabilities.Permission = snapshot.Definition.Agent.Permission
	result.Capabilities.Tools = snapshot.Definition.Agent.Tools.Allow
	result.Capabilities.Extensions = snapshot.Definition.Agent.Tools.Extensions
	result.Capabilities.Network = snapshot.Definition.Agent.Network
	result.Capabilities.Environment = snapshot.Definition.Agent.Environment.Allow
	encoder := json.NewEncoder(a.stdout)
	return encoder.Encode(result)
}

func snapshotFromArgs(args []string) (agent.Resolution, *agent.Snapshot, error) {
	resolution, err := resolveFromArgs(args)
	if err != nil {
		return agent.Resolution{}, nil, err
	}
	snapshot, err := agent.CreateSnapshot(resolution)
	if err != nil {
		return agent.Resolution{}, nil, err
	}
	return resolution, snapshot, nil
}

func resolveFromArgs(args []string) (agent.Resolution, error) {
	if len(args) < 3 {
		return agent.Resolution{}, validationError("usage: <agent-name-or-path> --workspace <path>")
	}
	selection := args[0]
	workspace := ""
	allow := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--workspace":
			if i+1 >= len(args) {
				return agent.Resolution{}, validationError("--workspace requires a path")
			}
			i++
			workspace = args[i]
		case "--allow-workspace-agent":
			allow = true
		default:
			return agent.Resolution{}, validationError("unknown option %q", args[i])
		}
	}
	if workspace == "" {
		return agent.Resolution{}, validationError("--workspace is required")
	}
	resolution, err := agent.Resolve(agent.ResolveOptions{Workspace: workspace, Selection: selection, AllowWorkspaceAgent: allow})
	if err != nil {
		return agent.Resolution{}, err
	}
	return resolution, nil
}

func snapshotResources(root string) ([]string, error) {
	var result []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(rel))
		return nil
	})
	return result, err
}

func validationError(format string, args ...any) error {
	return &contract.CommandError{Category: contract.ErrorValidation, Message: fmt.Sprintf(format, args...)}
}

func stub(path ...string) Command {
	return Command{
		Path: path,
		Execute: func(_ []string) error {
			return &contract.CommandError{
				Category: contract.ErrorConfiguration,
				Message:  "command is not implemented",
			}
		},
	}
}

// IsCommandError lets future command handlers distinguish handled failures
// without matching user-facing diagnostic text.
func IsCommandError(err error) (*contract.CommandError, bool) {
	var commandErr *contract.CommandError
	return commandErr, errors.As(err, &commandErr)
}
