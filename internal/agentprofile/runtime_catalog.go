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
	Ref                     string
	AgentType               string
	DisplayName             string
	Models                  []RuntimeModelOption
	DefaultReasoningEfforts []string
	DefaultReasoningEffort  string
}

type RuntimeModelOption struct {
	Ref                       string
	Kind                      ModelKind
	AgentModel                string
	ProviderRef               string
	Provider                  *Provider
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
}

type runtimeCatalogSource struct {
	ref         string
	displayName string
	models      []string
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

	out := make([]RuntimeOption, 0, len(runtimeCatalogSources))
	for _, source := range runtimeCatalogSources {
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
			model := RuntimeModelOption{Ref: ref, Kind: ModelKindNative, AgentModel: ref}
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
			if profileConfigProtocol(source.ref) == "codex" {
				model.SupportedReasoningEfforts = cloneStrings(standardReasoningEfforts)
				if strings.Contains(ref, "glm") || strings.Contains(ref, "deepseek") {
					model.SupportedReasoningEfforts = cloneStrings(extendedReasoningEfforts)
				}
			} else if source.ref == "claude" {
				model.SupportedReasoningEfforts = cloneStrings(option.DefaultReasoningEfforts)
			}
			// Some distribution models expose thinking as an enable/disable
			// capability. Do not present an effort enum the adapter cannot apply.
			if runtimeModelUsesBinaryThinking(ref) {
				model.SupportedReasoningEfforts = nil
			}
			option.Models = append(option.Models, model)
		}
		out = append(out, option)
	}
	return out, nil
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
		return RuntimeModelOption{Kind: ModelKindDefault}, nil
	}
	for _, model := range runtime.Models {
		if model.Ref == requested {
			return cloneRuntimeModelOption(model), nil
		}
	}
	if allowUnlistedNativeRuntimeModels {
		for _, source := range runtimeCatalogSources {
			if source.ref == runtime.Ref {
				return RuntimeModelOption{
					Ref: requested, Kind: ModelKindNative, AgentModel: requested,
					SupportedReasoningEfforts: cloneStrings(runtime.DefaultReasoningEfforts),
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
