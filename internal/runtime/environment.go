package runtime

import (
	"errors"
	"sort"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
)

// Environment is the complete caller-controlled portion of one sandbox's
// environment. It is built afresh for each run and is never written to the
// generated configuration or shared process state.
type Environment struct {
	values map[string]string
	redact Redactor
}

// ReadEnvironment obtains precisely the declared names immediately before a
// sandbox is created. A missing name is a setup failure, rather than silently
// becoming an empty optional value.
func ReadEnvironment(names []string, lookup func(string) (string, bool)) (Environment, error) {
	if lookup == nil {
		return Environment{}, configurationError("caller environment is unavailable")
	}
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := lookup(name)
		if !ok {
			return Environment{}, configurationError("declared environment variable %q is missing", name)
		}
		// os.Environ cannot contain NUL, but retain this check at the boundary so
		// alternate callers cannot form an ambiguous Docker --env argument.
		if strings.ContainsRune(value, '\x00') {
			return Environment{}, configurationError("declared environment variable %q contains an invalid value", name)
		}
		values[name] = value
	}
	return Environment{values: values, redact: NewRedactor(values)}, nil
}

// Entries returns deterministic Docker environment entries. No host
// environment is inherited: callers pass these entries explicitly to Docker.
func (e Environment) Entries() []string {
	names := make([]string, 0, len(e.values))
	for name := range e.values {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, name+"="+e.values[name])
	}
	return entries
}

// Redact removes this run's caller values from AgentRun-owned text surfaces.
func (e Environment) Redact(text string) string { return e.redact.Redact(text) }

// Redactor exposes the run-local policy to adjacent execution components that
// need to safely return an error to the CLI.
func (e Environment) Redactor() Redactor { return e.redact }

// Redactor is the single value-redaction policy used for run-owned output,
// diagnostics, and generated/rendered configuration. Empty values are not
// redacted because replacing an empty string would corrupt every surface.
type Redactor struct{ values []string }

func NewRedactor(values map[string]string) Redactor {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := Redactor{values: make([]string, 0, len(unique))}
	for value := range unique {
		result.values = append(result.values, value)
	}
	// Longer strings first prevents a shorter secret that is a prefix of
	// another from leaving the latter partly visible.
	sort.Slice(result.values, func(i, j int) bool { return len(result.values[i]) > len(result.values[j]) })
	return result
}

func (r Redactor) Redact(text string) string {
	for _, value := range r.values {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}

// RedactError preserves a stable command category while ensuring callers do
// not accidentally emit an underlying error containing a run secret.
func (r Redactor) RedactError(err error) error {
	if err == nil {
		return nil
	}
	if command, ok := err.(*contract.CommandError); ok {
		return &contract.CommandError{Category: command.Category, Message: r.Redact(command.Message)}
	}
	return errors.New(r.Redact(err.Error()))
}
