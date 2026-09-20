package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// piProviderID derives the pi provider id from a model ref: the leading
// segment for qualified refs (`glm-test/glm5.2` -> `glm-test`), the whole ref
// for legacy unqualified refs. Matches agent-pack-contract's `_pi_provider_id`.
func piProviderID(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// materializePiConfig projects a `pi` profile's model/provider configuration
// into the workspace using pi's project-level resources (agent-pack-contract
// >= 0.4 renders the same layout):
//
//   - `.pi/settings.json` pins `defaultModel` (native model) or
//     `defaultProvider` + `defaultModel` (provider-backed, the upstream model
//     registered by the generated extension).
//   - provider-backed profiles additionally get
//     `.pi/extensions/agent-pack-provider.ts` registering the pi provider.
//
// pi has no built-in permission gate, so `[permission]` stays unprojected (it
// remains available via the launch args snapshot). Returns the written file
// names for ProjectionResult.
func materializePiConfig(profile Profile, root string) ([]string, error) {
	if strings.TrimSpace(profile.Type) != "pi" {
		return nil, nil
	}
	model := strings.TrimSpace(profile.Model)
	provider := profile.Provider
	if model == "" && provider == nil {
		return nil, nil
	}
	var files []string

	settings := map[string]string{}
	if provider != nil {
		settings["defaultProvider"] = piProviderID(model)
		settings["defaultModel"] = strings.TrimSpace(provider.UpstreamModel)
	} else {
		settings["defaultModel"] = model
	}
	settingsPath := filepath.Join(root, ".pi", "settings.json")
	if err := writeJSONFile(settingsPath, settings); err != nil {
		return nil, err
	}
	files = append(files, ".pi/settings.json")

	if provider != nil {
		ts, err := piProviderExtensionTS(profile, model)
		if err != nil {
			return nil, err
		}
		extPath := filepath.Join(root, ".pi", "extensions", "agent-pack-provider.ts")
		if err := os.MkdirAll(filepath.Dir(extPath), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(extPath, []byte(ts), 0600); err != nil {
			return nil, err
		}
		files = append(files, ".pi/extensions/agent-pack-provider.ts")
	}
	return files, nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// piModelEntry builds one pi `models:` entry plus the provider-wide connection
// fragment for a provider-backed model, following agent-pack-contract's
// `build_pi_provider_entry` (per-platform compat flags included).
func piModelEntry(profile Profile, ref string) (entry map[string]any, connection map[string]any, err error) {
	provider := profile.Provider
	upstream := strings.TrimSpace(provider.UpstreamModel)
	if upstream == "" {
		upstream = "default_model"
	}
	contextWindow := 128000
	if provider.ContextWindow > 0 {
		contextWindow = provider.ContextWindow
	}
	maxTokens := 16384
	if provider.MaxTokens > 0 {
		maxTokens = provider.MaxTokens
	}
	reasoning := len(provider.Thinking) > 0
	entry = map[string]any{
		"id":            upstream,
		"name":          ref,
		"reasoning":     reasoning,
		"input":         []string{"text"},
		"cost":          map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		"contextWindow": contextWindow,
		"maxTokens":     maxTokens,
	}
	// pi hides any thinking level a model does not map, and silently clamps a
	// requested level it does not recognise down to the highest one it knows.
	// Declaring the map is therefore what makes xhigh/max reachable at all;
	// without it pi answers an xhigh or max request with high.
	if reasoning {
		entry["thinkingLevelMap"] = piThinkingLevelMap()
	}
	config, err := piProviderConfig(profile, ref)
	if err != nil {
		return nil, nil, err
	}
	if len(config.ModelCompat) > 0 {
		entry["compat"] = config.ModelCompat
	}
	connection = map[string]any{"baseUrl": config.BaseURL, "api": config.API}
	return entry, connection, nil
}

func piProviderConfig(profile Profile, ref string) (PiProviderConfig, error) {
	provider := profile.Provider
	if strings.TrimSpace(provider.Platform) != "openai-compat" {
		if customization.PiProviderConfig != nil {
			return customization.PiProviderConfig(profile)
		}
		return PiProviderConfig{}, fmt.Errorf("pi provider %q: unsupported platform %q", ref, provider.Platform)
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		return PiProviderConfig{}, fmt.Errorf("pi provider %q: base_url is required", ref)
	}
	return PiProviderConfig{BaseURL: baseURL, API: "openai-completions"}, nil
}

// piProviderExtensionTS renders the pi project extension that registers a
// provider-backed model's provider. The generated module matches the shape
// agent-pack-contract >= 0.4 emits (`.pi/extensions/agent-pack-provider.ts`);
// secrets only appear as `$ENV` references, never inline.
func piProviderExtensionTS(profile Profile, model string) (string, error) {
	ref := strings.TrimSpace(model)
	if ref == "" {
		return "", fmt.Errorf("pi provider projection requires a model ref")
	}
	entry, connection, err := piModelEntry(profile, ref)
	if err != nil {
		return "", err
	}
	providerID := piProviderID(ref)
	modelsJSON, err := json.MarshalIndent([]map[string]any{entry}, "", "    ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("// Generated by agent-pack-contract. Registers agent-pack providers with pi.\n")
	b.WriteString("// Do not edit manually.\n")
	b.WriteString("import type { ExtensionAPI } from \"@earendil-works/pi-coding-agent\";\n\n")
	b.WriteString("export default function (pi: ExtensionAPI): void {\n")
	fmt.Fprintf(&b, "  pi.registerProvider(%s, {\n", jsonString(providerID))
	fmt.Fprintf(&b, "    \"name\": %s,\n", jsonString(providerID))
	fmt.Fprintf(&b, "    \"apiKey\": %s,\n", jsonString("$"+strings.TrimSpace(profile.Provider.APIKeyEnv)))
	keys := make([]string, 0, len(connection))
	for k := range connection {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := connection[k].(type) {
		case string:
			fmt.Fprintf(&b, "    %s: %s,\n", jsonString(k), jsonString(v))
		default:
			fmt.Fprintf(&b, "    %s: %v,\n", jsonString(k), v)
		}
	}
	b.WriteString("    \"models\": ")
	// contract indents the models array under "models": with 4 extra spaces;
	// keep the same readable shape.
	lines := strings.Split(string(modelsJSON), "\n")
	b.WriteString(lines[0] + "\n")
	for _, line := range lines[1:] {
		b.WriteString("        " + line + "\n")
	}
	b.WriteString("  });\n\n")
	b.WriteString("}\n")
	return b.String(), nil
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// piProviderLaunchArgs returns the extra launch args for a provider-backed pi
// profile: `--model <provider-id>/<upstream-model>`, the pi model ref that the
// generated provider extension registers (matching agent-pack-contract's
// workspace-profile.json launch_args). Non-provider-backed profiles return nil
// so pi uses its login/default model.
func piProviderLaunchArgs(profile Profile, model string) []string {
	if strings.TrimSpace(profile.Type) != "pi" || profile.Provider == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	upstream := strings.TrimSpace(profile.Provider.UpstreamModel)
	if model == "" || upstream == "" {
		return nil
	}
	return []string{"--model", piProviderID(model) + "/" + upstream}
}

// piThinkingLevelMap declares every pi thinking level as passing straight
// through to the provider. amux only offers levels a model actually supports
// (the runtime catalog filters them), so the map is the identity for all
// seven; its purpose is to opt the extended levels in, since pi hides any
// level missing from the map.
func piThinkingLevelMap() map[string]string {
	levels := map[string]string{}
	for _, level := range piThinkingLevels {
		levels[level] = level
	}
	// pi spells the disabled level "off" and sends "none" upstream, which is
	// what every amux runtime calls it.
	levels["off"] = "none"
	return levels
}

// piThinkingLevels is pi's level vocabulary, in ascending order.
var piThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
