package agentprofile

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// RuntimeOption is an amux-owned execution choice. It is deliberately
// independent of Agent Profile: a Profile supplies expert content, while a
// RuntimeOption supplies the adapter, model/provider catalog and native launch
// defaults used to execute that content.
type RuntimeOption struct {
	NativeDefaultCapabilitiesKnown bool
	Ref                            string
	AgentType                      string
	DisplayName                    string
	Models                         []RuntimeModelOption
	DefaultReasoningEfforts        []string
	DefaultReasoningEffort         string
}

type RuntimeModelOption struct {
	// NativeCapabilitiesUnknown delegates validation to the native agent when
	// no execution-scoped discovery snapshot is available (e.g. offline launch).
	NativeCapabilitiesUnknown bool
	DisplayName               string
	Ref                       string
	Kind                      ModelKind
	AgentModel                string
	ProviderRef               string
	Provider                  *Provider
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	// BackendVariant selects a runtime backend variant of AgentModel for models
	// whose declared ref is a variant rather than a plain model name. Empty
	// means the runtime picks its default backend for the model.
	BackendVariant string
}

type runtimeCatalogSource struct {
	ref               string
	displayName       string
	models            []string
	modelDisplayNames map[string]string
	capabilities      map[string]RuntimeModelCapability
	variants          []RuntimeModelVariant
}

var standardReasoningEfforts = []string{"none", "low", "medium", "high", "xhigh"}
var extendedReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func RuntimeCatalog() ([]RuntimeOption, error) {
	data, err := fs.ReadFile(builtinProfileFS, packManifestRelPath)
	if err != nil {
		return nil, err
	}
	manifest, err := parsePackManifest(builtinProfilePath, data)
	if err != nil {
		return nil, err
	}
	return buildRuntimeCatalog(runtimeCatalogSources, manifest)
}

// validateRuntimeCatalogSources rejects declarations that cannot be projected
// consistently: an effort default outside the declared enum, a variant pointing
// at a model the runtime does not expose, or an empty member in either.
func validateRuntimeCatalogSources(sources []RuntimeCatalogSource) error {
	for _, source := range sources {
		for ref, capability := range source.Capabilities {
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("runtime %s declares a capability with an empty model ref", source.Ref)
			}
			for _, effort := range capability.SupportedReasoningEfforts {
				if strings.TrimSpace(effort) == "" {
					return fmt.Errorf("runtime %s model %s declares an empty reasoning effort", source.Ref, ref)
				}
			}
			if effort := strings.TrimSpace(capability.DefaultReasoningEffort); effort != "" &&
				len(capability.SupportedReasoningEfforts) > 0 &&
				!stringSliceContains(capability.SupportedReasoningEfforts, effort) {
				return fmt.Errorf("runtime %s model %s default reasoning effort %q is not in its supported efforts", source.Ref, ref, effort)
			}
		}
		for _, variant := range source.Variants {
			if strings.TrimSpace(variant.Ref) == "" || strings.TrimSpace(variant.AgentModel) == "" {
				return fmt.Errorf("runtime %s declares a variant with an empty ref or agent model", source.Ref)
			}
			if _, ok := source.Capabilities[variant.AgentModel]; !ok && !stringSliceContains(source.Models, variant.AgentModel) {
				return fmt.Errorf("runtime %s variant %s refers to unknown model %s", source.Ref, variant.Ref, variant.AgentModel)
			}
			for _, existing := range source.Variants {
				if existing.Ref == variant.Ref && existing.AgentModel == variant.AgentModel && existing.BackendVariant != variant.BackendVariant {
					return fmt.Errorf("runtime %s declares variant %s twice with different backends", source.Ref, variant.Ref)
				}
			}
		}
	}
	return nil
}

