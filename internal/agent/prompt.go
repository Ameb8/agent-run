package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode/utf8"

	"github.com/Ameb8/agent-run/internal/contract"
)

const (
	// MaxInputValueBytes and MaxInputBytes are the v1 invocation limits.
	MaxInputValueBytes = 16 << 20
	MaxInputBytes      = 32 << 20
	MaxPromptBytes     = 32 << 20
)

// InputOptions is the transport-neutral representation of the three run
// input flags. A dash in Files or JSONFiles consumes Stdin.
type InputOptions struct {
	Values    []string
	Files     []string
	JSONFiles []string
	Stdin     io.Reader
}

// ReadInputs reads caller-supplied input sources. It intentionally has no
// knowledge of an agent package, workspace, or sandbox.
func ReadInputs(options InputOptions) (map[string]string, error) {
	result := make(map[string]string)
	stdinUsed := false
	input := func(spec string, source io.Reader) error {
		name, value, err := splitInput(spec)
		if err != nil {
			return err
		}
		if source != nil {
			bytes, readErr := io.ReadAll(io.LimitReader(source, MaxInputValueBytes+1))
			if readErr != nil {
				return validation("input file %q: %v", value, readErr)
			}
			if len(bytes) > MaxInputValueBytes {
				return validation("input %q exceeds %d bytes", name, MaxInputValueBytes)
			}
			value = string(bytes)
		}
		return addInput(result, name, value)
	}
	for _, spec := range options.Values {
		if err := input(spec, nil); err != nil {
			return nil, err
		}
	}
	for _, spec := range options.Files {
		name, path, err := splitInput(spec)
		if err != nil {
			return nil, err
		}
		var reader io.Reader
		var closeFile func() error
		if path == "-" {
			if stdinUsed {
				return nil, validation("stdin may be consumed by at most one input option")
			}
			stdinUsed = true
			reader = options.Stdin
			if reader == nil {
				reader = os.Stdin
			}
		} else {
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil, validation("input file %q: %v", path, openErr)
			}
			reader, closeFile = file, file.Close
		}
		err = input(name+"="+path, reader)
		if closeFile != nil {
			_ = closeFile()
		}
		if err != nil {
			return nil, err
		}
	}
	for _, path := range options.JSONFiles {
		var reader io.Reader
		var closeFile func() error
		if path == "-" {
			if stdinUsed {
				return nil, validation("stdin may be consumed by at most one input option")
			}
			stdinUsed = true
			reader = options.Stdin
			if reader == nil {
				reader = os.Stdin
			}
		} else {
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil, validation("inputs JSON %q: %v", path, openErr)
			}
			reader, closeFile = file, file.Close
		}
		err := readJSONInputs(reader, result)
		if closeFile != nil {
			_ = closeFile()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := validateInputTotal(result); err != nil {
		return nil, err
	}
	return result, nil
}

func splitInput(spec string) (string, string, error) {
	name, value, ok := strings.Cut(spec, "=")
	if !ok || name == "" {
		return "", "", validation("input must use key=value form")
	}
	if !identifier.MatchString(name) {
		return "", "", validation("input name %q is invalid", name)
	}
	return name, value, nil
}

func addInput(inputs map[string]string, name, value string) error {
	if _, exists := inputs[name]; exists {
		return validation("input %q is supplied more than once", name)
	}
	if !utf8.ValidString(value) {
		return validation("input %q is not valid UTF-8", name)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return validation("input %q contains a NUL byte", name)
	}
	if len(value) > MaxInputValueBytes {
		return validation("input %q exceeds %d bytes", name, MaxInputValueBytes)
	}
	total := len(value)
	for _, existing := range inputs {
		total += len(existing)
	}
	if total > MaxInputBytes {
		return validation("all input values exceed %d bytes", MaxInputBytes)
	}
	inputs[name] = value
	return nil
}

func validateInputTotal(inputs map[string]string) error {
	total := 0
	for _, value := range inputs {
		total += len(value)
	}
	if total > MaxInputBytes {
		return validation("all input values exceed %d bytes", MaxInputBytes)
	}
	return nil
}

