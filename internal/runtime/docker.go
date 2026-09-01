package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// DockerInspector reads Docker's local RepoDigests metadata. docker image
// inspect does not pull images, and this implementation never invokes a run,
// search, tag substitution, pi, or Node.js command.
type DockerInspector struct {
	command commandRunner
}

type commandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func NewDockerInspector() DockerInspector {
	return DockerInspector{command: execRunner{}}
}

func (d DockerInspector) LocalImageDigests(ctx context.Context, image string) ([]string, error) {
	if d.command == nil {
		return nil, fmt.Errorf("docker command runner is unavailable")
	}
	output, err := d.command.Output(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return nil, err
	}
	var references []string
	if err := json.Unmarshal(output, &references); err != nil {
		return nil, fmt.Errorf("decode docker image digests: %w", err)
	}
	digests := make([]string, 0, len(references))
	for _, reference := range references {
		at := strings.LastIndexByte(reference, '@')
		if at < 0 || at == len(reference)-1 {
			continue
		}
		digests = append(digests, reference[at+1:])
	}
	return digests, nil
}