// buildRuntimeCatalog composes the runtime options from the registered sources
// and the pack manifest's provider bindings.
func buildRuntimeCatalog(sources []runtimeCatalogSource, manifest packManifest) ([]RuntimeOption, error) {
	out := make([]RuntimeOption, 0, len(sources))
	for _, source := range sources {
		option := RuntimeOption{
			Ref:         source.ref,
			AgentType:   source.ref,
			DisplayName: source.displayName,
		}
		if profileConfigProtocol(source.ref) == "codex" {
			option.DefaultReasoningEfforts = cloneStrings(standardReasoningEfforts)
		} else if source.ref == "claude" {
			option.DefaultReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}
		}
		for _, ref := range source.models {
			model := RuntimeModelOption{
				DisplayName: strings.TrimSpace(source.modelDisplayNames[ref]),
				Ref:         ref, Kind: ModelKindNative, AgentModel: ref,
			}
			if providerDef, ok := manifest.Providers[ref]; ok {
				provider := providerDef.toProvider()
				providerRef, _, err := parseProviderModelRef(ref)
				if err != nil {
					return nil, fmt.Errorf("runtime %s model %s: %w", source.ref, ref, err)
				}
				provider.Ref = providerRef
				if err := validateProvider(source.ref, provider); err != nil {
					return nil, fmt.Errorf("runtime %s model %s: %w", source.ref, ref, err)
				}
				model.Kind = ModelKindProvider
				model.AgentModel = provider.UpstreamModel
				model.ProviderRef = providerRef
				model.Provider = provider
				model.DefaultReasoningEffort = providerReasoningEffort(provider)
			}
			// A declared capability replaces the protocol heuristics entirely.
			// The heuristics below infer effort support from the model name, which
			// only holds for runtimes whose catalog is fixed at build time; a
			// runtime that discovers models from its own CLI declares them instead.
			if capability, ok := source.capabilities[ref]; ok {
				model.SupportedReasoningEfforts = cloneStrings(capability.SupportedReasoningEfforts)
				model.DefaultReasoningEffort = capability.DefaultReasoningEffort
			} else if profileConfigProtocol(source.ref) == "codex" {
				model.SupportedReasoningEfforts = cloneStrings(standardReasoningEfforts)
				if strings.Contains(ref, "glm") || strings.Contains(ref, "deepseek") {
					model.SupportedReasoningEfforts = cloneStrings(extendedReasoningEfforts)
				}
			} else if source.ref == "claude" {
				model.SupportedReasoningEfforts = cloneStrings(option.DefaultReasoningEfforts)
			}
			// Some distribution models expose thinking as an enable/disable
			// capability. Do not present an effort enum the adapter cannot apply.
			if _, declared := source.capabilities[ref]; !declared && runtimeModelUsesBinaryThinking(ref) {
				model.SupportedReasoningEfforts = nil
			}
			option.Models = append(option.Models, model)
		}
		for _, variant := range source.variants {
			variantRef := strings.TrimSpace(variant.Ref)
			agentModel := strings.TrimSpace(variant.AgentModel)
			if variantRef == "" || agentModel == "" {
				continue
			}
			model := RuntimeModelOption{
				DisplayName:    strings.TrimSpace(source.modelDisplayNames[variantRef]),
				Ref:            variantRef,
				Kind:           ModelKindNative,
				AgentModel:     agentModel,
				BackendVariant: strings.TrimSpace(variant.BackendVariant),
			}
			// A variant runs the same runtime model, so it reports that model's
			// reasoning surface and provider binding.
			if base, ok := findRuntimeModelByRef(option.Models, agentModel); ok {
				model.Kind = base.Kind
				model.ProviderRef = base.ProviderRef
				model.Provider = cloneProvider(base.Provider)
				model.SupportedReasoningEfforts = cloneStrings(base.SupportedReasoningEfforts)
				model.DefaultReasoningEffort = base.DefaultReasoningEffort
			}
			option.Models = append(option.Models, model)
		}
		out = append(out, option)
	}
	return out, nil
}

// findRuntimeModelByRef returns the catalog entry with the given ref.
func findRuntimeModelByRef(models []RuntimeModelOption, ref string) (RuntimeModelOption, bool) {
	for _, model := range models {
		if model.Ref == ref {
			return model, true
		}
	}
	return RuntimeModelOption{}, false
}

