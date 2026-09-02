// Package cli owns command dispatch, human diagnostics, and the terminal run
// result boundary.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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
	runtimeDoctor     doctorRunner
	subscriptionStore auth.Store
	subscriptionLogin subscriptionLogin
	authSetupError    error
	lookupEnv         func(string) (string, bool)
	stdin             io.Reader
	prepareProvider   func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error)
	runtimeIdentity   contract.RuntimeIdentity
	activeRun         *runFacts
}

type runFacts struct {
	agent   *contract.PackageIdentity
	model   *contract.ModelIdentity
	runtime contract.RuntimeIdentity
}

var fallbackRunSequence uint64

type runtimeVerifier interface {
	Verify(context.Context) (contract.RuntimeIdentity, error)
}

type doctorRunner interface {
	Run(context.Context) agentruntime.DoctorReport
}

type subscriptionLogin interface {
	Login(context.Context) ([]byte, error)
}

func New(stderr io.Writer) *App {
	return NewWithWriters(os.Stdout, stderr)
}

// NewWithWriters is useful to embedding callers and command tests.
func NewWithWriters(stdout, stderr io.Writer) *App {
	app := &App{stderr: stderr, stdout: stdout, stdin: os.Stdin, lookupEnv: os.LookupEnv, prepareProvider: provider.Prepare}
	if manifest, err := agentruntime.LoadManifest(); err == nil {
		if architecture, platformErr := agentruntime.HostArchitecture(); platformErr == nil {
			app.runtimeIdentity, _ = manifest.Identity(agentruntime.BuildVersion, architecture)
		}
	}
	app.subscriptionStore, app.authSetupError = auth.NewStore()
	verifier, err := agentruntime.NewVerifier(agentruntime.NewDockerInspector(), agentruntime.BuildVersion)
	if err == nil {
		app.runtimeVerifier = verifier
		app.runtimeDoctor = agentruntime.NewDoctor(*verifier)
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
	if equalPath(command.Path, []string{"run"}) {
		return a.runAndEmit(commandArgs)
	}
	if err := command.Execute(commandArgs); err != nil {
		a.diagnostic(err.Error())
		return 1
	}
	return 0
}

// runAndEmit is the sole stdout boundary for a run invocation. It starts its
// clock before resolution and serializes only after run has returned, so all
// deferred sandbox and snapshot cleanup is included in duration_s.
func (a *App) runAndEmit(args []string) int {
	started := time.Now()
	runID := newRunID()
	facts := &runFacts{runtime: a.runtimeIdentity}
	a.activeRun = facts
	err := a.run(args)
	a.activeRun = nil

	result := contract.RunResult{
		SchemaVersion: contract.ResultSchemaVersion,
		RunID:         runID,
		Runtime:       facts.runtime,
		Agent:         facts.agent,
		Model:         facts.model,
		DurationS:     time.Since(started).Seconds(),
	}
	if err == nil {
		result.Status = contract.RunStatusSuccess
		result.Result = ""
	} else {
		result.Status = contract.RunStatusFailure
		result.ErrorType, result.Error = runError(err)
	}
	if encodeErr := json.NewEncoder(a.stdout).Encode(result); encodeErr != nil {
		// stdout is the contract transport; an unwritable stream is necessarily
		// an invocation failure and cannot safely be represented on that stream.
		a.diagnostic("write run result")
		return 1
	}
	if result.Status == contract.RunStatusSuccess {
		return 0
	}
	return 1
}

func newRunID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		// crypto/rand failing is exceptionally rare; retain a non-empty,
		// invocation-local correlation value without exposing error details.
		return fmt.Sprintf("run-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&fallbackRunSequence, 1))
	}
	return fmt.Sprintf("run-%x", bytes)
}

func runError(err error) (contract.ErrorCategory, string) {
	var command *contract.CommandError
	if errors.As(err, &command) && command.Category != "" {
		return command.Category, safeRunErrorMessage(command.Category)
	}
	// Do not expose unexpected wrapped values: they can include provider bodies,
	// credentials, prompt material, or environment values.
	return contract.ErrorInternal, "unexpected AgentRun failure"
}

// Command errors often originate at a trust boundary (definition parsing,
// templates, provider transport, or child processes). Their detailed text can
// contain prompt inputs, model output, credentials, environment values, or a
// raw provider body. The stable category is the public diagnostic; keep the
// default detail deliberately bounded and independent of those sources.
func safeRunErrorMessage(category contract.ErrorCategory) string {
	switch category {
	case contract.ErrorValidation:
		return "run validation failed"
	case contract.ErrorConfiguration:
		return "run configuration failed"
	case contract.ErrorAuthentication:
		return "provider authentication failed"
	case contract.ErrorProvider:
		return "model provider request failed"
	case contract.ErrorTool:
		return "allowed tool failed"
	case contract.ErrorOutput:
		return "final output did not satisfy the declared contract"
	case contract.ErrorTimeout:
		return "run timeout reached"
	case contract.ErrorLimit:
		return "run limit reached"
	case contract.ErrorCancelled:
		return "run cancelled"
	case contract.ErrorExecution:
		return "agent runtime execution failed"
	default:
		return "unexpected AgentRun failure"
	}
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
		{Path: []string{"version"}, Execute: a.version},
		{Path: []string{"doctor"}, Execute: a.doctor},
	}
}

