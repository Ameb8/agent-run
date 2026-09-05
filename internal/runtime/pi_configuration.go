package runtime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ameb8/agent-run/internal/agent"
	"github.com/Ameb8/agent-run/internal/contract"
)

const (
	piAgentDirectory      = temporaryMount + "/pi-home"
	piSessionDirectory    = temporaryMount + "/pi-sessions"
	piSettingsFile        = "settings.json"
	piCodingAgentDir      = "PI_CODING_AGENT_DIR"
	piCodingAgentSessions = "PI_CODING_AGENT_SESSION_DIR"
	piToolAdapterFile     = "agentrun-tools.ts"
	piExtensionLoaderFile = "agentrun-extensions.ts"
	piProviderAdapterFile = "agentrun-provider.ts"
)

// stableToolAdapter adapts the one Pi built-in whose upstream name is not part
// of AgentRun's v1 contract. Keeping this extension generated and run-local
// prevents a workspace or user Pi configuration from adding tools or changing
// their names. The implementation delegates to Pi's pinned built-in, so its
// non-interactive shell behavior and error results remain intact.
const stableToolAdapter = `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { createBashTool } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  const bash = createBashTool("/workspace");
  pi.registerTool({ ...bash, name: "shell", label: "shell" });
}
`

// PiConfiguration is the complete, run-local resource contract passed to the
// pinned Pi CLI.  It deliberately contains no model credential, caller
// environment value, package path, or discovery source.
type PiConfiguration struct {
	AgentDirectory  string
	SessionDir      string
	Settings        string
	Skills          []string
	PromptTemplate  string
	OutputSchema    string
	Extensions      []string
	ExtensionLoader string
	ActiveTools     []string
	ToolAdapter     string
	ProviderAdapter string
	Provider        string
	Model           string
}

// GenerateProviderAdapter writes AgentRun's infrastructure extension.  Its
// endpoint is the run-private Unix socket mounted at /agentrun/tmp, not the
// configured provider endpoint; consequently neither credentials nor a host
// route can enter Pi.  The protocol is intentionally JSONL and is consumed by
// execution.ProviderBridge.
func GenerateProviderAdapter(configuration string, selected contract.Model) (string, error) {
	configuration, err := privateDirectory(configuration, "generated configuration")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(configuration, "pi"), 0o700); err != nil {
		return "", configurationError("create provider adapter directory: %v", err)
	}
	if strings.TrimSpace(selected.Model) == "" {
		return "", configurationError("provider model is required")
	}
	encodedModel, err := json.Marshal(selected.Model)
	if err != nil {
		return "", configurationError("encode provider model: %v", err)
	}
	streamImport, streamName, api, placeholder := "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/providers/openai-responses.js", "streamSimpleOpenAIResponses", "openai-responses", "agentrun-private-bridge"
	if selected.Provider == contract.ProviderOpenAISubscription {
		streamImport, streamName, api = "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/providers/openai-codex-responses.js", "streamSimpleOpenAICodexResponses", "openai-codex-responses"
		claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"agentrun"}}`))
		placeholder = "e30." + claims + ".agentrun"
	} else if selected.Provider != contract.ProviderOpenAICompatible {
		return "", configurationError("provider is unsupported")
	}
	encodedImport, _ := json.Marshal(streamImport)
	encodedStream, _ := json.Marshal(streamName)
	encodedAPI, _ := json.Marshal(api)
	encodedPlaceholder, _ := json.Marshal(placeholder)
	source := `// AgentRun-owned Pi 0.74 provider adapter. No credential or provider origin enters this file.
import http from "node:http";
import net from "node:net";
import { setGlobalDispatcher, EnvHttpProxyAgent } from "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/undici/index.js";
import * as providerStreams from ` + string(encodedImport) + `;
const providerSocket = "/agentrun/tmp/provider.sock";
const egressSocket = "/agentrun/tmp/egress.sock";
const providerPort = 43123;
const egressPort = 43124;
const maxFrameBytes = 32 * 1024 * 1024;
const model = ` + string(encodedModel) + `;
const api = ` + string(encodedAPI) + `;
const placeholder = ` + string(encodedPlaceholder) + `;
const streamSimple = (providerStreams as any)[` + string(encodedStream) + `];

