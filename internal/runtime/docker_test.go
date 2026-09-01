package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDockerInspectorUsesOnlyLocalImageInspect(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{output: []byte(`["registry.example/agentrun@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`)}
	inspector := DockerInspector{command: runner}
	digests, err := inspector.LocalImageDigests(context.Background(), "agentrun-runtime:private")
	if err != nil {
		t.Fatal(err)
	}
	if runner.name != "docker" || !reflect.DeepEqual(runner.args, []string{"image", "inspect", "--format", "{{json .RepoDigests}}", "agentrun-runtime:private"}) {
		t.Fatalf("command = %q %q", runner.name, runner.args)
	}
	want := []string{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if !reflect.DeepEqual(digests, want) {
		t.Fatalf("digests = %q, want %q", digests, want)
	}
}

func TestDockerInspectorRejectsUnavailableOrMalformedMetadata(t *testing.T) {
	t.Parallel()

	for _, runner := range []*recordingRunner{{err: errors.New("missing")}, {output: []byte(`not-json`)}} {
		if _, err := (DockerInspector{command: runner}).LocalImageDigests(context.Background(), "image"); err == nil {
			t.Fatal("LocalImageDigests() succeeded")
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