// version reports only release-owned identity fields. It does not inspect the
// host and therefore never substitutes a PATH-discovered pi or Node runtime.
func (a *App) version(args []string) error {
	if len(args) != 0 {
		return validationError("usage: version")
	}
	if a.runtimeIdentity.AgentRunVersion == "" || a.runtimeIdentity.PiVersion == "" || a.runtimeIdentity.JavaScriptVersion == "" || a.runtimeIdentity.ImageDigest == "" {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "private runtime manifest is unavailable"}
	}
	return json.NewEncoder(a.stdout).Encode(a.runtimeIdentity)
}

// doctor is deliberately definition-free: it checks the installed runtime
// boundary and the optional managed subscription's presence, but cannot and
// does not contact arbitrary agent providers.
func (a *App) doctor(args []string) error {
	if len(args) != 0 {
		return validationError("usage: doctor")
	}
	if a.runtimeDoctor == nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "private runtime diagnostics are unavailable"}
	}
	report := a.runtimeDoctor.Run(context.Background())
	credential := agentruntime.DoctorCheck{Name: "subscription_auth", Status: agentruntime.DoctorMissing, Detail: "OpenAI subscription authentication is not present; run auth login openai-subscription"}
	if a.authSetupError != nil {
		credential.Status = agentruntime.DoctorFail
		credential.Detail = "OpenAI subscription authentication storage is unavailable"
	} else {
		present, err := a.subscriptionStore.Present()
		if err != nil {
			credential.Status = agentruntime.DoctorFail
			credential.Detail = "OpenAI subscription authentication storage is unavailable"
		} else if present {
			credential.Status = agentruntime.DoctorPass
			credential.Detail = "OpenAI subscription authentication is present (credentials were not validated)"
		}
	}
	report.Checks = append(report.Checks, credential)
	if err := json.NewEncoder(a.stdout).Encode(report); err != nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "write doctor report"}
	}
	if !report.Passing() {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "doctor found host prerequisites that require attention"}
	}
	return nil
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
	options, err := parseRunArgs(args)
	if err != nil {
		return err
	}
	resolution, err := resolveFromArgs(options.definitionArgs)
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
	if a.activeRun != nil {
		a.activeRun.agent = &contract.PackageIdentity{Name: snapshot.Definition.Agent.Name, Digest: snapshot.Digest}
		a.activeRun.model = &contract.ModelIdentity{Provider: snapshot.Definition.Agent.Model.Provider, Requested: snapshot.Definition.Agent.Model.Model}
	}
	if err := agent.VerifyExpectedDigest(snapshot, options.expectedDigest); err != nil {
		return err
	}
	// Inputs are intentionally read before a sandbox exists. Their source files
	// are never mounted implicitly, and all template evaluation reads the
	// immutable snapshot rather than the mutable selected package.
	inputs, err := agent.ReadInputs(agent.InputOptions{
		Values:    options.inputs,
		Files:     options.inputFiles,
		JSONFiles: options.inputsJSON,
		Stdin:     a.stdin,
	})
	if err != nil {
		return err
	}
	if _, err := agent.RenderPrompt(snapshot.Definition, inputs); err != nil {
		return err
	}
	// Runtime verification happens after static package validation but before any
	// extension or model work. It never discovers a host pi or JavaScript binary.
	if a.runtimeVerifier == nil {
		return &contract.CommandError{Category: contract.ErrorConfiguration, Message: "private runtime manifest is unavailable"}
	}
	runtimeIdentity, err := a.runtimeVerifier.Verify(context.Background())
	if err != nil {
		return err
	}
	if a.activeRun != nil {
		a.activeRun.runtime = runtimeIdentity
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

// runOptions separates invocation-only flags from the static package
// resolver. Keeping this parser here prevents list, validate, and inspect
// from accepting execution inputs by accident.
type runOptions struct {
	definitionArgs []string
	expectedDigest string
	inputs         []string
	inputFiles     []string
	inputsJSON     []string
}

func parseRunArgs(args []string) (runOptions, error) {
	if len(args) == 0 {
		return runOptions{}, validationError("usage: run <agent-name-or-path> --workspace <path>")
	}
	options := runOptions{definitionArgs: []string{args[0]}}
	seen := make(map[string]bool)
	for i := 1; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--allow-workspace-agent":
			if seen[flag] {
				return runOptions{}, validationError("%s may be supplied only once", flag)
			}
			seen[flag] = true
			options.definitionArgs = append(options.definitionArgs, flag)
		case "--workspace", "--expect-agent-digest", "--input", "--input-file", "--inputs-json", "--output-format":
			if i+1 >= len(args) {
				return runOptions{}, validationError("%s requires a value", flag)
			}
			i++
			value := args[i]
			switch flag {
			case "--workspace":
				if seen[flag] {
					return runOptions{}, validationError("%s may be supplied only once", flag)
				}
				seen[flag] = true
				options.definitionArgs = append(options.definitionArgs, flag, value)
			case "--expect-agent-digest":
				if seen[flag] {
					return runOptions{}, validationError("%s may be supplied only once", flag)
				}
				seen[flag] = true
				options.expectedDigest = value
			case "--input":
				options.inputs = append(options.inputs, value)
			case "--input-file":
				options.inputFiles = append(options.inputFiles, value)
			case "--inputs-json":
				options.inputsJSON = append(options.inputsJSON, value)
			case "--output-format":
				if seen[flag] {
					return runOptions{}, validationError("%s may be supplied only once", flag)
				}
				seen[flag] = true
				if value != "json" {
					return runOptions{}, validationError("--output-format must be json")
				}
			}
		default:
			return runOptions{}, validationError("unknown option %q", flag)
		}
	}
	return options, nil
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