func readJSONInputs(reader io.Reader, inputs map[string]string) error {
	decoder := json.NewDecoder(&utf8Reader{reader: reader})
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return validation("inputs JSON must be an object with string values")
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return validation("inputs JSON: %v", err)
		}
		name, ok := token.(string)
		if !ok || !identifier.MatchString(name) {
			return validation("input name %q is invalid", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || len(raw) == 0 || raw[0] != '"' {
			return validation("inputs JSON value for %q must be a string", name)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return validation("inputs JSON value for %q must be a string", name)
		}
		if err := addInput(inputs, name, value); err != nil {
			return err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return validation("inputs JSON: %v", err)
	}
	if decoder.More() {
		return validation("inputs JSON must contain exactly one object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return validation("inputs JSON must contain exactly one object")
	}
	return nil
}

// utf8Reader prevents encoding/json from silently replacing malformed UTF-8
// in JSON strings with U+FFFD. JSON text used as an input source must itself
// be UTF-8, just like the resulting input values.
type utf8Reader struct {
	reader io.Reader
	tail   []byte
}

func (r *utf8Reader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		bytes := append(append([]byte(nil), r.tail...), p[:n]...)
		r.tail = r.tail[:0]
		for len(bytes) > 0 {
			if !utf8.FullRune(bytes) {
				r.tail = append(r.tail, bytes...)
				break
			}
			_, size := utf8.DecodeRune(bytes)
			if size == 1 && bytes[0] >= utf8.RuneSelf {
				return n, errInvalidUTF8
			}
			bytes = bytes[size:]
		}
	}
	if err == io.EOF && len(r.tail) > 0 {
		return n, errInvalidUTF8
	}
	return n, err
}

var errInvalidUTF8 = errors.New("input source is not valid UTF-8")

// RenderPrompt validates supplied inputs and renders the package-contained
// prompt files supplied by ParseAndValidate.
func RenderPrompt(definition Definition, supplied map[string]string) (string, error) {
	if err := validateInputTotal(supplied); err != nil {
		return "", err
	}
	data := make(map[string]string, len(definition.Agent.Prompt.Inputs.Required)+len(definition.Agent.Prompt.Inputs.Optional))
	for _, name := range definition.Agent.Prompt.Inputs.Required {
		data[name] = ""
	}
	for _, name := range definition.Agent.Prompt.Inputs.Optional {
		data[name] = ""
	}
	for name, value := range supplied {
		if _, declared := data[name]; !declared {
			return "", validation("input %q is not declared by the prompt", name)
		}
		if err := addInput(make(map[string]string), name, value); err != nil {
			return "", err
		}
		data[name] = value
	}
	for _, name := range definition.Agent.Prompt.Inputs.Required {
		if _, supplied := supplied[name]; !supplied {
			return "", validation("required input %q is missing", name)
		}
	}
	template, mainName, err := parsePrompt(definition)
	if err != nil {
		return "", err
	}
	var rendered limitBuffer
	rendered.limit = MaxPromptBytes
	if err := template.ExecuteTemplate(&rendered, mainName, data); err != nil {
		if rendered.exceeded {
			return "", limit("rendered prompt exceeds %d bytes", MaxPromptBytes)
		}
		return "", validation("render prompt: %v", err)
	}
	if rendered.exceeded {
		return "", limit("rendered prompt exceeds %d bytes", MaxPromptBytes)
	}
	return rendered.String(), nil
}

func parsePrompt(definition Definition) (*template.Template, string, error) {
	allPaths := append(append([]string(nil), definition.PromptIncludes...), definition.PromptTemplate)
	owners := make(map[string]string)
	templates := template.New("prompt").Option("missingkey=error")
	mainName := ""
	for index, path := range allPaths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, "", validation("prompt template %q: %v", path, err)
		}
		name := fmt.Sprintf("source:%s", filepath.ToSlash(path))
		trees, err := parse.Parse(name, string(contents), "", "")
		if err != nil {
			return nil, "", validation("prompt template %q: %v", path, err)
		}
		for defined, tree := range trees {
			if previous, duplicate := owners[defined]; duplicate {
				return nil, "", validation("template %q is defined by both %q and %q", defined, previous, path)
			}
			owners[defined] = path
			if err := validateTemplateInputs(tree, definition.Agent.Prompt.Inputs); err != nil {
				return nil, "", err
			}
		}
		if _, err := templates.AddParseTree(name, trees[name]); err != nil {
			return nil, "", validation("prompt template %q: %v", path, err)
		}
		for defined, tree := range trees {
			if defined != name {
				if _, err := templates.AddParseTree(defined, tree); err != nil {
					return nil, "", validation("prompt template %q: %v", path, err)
				}
			}
		}
		if index == len(allPaths)-1 {
			mainName = name
		}
	}
	return templates, mainName, nil
}

func validateTemplateInputs(tree *parse.Tree, inputs contract.PromptInputs) error {
	declared := make(map[string]bool, len(inputs.Required)+len(inputs.Optional))
	for _, name := range inputs.Required {
		declared[name] = true
	}
	for _, name := range inputs.Optional {
		declared[name] = true
	}
	var walk func(parse.Node) error
	walk = func(node parse.Node) error {
		if node == nil || reflect.ValueOf(node).IsNil() {
			return nil
		}
		if field, ok := node.(*parse.FieldNode); ok && len(field.Ident) > 0 && !declared[field.Ident[0]] {
			return validation("template %q references undeclared input %q", tree.Name, field.Ident[0])
		}
		switch n := node.(type) {
		case *parse.ListNode:
			for _, child := range n.Nodes {
				if err := walk(child); err != nil {
					return err
				}
			}
		case *parse.ActionNode:
			return walk(n.Pipe)
		case *parse.PipeNode:
			for _, command := range n.Cmds {
				if err := walk(command); err != nil {
					return err
				}
			}
		case *parse.CommandNode:
			for _, arg := range n.Args {
				if err := walk(arg); err != nil {
					return err
				}
			}
		case *parse.TemplateNode:
			return walk(n.Pipe)
		case *parse.IfNode:
			if err := walk(n.Pipe); err != nil {
				return err
			}
			if err := walk(n.List); err != nil {
				return err
			}
			return walk(n.ElseList)
		case *parse.RangeNode:
			if err := walk(n.Pipe); err != nil {
				return err
			}
			if err := walk(n.List); err != nil {
				return err
			}
			return walk(n.ElseList)
		case *parse.WithNode:
			if err := walk(n.Pipe); err != nil {
				return err
			}
			if err := walk(n.List); err != nil {
				return err
			}
			return walk(n.ElseList)
		}
		return nil
	}
	return walk(tree.Root)
}

type limitBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

func limit(format string, args ...any) error {
	return &contract.CommandError{Category: contract.ErrorLimit, Message: fmt.Sprintf(format, args...)}
}
