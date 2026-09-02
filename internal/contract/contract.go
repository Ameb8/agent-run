// Package contract defines the shared, transport-independent v1 vocabulary.
//
// It deliberately contains data types only. Parsing agent definitions,
// resolving packages, and serializing run results belong to later layers.
package contract

import (
	"encoding/json"
	"fmt"
)

const (
	DefinitionSchemaVersion = 1
	ResultSchemaVersion     = 1
	DefaultMaxTurns         = 25
	DefaultTimeoutSeconds   = 900
)

// AgentDefinition is the complete v1 agent-definition document shape.
type AgentDefinition struct {
	SchemaVersion int         `json:"schema_version" yaml:"schema_version"`
	Name          string      `json:"name" yaml:"name"`
	Description   string      `json:"description,omitempty" yaml:"description,omitempty"`
	Model         Model       `json:"model" yaml:"model"`
	Skills        []string    `json:"skills,omitempty" yaml:"skills,omitempty"`
	Tools         Tools       `json:"tools,omitempty" yaml:"tools,omitempty"`
	Network       Network     `json:"network,omitempty" yaml:"network,omitempty"`
	Environment   Environment `json:"environment,omitempty" yaml:"environment,omitempty"`
	Permission    Permission  `json:"permission" yaml:"permission"`
	Prompt        Prompt      `json:"prompt" yaml:"prompt"`
	Output        Output      `json:"output,omitempty" yaml:"output,omitempty"`
	Limits        Limits      `json:"limits,omitempty" yaml:"limits,omitempty"`
}

type Model struct {
	Provider  Provider `json:"provider" yaml:"provider"`
	Endpoint  string   `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Model     string   `json:"model" yaml:"model"`
	APIKeyEnv string   `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
}

type Provider string

const (
	ProviderOpenAICompatible   Provider = "openai-compatible"
	ProviderOpenAISubscription Provider = "openai-subscription"
)

type Tools struct {
	Extensions []string `json:"extensions,omitempty" yaml:"extensions,omitempty"`
	Allow      []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

type Network struct {
	Mode  NetworkMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	Hosts []string    `json:"hosts,omitempty" yaml:"hosts,omitempty"`
}

type NetworkMode string

const (
	NetworkNone      NetworkMode = "none"
	NetworkAllowlist NetworkMode = "allowlist"
)

type Environment struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionReadWrite Permission = "read-write"
)

type Prompt struct {
	Template string       `json:"template" yaml:"template"`
	Includes []string     `json:"includes,omitempty" yaml:"includes,omitempty"`
	Inputs   PromptInputs `json:"inputs,omitempty" yaml:"inputs,omitempty"`
}

type PromptInputs struct {
	Required []string `json:"required,omitempty" yaml:"required,omitempty"`
	Optional []string `json:"optional,omitempty" yaml:"optional,omitempty"`
}

type Output struct {
	Schema string `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type Limits struct {
	MaxTurns int `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	TimeoutS int `json:"timeout_s,omitempty" yaml:"timeout_s,omitempty"`
}

// EffectiveDefaults are values applied when an optional definition field is
// omitted. They are reported by inspect after validation in a later task.
type EffectiveDefaults struct {
	NetworkMode NetworkMode `json:"network_mode"`
	MaxTurns    int         `json:"max_turns"`
	TimeoutS    int         `json:"timeout_s"`
}

func V1Defaults() EffectiveDefaults {
	return EffectiveDefaults{
		NetworkMode: NetworkNone,
		MaxTurns:    DefaultMaxTurns,
		TimeoutS:    DefaultTimeoutSeconds,
	}
}

// PackageIdentity identifies the immutable snapshot actually selected for a
// run or inspection. Origin is workspace, user, or path.
type PackageIdentity struct {
	Name   string        `json:"name"`
	Digest string        `json:"digest"`
	Origin PackageOrigin `json:"origin"`
}

