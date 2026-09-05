package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/egress"
)

// DoctorStatus is deliberately factual: a host can distinguish an unsupported
// platform from a missing local prerequisite without parsing prose.
type DoctorStatus string

const (
	DoctorPass        DoctorStatus = "PASS"
	DoctorFail        DoctorStatus = "FAIL"
	DoctorMissing     DoctorStatus = "MISSING"
	DoctorUnsupported DoctorStatus = "UNSUPPORTED"
)

type DoctorCheck struct {
	Name     string       `json:"name"`
	Status   DoctorStatus `json:"status"`
	Detail   string       `json:"detail"`
	Optional bool         `json:"optional,omitempty"`
}

type DoctorReport struct {
	Runtime      contract.RuntimeIdentity `json:"runtime"`
	BuiltInTools []BuiltInTool            `json:"built_in_tools"`
	Checks       []DoctorCheck            `json:"checks"`
}

func (r DoctorReport) Passing() bool {
	for _, check := range r.Checks {
		if !check.Optional && check.Status != DoctorPass {
			return false
		}
	}
	return true
}

// Doctor exercises setup-only runtime seams. It never creates a provider
// transport, reads an agent definition, or starts pi with a prompt.
type Doctor struct {
	sandbox     DockerSandbox
	egressProbe func() DoctorCheck
}

func NewDoctor(verifier Verifier) Doctor {
	return Doctor{sandbox: NewDockerSandbox(verifier), egressProbe: probeEgress}
}

func (d Doctor) Run(ctx context.Context) DoctorReport {
	architecture := d.sandbox.arch
	if architecture == "" {
		architecture = runtimeArchitectureForReport()
	}
	identity, _ := d.sandbox.Verifier.Manifest.Identity(d.sandbox.Verifier.Version, architecture)
	report := DoctorReport{
		Runtime:      identity,
		BuiltInTools: d.sandbox.Verifier.Manifest.BuiltInTools,
	}
	if d.sandbox.goos != "linux" {
		report.Checks = append(report.Checks,
			DoctorCheck{Name: "private_image", Status: DoctorUnsupported, Detail: "AgentRun v1 requires a Linux host"},
			DoctorCheck{Name: "docker", Status: DoctorUnsupported, Detail: "AgentRun v1 requires a Linux Docker Engine"},
			DoctorCheck{Name: "sandbox", Status: DoctorUnsupported, Detail: "required sandbox isolation is supported only on Linux"},
			DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "private runtime cannot be probed on an unsupported host"},
			d.probeEgress(),
		)
		return report
	}
	if err := d.sandbox.verifyEngine(ctx); err != nil {
		// An inaccessible engine makes image metadata unknowable; do not report
		// the image itself as absent in that case.
		report.Checks = append(report.Checks, DoctorCheck{Name: "private_image", Status: DoctorMissing, Detail: "private runtime image cannot be inspected until Docker Engine is available"})
		report.Checks = append(report.Checks, doctorFailure("docker", err))
		// Creating a probe cannot add useful evidence if Docker itself is not
		// usable.  Report it explicitly rather than implying a configuration
		// file was sufficient.
		report.Checks = append(report.Checks, DoctorCheck{Name: "sandbox", Status: DoctorMissing, Detail: "Docker Engine cannot create the required sandbox"})
		report.Checks = append(report.Checks, DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "private runtime cannot be probed without Docker"})
	} else if _, err := d.sandbox.Verifier.VerifyPlatform(ctx, d.sandbox.goos, d.sandbox.arch); err != nil {
		report.Checks = append(report.Checks,
			doctorFailure("private_image", err),
			DoctorCheck{Name: "sandbox", Status: DoctorMissing, Detail: "required sandbox cannot be probed until the private runtime image is installed"},
			DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "private runtime tools cannot be probed until the private runtime image is installed"},
		)
	} else {
		report.Checks = append(report.Checks, doctorPass("private_image", "installed private image matches the release digest"))
		report.Checks = append(report.Checks, doctorPass("docker", "Linux Docker Engine supports required non-recursive mounts"))
		sandbox, tools := d.probeSandbox(ctx)
		report.Checks = append(report.Checks, sandbox, tools)
	}
	report.Checks = append(report.Checks, d.probeEgress())
	return report
}

func runtimeArchitectureForReport() string {
	architecture, err := HostArchitecture()
	if err != nil {
		return ""
	}
	return architecture
}

func (d Doctor) probeEgress() DoctorCheck {
	if d.egressProbe == nil {
		return probeEgress()
	}
	return d.egressProbe()
}

