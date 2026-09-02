package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDoctorProbesRuntimeBoundaryWithoutStartingAModel(t *testing.T) {
	t.Parallel()
	runner := &doctorCommand{}
	doctor := Doctor{sandbox: DockerSandbox{
		Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{digests: []string{testDigest}}, Version: "test"},
		command:  runner,
		goos:     "linux",
	}, egressProbe: probeEgress}
	report := doctor.Run(context.Background())
	if !report.Passing() {
		t.Fatalf("doctor report = %#v", report)
	}
	if report.Runtime.AgentRunVersion != "test" || report.Runtime.ImageDigest != testDigest {
		t.Fatalf("runtime = %#v", report.Runtime)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "model") || strings.Contains(strings.Join(call, " "), "curl") {
			t.Fatalf("doctor made a model or external request: %q", call)
		}
	}
}

func TestDoctorFailureHidesDockerOutputAndClassifiesUnsupported(t *testing.T) {
	t.Parallel()
	check := doctorFailure("docker", fmt.Errorf("rootless Docker contained credential-canary"))
	if check.Status != DoctorUnsupported || strings.Contains(check.Detail, "credential-canary") {
		t.Fatalf("check = %#v", check)
	}
}

func TestDoctorFailureProvidesActionableImageAndDockerDetails(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, check, cause, want string
		status                   DoctorStatus
	}{
		{"missing image", "private_image", "private runtime image is unavailable", "install the release-owned image", DoctorMissing},
		{"wrong digest", "private_image", "private runtime image does not match release digest", "digest does not match", DoctorFail},
		{"missing Docker", "docker", "Docker Engine is unavailable", "unavailable to the invoking user", DoctorMissing},
		{"unsupported Docker", "docker", "Docker Engine API 1.45 or newer is required", "does not support", DoctorUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			check := doctorFailure(test.check, fmt.Errorf("%s credential-canary", test.cause))
			if check.Status != test.status || !strings.Contains(check.Detail, test.want) || strings.Contains(check.Detail, "credential-canary") {
				t.Fatalf("check = %#v", check)
			}
		})
	}
}

func TestDoctorReportsUnsupportedHostWithoutDockerProbe(t *testing.T) {
	t.Parallel()
	doctor := Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Version: "test"}, goos: "darwin"}}
	report := doctor.Run(context.Background())
	if report.Passing() || report.Checks[0].Status != DoctorUnsupported || report.Checks[1].Status != DoctorUnsupported {
		t.Fatalf("doctor report = %#v", report)
	}
}

func TestDoctorReportsEachProbeFailureWithoutLeakingItsCause(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		doctor Doctor
		check  string
		status DoctorStatus
		noCall string
	}{
		{
			name: "missing image", check: "private_image", status: DoctorMissing, noCall: "create",
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{err: fmt.Errorf("credential-canary image not found")}, Version: "test"}, command: &doctorCommand{}, goos: "linux"}, egressProbe: probeEgress},
		},
		{
			name: "unsupported Docker API", check: "docker", status: DoctorUnsupported,
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{digests: []string{testDigest}}, Version: "test"}, command: &doctorCommand{apiVersion: "1.44"}, goos: "linux"}, egressProbe: probeEgress},
		},
		{
			name: "missing Docker access", check: "docker", status: DoctorMissing,
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{digests: []string{testDigest}}, Version: "test"}, command: &doctorCommand{engineErr: true}, goos: "linux"}, egressProbe: probeEgress},
		},
		{
			name: "sandbox isolation", check: "sandbox", status: DoctorFail,
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{digests: []string{testDigest}}, Version: "test"}, command: &doctorCommand{badIsolation: true}, goos: "linux"}, egressProbe: probeEgress},
		},
		{
			name: "bundled tools", check: "bundled_tools", status: DoctorFail,
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Inspector: fakeInspector{digests: []string{testDigest}}, Version: "test"}, command: &doctorCommand{exitStatus: 1}, goos: "linux"}, egressProbe: probeEgress},
		},
		{
			name: "egress proxy", check: "egress_proxy", status: DoctorFail,
			doctor: Doctor{sandbox: DockerSandbox{Verifier: Verifier{Manifest: testManifest(), Version: "test"}, goos: "darwin"}, egressProbe: func() DoctorCheck {
				return DoctorCheck{Name: "egress_proxy", Status: DoctorFail, Detail: "proxy safety probe failed"}
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := test.doctor.Run(context.Background())
			check := findDoctorCheck(t, report, test.check)
			if check.Status != test.status || strings.Contains(check.Detail, "credential-canary") {
				t.Fatalf("check = %#v", check)
			}
			if runner, ok := test.doctor.sandbox.command.(*doctorCommand); ok && test.noCall != "" {
				for _, call := range runner.calls {
					if len(call) > 0 && call[0] == test.noCall {
						t.Fatalf("unexpected docker %s for %s", test.noCall, test.name)
					}
				}
			}
		})
	}
}

func findDoctorCheck(t *testing.T, report DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("report has no %q check: %#v", name, report)
	return DoctorCheck{}
}

type doctorCommand struct {
	calls        [][]string
	apiVersion   string
	badIsolation bool
	exitStatus   int
	engineErr    bool
	mounts       map[string]string
}

func (d *doctorCommand) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	d.calls = append(d.calls, args)
	if len(args) == 0 {
		return nil, fmt.Errorf("missing command")
	}
	switch args[0] {
	case "version":
		if d.engineErr {
			return nil, fmt.Errorf("Docker access failed with credential-canary")
		}
		if strings.Contains(strings.Join(args, " "), "Server.Os") {
			return []byte("linux\n"), nil
		}
		if d.apiVersion != "" {
			return []byte(d.apiVersion + "\n"), nil
		}
		return []byte("1.45\n"), nil
	case "info":
		return []byte("[]"), nil
	case "create":
		d.mounts = make(map[string]string)
		for _, arg := range args {
			if !strings.HasPrefix(arg, "type=bind,") {
				continue
			}
			var source, destination string
			for _, option := range strings.Split(arg, ",") {
				if strings.HasPrefix(option, "src=") {
					source = strings.TrimPrefix(option, "src=")
				}
				if strings.HasPrefix(option, "dst=") {
					destination = strings.TrimPrefix(option, "dst=")
				}
			}
			d.mounts[destination] = source
		}
		return []byte("doctor-container\n"), nil
	case "inspect":
		if strings.Contains(strings.Join(args, " "), "HostConfig") {
			if d.badIsolation {
				return []byte(`{"NetworkMode":"bridge"}`), nil
			}
			return []byte(`{"NetworkMode":"none","ReadonlyRootfs":true,"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges:true"],"PidsLimit":256}`), nil
		}
		return []byte(fmt.Sprintf(`[{"Type":"bind","Source":%q,"Destination":"/workspace","RW":false,"Propagation":"rprivate"},{"Type":"bind","Source":%q,"Destination":"/agentrun/resources","RW":false,"Propagation":"rprivate"},{"Type":"bind","Source":%q,"Destination":"/agentrun/config","RW":false,"Propagation":"rprivate"},{"Type":"bind","Source":%q,"Destination":"/agentrun/tmp","RW":true,"Propagation":"rprivate"}]`, d.mounts[workspaceMount], d.mounts[resourcesMount], d.mounts[configurationMount], d.mounts[temporaryMount])), nil
	case "start":
		return nil, nil
	case "wait":
		return []byte(fmt.Sprintf("%d\n", d.exitStatus)), nil
	case "rm":
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected docker command %q", args)
	}
}