function exchange(request: any): Promise<any> {
  return new Promise((resolve, reject) => {
    const peer = net.createConnection(providerSocket); let input = ""; let settled = false;
    const fail = () => { if (!settled) { settled = true; peer.destroy(); reject(new Error("AgentRun provider bridge failed")); } };
    peer.once("error", fail);
    peer.on("data", (chunk) => {
      input += chunk.toString("utf8");
      if (Buffer.byteLength(input) > maxFrameBytes) return fail();
      const lf = input.indexOf("\n");
      if (lf < 0) return;
      if (input.indexOf("\n", lf + 1) >= 0 || input[lf - 1] === "\r") return fail();
      try { const reply = JSON.parse(input.slice(0, lf)); settled = true; peer.end(); resolve(reply); } catch { fail(); }
    });
    peer.once("end", () => { if (!settled) fail(); });
    peer.on("connect", () => peer.write(JSON.stringify(request) + "\n"));
  });
}

function readBody(request: any): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []; let size = 0;
    request.on("data", (chunk: Buffer) => { size += chunk.length; if (size > maxFrameBytes) { request.destroy(); reject(new Error("provider request exceeds limit")); } else chunks.push(chunk); });
    request.once("end", () => resolve(Buffer.concat(chunks)));
    request.once("error", reject);
  });
}