type PackageOrigin string

const (
	PackageOriginWorkspace PackageOrigin = "workspace"
	PackageOriginUser      PackageOrigin = "user"
	PackageOriginPath      PackageOrigin = "path"
)

// RuntimeIdentity identifies AgentRun's pinned private runtime.
type RuntimeIdentity struct {
	AgentRunVersion   string `json:"agentrun_version"`
	PiVersion         string `json:"pi_version"`
	JavaScriptVersion string `json:"javascript_version"`
	ImageDigest       string `json:"image_digest"`
}

// ModelIdentity records the selected provider and requested model in a result.
type ModelIdentity struct {
	Provider  Provider `json:"provider"`
	Requested string   `json:"requested"`
}

type RunStatus string

const (
	RunStatusSuccess RunStatus = "SUCCESS"
	RunStatusFailure RunStatus = "FAILURE"
)

// RunResult is the v1 terminal result shape. Result is deliberately any JSON
// value because a declared output schema may validate any JSON type.
// Serialization is owned by a later runtime layer.
type RunResult struct {
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	Status        RunStatus        `json:"status"`
	Result        any              `json:"-"`
	ErrorType     ErrorCategory    `json:"-"`
	Error         string           `json:"-"`
	Agent         *PackageIdentity `json:"agent,omitempty"`
	Runtime       RuntimeIdentity  `json:"runtime"`
	Model         *ModelIdentity   `json:"model,omitempty"`
	TurnsUsed     int              `json:"turns_used"`
	DurationS     float64          `json:"duration_s"`
}

// MarshalJSON enforces the mutually exclusive terminal shapes. In particular,
// a successful structured result of JSON null must still retain its result
// member, while failure objects must not grow a misleading result:null member.
func (r RunResult) MarshalJSON() ([]byte, error) {
	type common struct {
		SchemaVersion int              `json:"schema_version"`
		RunID         string           `json:"run_id"`
		Status        RunStatus        `json:"status"`
		Agent         *PackageIdentity `json:"agent,omitempty"`
		Runtime       RuntimeIdentity  `json:"runtime"`
		Model         *ModelIdentity   `json:"model,omitempty"`
		TurnsUsed     int              `json:"turns_used"`
		DurationS     float64          `json:"duration_s"`
	}
	base := common{r.SchemaVersion, r.RunID, r.Status, r.Agent, r.Runtime, r.Model, r.TurnsUsed, r.DurationS}
	if r.Status == RunStatusSuccess {
		return json.Marshal(struct {
			common
			Result any `json:"result"`
		}{base, r.Result})
	}
	return json.Marshal(struct {
		common
		ErrorType ErrorCategory `json:"error_type"`
		Error     string        `json:"error"`
	}{base, r.ErrorType, r.Error})
}

// ErrorCategory is a stable machine-readable run-failure category. Callers
// must still tolerate categories added by future result schema versions.
type ErrorCategory string

const (
	ErrorValidation     ErrorCategory = "VALIDATION"
	ErrorConfiguration  ErrorCategory = "CONFIGURATION"
	ErrorAuthentication ErrorCategory = "AUTHENTICATION"
	ErrorProvider       ErrorCategory = "PROVIDER"
	ErrorTool           ErrorCategory = "TOOL"
	ErrorOutput         ErrorCategory = "OUTPUT"
	ErrorTimeout        ErrorCategory = "TIMEOUT"
	ErrorLimit          ErrorCategory = "LIMIT"
	ErrorCancelled      ErrorCategory = "CANCELLED"
	ErrorExecution      ErrorCategory = "EXECUTION"
	ErrorInternal       ErrorCategory = "INTERNAL"
)

// CommandError represents a handled command failure. It does not serialize a
// run result; the runtime layer will map it to that contract where applicable.
type CommandError struct {
	Category ErrorCategory
	Message  string
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	if e.Category == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Category, e.Message)
}