func (d Doctor) probeSandbox(ctx context.Context) (DoctorCheck, DoctorCheck) {
	workspace, err := os.MkdirTemp("", "agentrun-doctor-workspace-")
	if err != nil {
		return DoctorCheck{Name: "sandbox", Status: DoctorFail, Detail: "cannot create an isolated sandbox probe"}, DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "sandbox probe is unavailable"}
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	scope, err := NewRunScope()
	if err != nil {
		return DoctorCheck{Name: "sandbox", Status: DoctorFail, Detail: "cannot create private sandbox storage"}, DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "sandbox probe is unavailable"}
	}
	defer func() { _ = scope.Close() }()
	container, err := d.sandbox.Create(ctx, SandboxRequest{
		Workspace: workspace, Resources: scope.Resources, Configuration: scope.Configuration, Temporary: scope.Temporary,
		Permission: contract.PermissionReadOnly, Command: []string{"bash", "-c", "pi --version && node --version && command -v bash && command -v rg"},
	})
	if err != nil {
		return doctorFailure("sandbox", err), DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "sandbox probe could not be created"}
	}
	defer func() { _ = container.Remove(context.Background()) }()
	if err := d.inspectProbe(ctx, container.ID, workspace, scope.Resources, scope.Configuration, scope.Temporary); err != nil {
		return doctorFailure("sandbox", err), DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "sandbox isolation probe failed"}
	}
	if err := container.Start(ctx); err != nil {
		return doctorFailure("sandbox", err), DoctorCheck{Name: "bundled_tools", Status: DoctorMissing, Detail: "private image could not start"}
	}
	status, err := container.Wait(ctx)
	if err != nil || status != 0 {
		return DoctorCheck{Name: "sandbox", Status: DoctorPass, Detail: "required mount, process, and network isolation is enforced"}, DoctorCheck{Name: "bundled_tools", Status: DoctorFail, Detail: "private image is missing a required bundled tool"}
	}
	return DoctorCheck{Name: "sandbox", Status: DoctorPass, Detail: "required mount, process, and network isolation is enforced"}, DoctorCheck{Name: "bundled_tools", Status: DoctorPass, Detail: "pi, JavaScript, bash, and ripgrep are available in the private image"}
}

func (d Doctor) inspectProbe(ctx context.Context, id, workspace, resources, configuration, temporary string) error {
	output, err := d.sandbox.command.Output(ctx, "docker", "inspect", "--format", "{{json .HostConfig}}", id)
	if err != nil {
		return fmt.Errorf("inspect sandbox isolation")
	}
	var config struct {
		NetworkMode    string
		ReadonlyRootfs bool
		CapDrop        []string
		SecurityOpt    []string
		PidsLimit      int64
	}
	if json.Unmarshal(output, &config) != nil || config.NetworkMode != "none" || !config.ReadonlyRootfs || config.PidsLimit != 256 || !containsFold(config.CapDrop, "ALL") || !containsFold(config.SecurityOpt, "no-new-privileges:true") {
		return fmt.Errorf("required process and network isolation was not applied by Docker Engine")
	}
	output, err = d.sandbox.command.Output(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", id)
	if err != nil {
		return fmt.Errorf("inspect sandbox mounts")
	}
	var mounts []struct {
		Type        string
		Source      string
		Destination string
		RW          bool
		Propagation string
	}
	if json.Unmarshal(output, &mounts) != nil {
		return fmt.Errorf("invalid sandbox mount metadata was returned by Docker Engine")
	}
	expected := map[string]struct {
		source string
		rw     bool
	}{
		workspaceMount:     {workspace, false},
		resourcesMount:     {resources, false},
		configurationMount: {configuration, false},
		temporaryMount:     {temporary, true},
	}
	for _, mount := range mounts {
		want, required := expected[mount.Destination]
		if !required {
			continue
		}
		if mount.Type != "bind" || mount.Source != want.source || mount.RW != want.rw || mount.Propagation != "rprivate" {
			return fmt.Errorf("required sandbox mounts were not applied by Docker Engine")
		}
		delete(expected, mount.Destination)
	}
	if len(expected) != 0 {
		return fmt.Errorf("required sandbox mounts were not applied by Docker Engine")
	}
	return nil
}

func probeEgress() DoctorCheck {
	proxy, err := egress.New(contract.Network{Mode: contract.NetworkNone}, doctorResolver{})
	if err != nil {
		return DoctorCheck{Name: "egress_proxy", Status: DoctorFail, Detail: "cannot construct the egress proxy"}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodConnect, "http://example.invalid", nil)
	request.Host = "example.invalid:443"
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		return DoctorCheck{Name: "egress_proxy", Status: DoctorFail, Detail: "egress proxy did not deny disabled network access"}
	}
	return DoctorCheck{Name: "egress_proxy", Status: DoctorPass, Detail: "egress proxy denies direct tool network access by default"}
}

// doctorResolver is never asked to resolve an address by the deny-by-default
// probe, but keeps the production proxy constructor on its ordinary seam.
type doctorResolver struct{}

func (doctorResolver) LookupNetIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func doctorPass(name, detail string) DoctorCheck {
	return DoctorCheck{Name: name, Status: DoctorPass, Detail: detail}
}

func doctorFailure(name string, err error) DoctorCheck {
	status := DoctorFail
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "requires a linux") || strings.Contains(message, "linux docker engine is required") || strings.Contains(message, "rootless") || strings.Contains(message, "api 1.45") {
		status = DoctorUnsupported
	}
	if strings.Contains(message, "unavailable") || strings.Contains(message, "not found") {
		status = DoctorMissing
	}
	detail := "required " + strings.ReplaceAll(name, "_", " ") + " check failed"
	switch name {
	case "private_image":
		if status == DoctorMissing {
			detail = "private runtime image is missing; install or load the matching AgentRun release bundle image before authentication (agentrun auth login openai-subscription cannot install the image)"
		} else if strings.Contains(message, "platform is") || strings.Contains(message, "want linux/") {
			detail = "private runtime image architecture does not match this host; install or load the matching AgentRun release bundle image"
		} else if strings.Contains(message, "does not match") {
			detail = "private runtime image digest does not match the release; install or load the matching AgentRun release bundle image"
		}
	case "docker":
		switch status {
		case DoctorMissing:
			detail = "Docker Engine is unavailable to the invoking user"
		case DoctorUnsupported:
			detail = "Docker Engine does not support AgentRun v1 isolation requirements"
		}
	}
	return DoctorCheck{Name: name, Status: status, Detail: detail}
}