const providerServer = http.createServer(async (request, response) => {
  try {
    const body = await readBody(request);
    const headers: Record<string, string[]> = {};
    for (const [name, value] of Object.entries(request.headers)) if (value !== undefined) headers[name] = Array.isArray(value) ? value : [value];
    const target = (request.url || "/").replace(/^\//, "");
    const reply = await exchange({ id: crypto.randomUUID(), method: request.method || "POST", target, headers, body: body.toString("base64") });
    if (!reply.accepted) throw new Error("provider request rejected");
    for (const [name, values] of Object.entries(reply.headers || {})) response.setHeader(name, values as string[]);
    response.writeHead(reply.status); response.end(Buffer.from(reply.body, "base64"));
  } catch { if (!response.headersSent) response.writeHead(502); response.end(); }
});

const egressServer = net.createServer((client) => {
  const upstream = net.createConnection(egressSocket);
  client.pipe(upstream); upstream.pipe(client);
  const close = () => { client.destroy(); upstream.destroy(); };
  client.once("error", close); upstream.once("error", close);
});

function listen(server: any, port: number): Promise<void> {
  return new Promise((resolve, reject) => { server.once("error", reject); server.listen(port, "127.0.0.1", resolve); });
}

export default async function(pi: any) {
  await listen(providerServer, providerPort);
  await listen(egressServer, egressPort);
  process.env.HTTP_PROXY = "http://127.0.0.1:" + egressPort;
  process.env.HTTPS_PROXY = process.env.HTTP_PROXY;
  process.env.NO_PROXY = "127.0.0.1,localhost";
  setGlobalDispatcher(new EnvHttpProxyAgent());
  pi.on("session_shutdown", () => { providerServer.close(); egressServer.close(); });
  pi.registerProvider("agentrun", {
    name: "AgentRun", baseUrl: "http://127.0.0.1:" + providerPort, apiKey: placeholder, api,
    models: [{ id: model, name: model, api, reasoning: true, input: ["text", "image"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 200000, maxTokens: 32000 }],
    streamSimple: (selectedModel: any, context: any, options: any) => streamSimple(selectedModel, context, { ...options, apiKey: placeholder, transport: "sse", maxRetries: 0 }),
  });
}
`
	path := filepath.Join(configuration, "pi", piProviderAdapterFile)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		return "", configurationError("write provider adapter: %v", err)
	}
	return filepath.ToSlash(filepath.Join(configurationMount, "pi", piProviderAdapterFile)), nil
}

// GeneratePiConfiguration writes the otherwise-empty Pi settings file and
// translates selected snapshot paths into their fixed container paths. Pi's
// command line disables every automatic resource discovery mechanism and
// adds selected skills explicitly, preserving definition order.
//
// resources must be the parent mounted at /agentrun/resources; snapshot must
// be a child of it.  The latter condition prevents a generated invocation
// from exposing a source-package path or arbitrary host file to the runtime.
func GeneratePiConfiguration(configuration, temporary, resources string, snapshot *agent.Snapshot) (PiConfiguration, error) {
	if snapshot == nil || snapshot.Root == "" {
		return PiConfiguration{}, configurationError("Pi configuration requires an agent snapshot")
	}
	configuration, err := privateDirectory(configuration, "generated configuration")
	if err != nil {
		return PiConfiguration{}, err
	}
	if _, err := privateDirectory(temporary, "private temporary storage"); err != nil {
		return PiConfiguration{}, err
	}
	resources, err = privateDirectory(resources, "selected resource snapshot parent")
	if err != nil {
		return PiConfiguration{}, err
	}
	snapshotRoot, err := filepath.EvalSymlinks(snapshot.Root)
	if err != nil || !withinDirectory(resources, snapshotRoot) || snapshotRoot == resources {
		return PiConfiguration{}, configurationError("agent snapshot is not contained by selected resources")
	}

	agentDirectory := filepath.Join(configuration, "pi")
	if err := os.Mkdir(agentDirectory, 0o700); err != nil && !os.IsExist(err) {
		return PiConfiguration{}, configurationError("create Pi configuration: %v", err)
	}
	settings := filepath.Join(agentDirectory, piSettingsFile)
	// Keep all persisted discovery lists empty. Explicit --skill arguments are
	// used below because --no-skills excludes settings-based skill sources.
	contents, err := json.Marshal(struct {
		Extensions []string `json:"extensions"`
		Packages   []string `json:"packages"`
		Prompts    []string `json:"prompts"`
		Skills     []string `json:"skills"`
		Themes     []string `json:"themes"`
	}{Extensions: []string{}, Packages: []string{}, Prompts: []string{}, Skills: []string{}, Themes: []string{}})
	if err != nil {
		return PiConfiguration{}, configurationError("encode Pi configuration: %v", err)
	}
	if err := os.WriteFile(settings, contents, 0o600); err != nil {
		return PiConfiguration{}, configurationError("write Pi configuration: %v", err)
	}

	toContainer := func(path string) (string, error) {
		path, err = filepath.EvalSymlinks(path)
		if err != nil || !withinDirectory(snapshotRoot, path) {
			return "", configurationError("selected snapshot resource is unavailable")
		}
		rel, err := filepath.Rel(resources, path)
		if err != nil {
			return "", configurationError("map selected snapshot resource: %v", err)
		}
		return filepath.ToSlash(filepath.Join(resourcesMount, rel)), nil
	}

	result := PiConfiguration{AgentDirectory: piAgentDirectory, SessionDir: piSessionDirectory, Settings: filepath.ToSlash(filepath.Join(configurationMount, "pi", piSettingsFile))}
	result.PromptTemplate, err = toContainer(snapshot.Definition.PromptTemplate)
	if err != nil {
		return PiConfiguration{}, err
	}
	if snapshot.Definition.OutputSchema != "" {
		result.OutputSchema, err = toContainer(snapshot.Definition.OutputSchema)
		if err != nil {
			return PiConfiguration{}, err
		}
	}
	for _, skill := range snapshot.Definition.Skills {
		containerPath, pathErr := toContainer(skill)
		if pathErr != nil {
			return PiConfiguration{}, pathErr
		}
		result.Skills = append(result.Skills, containerPath)
	}
	if err := agent.ValidateExtensions(snapshot); err != nil {
		return PiConfiguration{}, err
	}
	for _, extension := range snapshot.Definition.Extensions {
		// Definition.Extensions identifies the validated extension directory;
		// only its conventional entry point is executable. Passing the directory
		// would let Pi choose a manifest or other loader behavior instead.
		containerPath, pathErr := toContainer(filepath.Join(extension, "index.ts"))
		if pathErr != nil {
			return PiConfiguration{}, pathErr
		}
		result.Extensions = append(result.Extensions, containerPath)
	}
	result.ActiveTools = append(result.ActiveTools, snapshot.Definition.Agent.Tools.Allow...)
	if containsTool(result.ActiveTools, "shell") {
		adapter := filepath.Join(agentDirectory, piToolAdapterFile)
		if err := os.WriteFile(adapter, []byte(stableToolAdapter), 0o600); err != nil {
			return PiConfiguration{}, configurationError("write stable tool adapter: %v", err)
		}
		result.ToolAdapter = filepath.ToSlash(filepath.Join(configurationMount, "pi", piToolAdapterFile))
	}
	if len(result.Extensions) != 0 {
		loader := filepath.Join(agentDirectory, piExtensionLoaderFile)
		contents, loaderErr := extensionLoader(result.Extensions, result.ActiveTools)
		if loaderErr != nil {
			return PiConfiguration{}, configurationError("generate extension loader: %v", loaderErr)
		}
		if err := os.WriteFile(loader, contents, 0o600); err != nil {
			return PiConfiguration{}, configurationError("write extension loader: %v", err)
		}
		result.ExtensionLoader = filepath.ToSlash(filepath.Join(configurationMount, "pi", piExtensionLoaderFile))
	}
	return result, nil
}

// extensionLoader is the sole Pi extension that AgentRun passes for declared
// package extensions. It imports their immutable index.ts files in definition
// order, retains a run-local registration set, and rejects conflicts before
// Pi can offer a tool to the model. The generated file and its module state
// live under the private per-run configuration mount, so lifecycle state is
// never shared between runs.
func extensionLoader(extensions, allowed []string) ([]byte, error) {
	encode := func(value any) (string, error) {
		bytes, err := json.Marshal(value)
		return string(bytes), err
	}
	var source strings.Builder
	for i, extension := range extensions {
		path, err := encode(extension)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(&source, "import extension%d from %s;\n", i, path)
	}
	allowedJSON, err := encode(allowed)
	if err != nil {
		return nil, err
	}
	builtInsJSON, err := encode([]string{"read", "grep", "write", "edit", "shell"})
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(&source, `
const allowed = new Set<string>(%s);
const builtIns = new Set<string>(%s);

export default function (pi: any) {
  const registered = new Set<string>();
  const guarded = new Proxy(pi, {
    get(target, property, receiver) {
      if (property !== "registerTool") return Reflect.get(target, property, receiver);
      return (tool: any, ...rest: any[]) => {
        const name = tool?.name;
        if (typeof name !== "string" || name.length === 0) throw new Error("extension registered an invalid tool name");
        if (builtIns.has(name)) throw new Error("extension cannot override built-in tool " + name);
        if (registered.has(name)) throw new Error("duplicate extension tool " + name);
        registered.add(name);
        return target.registerTool.call(target, tool, ...rest);
      };
    },
  });
`, allowedJSON, builtInsJSON)
	for i := range extensions {
		_, _ = fmt.Fprintf(&source, "  extension%d(guarded);\n", i)
	}
	source.WriteString(`  for (const name of allowed) {
    if (!builtIns.has(name) && !registered.has(name)) throw new Error("allowed extension tool was not registered: " + name);
  }
}
`)
	return []byte(source.String()), nil
}

// Environment is the fixed Pi environment in addition to the separately
// allowlisted caller variables. It replaces, rather than inherits, Pi's
// normal per-user configuration and session locations.
func (c PiConfiguration) Environment() []string {
	return []string{piCodingAgentDir + "=" + c.AgentDirectory, piCodingAgentSessions + "=" + c.SessionDir}
}

// Command returns only documented options from the pinned 0.74 Pi contract.
// The explicit paths remain effective with --no-skills; all global and project
// resource discovery (including AGENTS.md) is disabled.
func (c PiConfiguration) Command() []string {
	// Start with no tools, then use Pi's explicit allowlist. This means an empty
	// list stays empty and Pi additions such as find, ls, or bash never become
	// AgentRun capabilities by default.
	command := []string{"pi", "--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files"}
	if c.ProviderAdapter != "" {
		// Must precede every package extension: provider registration is runtime
		// infrastructure and has to fail before any model operation.
		command = append(command, "--extension", c.ProviderAdapter)
	}
	if c.Provider != "" {
		command = append(command, "--provider", c.Provider)
	}
	if c.Model != "" {
		command = append(command, "--model", c.Model)
	}
	// Pi receives only the generated adapter and the immutable, declared
	// extension entry points. --no-extensions still disables all discovery
	// sources; explicit --extension is the per-run loading mechanism.
	if c.ToolAdapter != "" {
		command = append(command, "--extension", c.ToolAdapter)
	}
	if c.ExtensionLoader != "" {
		command = append(command, "--extension", c.ExtensionLoader)
	}
	if len(c.ActiveTools) != 0 {
		command = append(command, "--tools", strings.Join(c.ActiveTools, ","))
	}
	for _, skill := range c.Skills {
		command = append(command, "--skill", skill)
	}
	return command
}

func containsTool(tools []string, name string) bool {
	for _, tool := range tools {
		if tool == name {
			return true
		}
	}
	return false
}

func privateDirectory(path, label string) (string, error) {
	if path == "" {
		return "", configurationError("%s is required", label)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", configurationError("%s: %v", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		if err != nil {
			return "", configurationError("%s: %v", label, err)
		}
		return "", configurationError("%s is not a directory", label)
	}
	return canonical, nil
}

func withinDirectory(root, path string) bool {
	return path != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}
