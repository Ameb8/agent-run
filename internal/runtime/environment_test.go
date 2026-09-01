package runtime

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestReadEnvironmentPassesOnlyDeclaredValuesAndRedactsThem(t *testing.T) {
	t.Parallel()
	const first = "extension-secret-canary"
	const second = "second-secret-canary"
	lookups := []string{}
	environment, err := ReadEnvironment([]string{"SECOND", "FIRST"}, func(name string) (string, bool) {
		lookups = append(lookups, name)
		switch name {
		case "FIRST":
			return first, true
		case "SECOND":
			return second, true
		default:
			t.Fatalf("undeclared lookup %q", name)
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lookups, []string{"SECOND", "FIRST"}) {
		t.Fatalf("lookups = %q", lookups)
	}
	if got, want := environment.Entries(), []string{"FIRST=" + first, "SECOND=" + second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %q, want %q", got, want)
	}
	for _, surface := range []string{
		environment.Redact("log " + first + " " + second),
		environment.Redact("generated=" + first),
		environment.Redactor().RedactError(&contract.CommandError{Category: contract.ErrorProvider, Message: "result=" + second}).Error(),
	} {
		if strings.Contains(surface, first) || strings.Contains(surface, second) {
			t.Fatalf("secret leaked from AgentRun-owned surface: %q", surface)
		}
	}
}

func TestReadEnvironmentFailsClosedForMissingDeclaredValue(t *testing.T) {
	t.Parallel()
	_, err := ReadEnvironment([]string{"PRESENT", "MISSING"}, func(name string) (string, bool) {
		return "value", name == "PRESENT"
	})
	var command *contract.CommandError
	if !errors.As(err, &command) || command.Category != contract.ErrorConfiguration {
		t.Fatalf("ReadEnvironment() error = %v, want CONFIGURATION", err)
	}
}

func TestRunEnvironmentsDoNotShareValues(t *testing.T) {
	t.Parallel()
	first, err := ReadEnvironment([]string{"TOKEN"}, func(string) (string, bool) { return "first-run-canary", true })
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadEnvironment([]string{"TOKEN"}, func(string) (string, bool) { return "second-run-canary", true })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(first.Entries(), " "); strings.Contains(got, "second-run-canary") {
		t.Fatalf("first run inherited second value: %q", got)
	}
	if got := strings.Join(second.Entries(), " "); strings.Contains(got, "first-run-canary") {
		t.Fatalf("second run inherited first value: %q", got)
	}
}

func TestRedactorHandlesOverlappingValuesAndPreservesCategories(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(map[string]string{"short": "secret", "long": "secret-value", "empty": ""})
	if got := redactor.Redact("secret-value secret"); got != "[REDACTED] [REDACTED]" {
		t.Fatalf("Redact() = %q", got)
	}
	err := redactor.RedactError(&contract.CommandError{Category: contract.ErrorConfiguration, Message: "secret-value"})
	var command *contract.CommandError
	if !errors.As(err, &command) || command.Category != contract.ErrorConfiguration || strings.Contains(err.Error(), "secret") {
		t.Fatalf("RedactError() = %v", err)
	}
}
