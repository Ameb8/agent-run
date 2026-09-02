// Package output validates AgentRun's optional structured final output.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	draft202012     = "https://json-schema.org/draft/2020-12/schema"
	draft202012HTTP = "http://json-schema.org/draft/2020-12/schema"
)

// Validator is a compiled, self-contained Draft 2020-12 schema.
type Validator struct {
	schema *jsonschema.Schema
}

// CompileFile validates and compiles a selected package output schema. The
// caller must supply the already-contained snapshot path, rather than an
// arbitrary model-controlled path.
func CompileFile(path string) (*Validator, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read output schema: %w", err)
	}
	return Compile(contents)
}

// Compile validates a self-contained JSON Schema Draft 2020-12 document.
// It deliberately installs no resource loader: an output schema cannot make
// AgentRun read another file or contact a remote endpoint.
func Compile(contents []byte) (*Validator, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid output schema JSON: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, fmt.Errorf("invalid output schema JSON: %w", err)
	}
	if err := checkSchema(document); err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:agentrun:output-schema", document); err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}
	schema, err := compiler.Compile("urn:agentrun:output-schema")
	if err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}
	if schema.DraftVersion != 2020 {
		return nil, errors.New("unsupported output schema dialect")
	}
	return &Validator{schema: schema}, nil
}

// Parse validates response as exactly one JSON value and then validates that
// value against the selected schema. It never strips Markdown or prose.
func (v *Validator) Parse(response string) (any, error) {
	if v == nil || v.schema == nil {
		return nil, errors.New("output validator is unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(response))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("final output is not valid JSON")
	}
	if err := requireEOF(decoder); err != nil {
		return nil, errors.New("final output must be exactly one JSON value")
	}
	if err := v.schema.Validate(value); err != nil {
		return nil, errors.New("final output does not conform to output schema")
	}
	return value, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("contains more than one JSON value")
	}
	return err
}

func checkSchema(value any) error {
	node, ok := value.(map[string]any)
	if !ok { // Boolean schemas are valid; the compiler rejects all other types.
		return nil
	}
	if dialect, ok := node["$schema"]; ok {
		name, ok := dialect.(string)
		if !ok || (name != draft202012 && name != draft202012HTTP) {
			return errors.New("unsupported output schema dialect")
		}
	}
	for _, keyword := range []string{"$ref", "$dynamicRef"} {
		if reference, ok := node[keyword]; ok {
			text, ok := reference.(string)
			if !ok || !strings.HasPrefix(text, "#") {
				return errors.New("output schema must not resolve external references")
			}
		}
	}
	for _, keyword := range []string{"additionalProperties", "propertyNames", "unevaluatedProperties", "not", "if", "then", "else", "contains", "items", "unevaluatedItems", "contentSchema"} {
		if err := checkSchema(node[keyword]); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if err := checkSchemaArray(node[keyword]); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"$defs", "properties", "patternProperties", "dependentSchemas"} {
		if err := checkSchemaMap(node[keyword]); err != nil {
			return err
		}
	}
	return nil
}

func checkSchemaArray(value any) error {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, value := range values {
		if err := checkSchema(value); err != nil {
			return err
		}
	}
	return nil
}

func checkSchemaMap(value any) error {
	values, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, value := range values {
		if err := checkSchema(value); err != nil {
			return err
		}
	}
	return nil
}