func ResolveRuntimeOption(ref string) (RuntimeOption, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RuntimeOption{}, fmt.Errorf("runtime is required")
	}
	catalog, err := RuntimeCatalog()
	if err != nil {
		return RuntimeOption{}, err
	}
	for _, option := range catalog {
		if option.Ref == ref {
			return cloneRuntimeOption(option), nil
		}
	}
	return RuntimeOption{}, fmt.Errorf("unknown runtime %q", ref)
}

func ResolveRuntimeModel(runtime RuntimeOption, requested string) (RuntimeModelOption, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return RuntimeModelOption{Kind: ModelKindDefault, NativeCapabilitiesUnknown: runtime.Ref == "codex" && !runtime.NativeDefaultCapabilitiesKnown}, nil
	}
	for _, model := range runtime.Models {
		if model.Ref == requested {
			return cloneRuntimeModelOption(model), nil
		}
	}
	if customization.ResolveServiceModel != nil {
		if model, handled, err := customization.ResolveServiceModel(runtime.Ref, requested); handled || err != nil {
			return model, err
		}
	}
	if allowUnlistedNativeRuntimeModels || runtime.Ref == "codex" {
		for _, source := range runtimeCatalogSources {
			if source.ref == runtime.Ref {
				efforts := cloneStrings(runtime.DefaultReasoningEfforts)
				if runtime.Ref == "codex" {
					efforts = nil
				}
				return RuntimeModelOption{
					Ref: requested, Kind: ModelKindNative, AgentModel: requested,
					NativeCapabilitiesUnknown: runtime.Ref == "codex",
					SupportedReasoningEfforts: efforts,
				}, nil
			}
		}
	}
	return RuntimeModelOption{}, fmt.Errorf("model %q is not supported by runtime %s", requested, runtime.Ref)
}

func ValidateRuntimeReasoningEffort(runtime RuntimeOption, model RuntimeModelOption, effort string) error {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	if model.NativeCapabilitiesUnknown {
		return nil
	}
	allowed := model.SupportedReasoningEfforts
	if strings.TrimSpace(model.Ref) == "" {
		allowed = runtime.DefaultReasoningEfforts
	}
	if !stringSliceContains(allowed, effort) {
		modelRef := strings.TrimSpace(model.Ref)
		if modelRef == "" {
			modelRef = "runtime default"
		}
		return fmt.Errorf("reasoning effort %q is not supported by runtime %s model %s", effort, runtime.Ref, modelRef)
	}
	return nil
}

func RuntimeRefs() ([]string, error) {
	catalog, err := RuntimeCatalog()
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(catalog))
	for _, runtime := range catalog {
		refs = append(refs, runtime.Ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func cloneRuntimeOption(option RuntimeOption) RuntimeOption {
	option.DefaultReasoningEfforts = cloneStrings(option.DefaultReasoningEfforts)
	models := make([]RuntimeModelOption, 0, len(option.Models))
	for _, model := range option.Models {
		models = append(models, cloneRuntimeModelOption(model))
	}
	option.Models = models
	return option
}

func cloneRuntimeModelOption(model RuntimeModelOption) RuntimeModelOption {
	model.Provider = cloneProvider(model.Provider)
	model.SupportedReasoningEfforts = cloneStrings(model.SupportedReasoningEfforts)
	return model
}

func cloneRuntimeModelCapabilities(in map[string]RuntimeModelCapability) map[string]RuntimeModelCapability {
	if in == nil {
		return nil
	}
	out := make(map[string]RuntimeModelCapability, len(in))
	for ref, capability := range in {
		capability.SupportedReasoningEfforts = cloneStrings(capability.SupportedReasoningEfforts)
		out[ref] = capability
	}
	return out
}

func cloneRuntimeModelVariants(in []RuntimeModelVariant) []RuntimeModelVariant {
	if in == nil {
		return nil
	}
	return append([]RuntimeModelVariant(nil), in...)
}

func providerReasoningEffort(provider *Provider) string {
	if provider == nil {
		return ""
	}
	for _, key := range []string{"reasoning_effort", "thinking_effort"} {
		if value := strings.TrimSpace(provider.Thinking[key]); value != "" {
			return value
		}
	}
	return ""
}
