package output

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAcceptsEveryJSONValueType(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, schema, response string
	}{
		{"object", `{"type":"object"}`, `{"ok":true}`},
		{"array", `{"type":"array"}`, `["ok"]`},
		{"string", `{"type":"string"}`, `"ok"`},
		{"number", `{"type":"number"}`, `1.25`},
		{"boolean", `{"type":"boolean"}`, `true`},
		{"null", `{"type":"null"}`, `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			validator, err := Compile([]byte(test.schema))
			if err != nil {
				t.Fatal(err)
			}
			value, err := validator.Parse(test.response)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil || string(encoded) != test.response {
				t.Fatalf("value = %s, err = %v", encoded, err)
			}
		})
	}
}

func TestParseRequiresExactlyOneJSONValue(t *testing.T) {
	t.Parallel()

	validator, err := Compile([]byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{
		"```json\n{}\n```",
		"Here is the result: {}",
		"{} trailing prose",
		"{} []",
		`{"unterminated":`,
	} {
		if _, err := validator.Parse(response); err == nil {
			t.Errorf("Parse(%q) succeeded", response)
		}
	}
}

func TestCompileEnforcesDraftAndSelfContainment(t *testing.T) {
	t.Parallel()

	for _, schema := range []string{
		`{"$schema":"https://json-schema.org/draft/2019-09/schema"}`,
		`{"$schema":"https://example.test/schema"}`,
		`{"$ref":"https://example.test/schema"}`,
		`{"$ref":"other-schema.json"}`,
		`{"$dynamicRef":"https://example.test/schema#node"}`,
		`{"type":"object", "properties":{"child":{"$ref":"other.json"}}}`,
	} {
		if _, err := Compile([]byte(schema)); err == nil {
			t.Errorf("Compile(%s) succeeded", schema)
		}
	}
}

func TestCompileSupportsDraft202012KeywordsAndInternalReferences(t *testing.T) {
	t.Parallel()

	validator, err := Compile([]byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$defs": {"positive": {"type": "integer", "exclusiveMinimum": 0}},
  "type": "array",
  "prefixItems": [{"$ref": "#/$defs/positive"}],
  "items": false
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Parse(`[1]`); err != nil {
		t.Fatalf("valid 2020-12 value: %v", err)
	}
	for _, response := range []string{`[0]`, `[1, 2]`} {
		if _, err := validator.Parse(response); err == nil {
			t.Errorf("Parse(%s) succeeded", response)
		}
	}
}

func TestCompileDoesNotTreatInstanceDataAsReferences(t *testing.T) {
	t.Parallel()

	validator, err := Compile([]byte(`{
  "const": {"$ref": "https://example.test/not-a-schema"},
  "examples": [{"$dynamicRef": "https://example.test/not-a-schema"}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Parse(`{"$ref":"https://example.test/not-a-schema"}`); err != nil {
		t.Fatalf("valid const value: %v", err)
	}
}

func TestCompileRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()

	for _, schema := range []string{"", `{`, `{} {}`} {
		if _, err := Compile([]byte(schema)); err == nil {
			t.Errorf("Compile(%q) succeeded", schema)
		}
	}
	_, err := Compile([]byte(`{"type": 3}`))
	if err == nil || !strings.Contains(err.Error(), "invalid output schema") {
		t.Fatalf("Compile(invalid keyword) = %v", err)
	}
}
