package agentprofile

import (
	"fmt"
	"sort"
	"strings"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// ModelSwitchProjection is the result of switching an existing session to a
// different model: the rebuilt process args, the profile's default env (for
// the caller to merge by key), the resolved selection, and the profile spec
// snapshot carrying the selected model and provider binding.
type ModelSwitchProjection struct {
	ExtraArgs   []string
	Env         []string
	Selection   ResolvedModelSelection
	ProfileSpec *pb.AgentProfileSpec
}

// ProjectModelSwitch rebuilds a session's launch args for a model switch. It
// resolves requested against the profile's model options (exact ref first,
// then the provider upstream name / unqualified ref suffix, with ambiguity
// rejected), strips the previous execution selection (model flags plus every
// process-global config key the provider projections write) from storedArgs,
// and appends the new execution configuration: native selections get a plain
// model flag, provider-backed selections get the same provider projection
// ProjectProfileLaunch produces for `run --model`.
func ProjectModelSwitch(profile Profile, storedArgs []string, requested string) (ModelSwitchProjection, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return ModelSwitchProjection{}, fmt.Errorf("model is required")
	}
	selection, err := resolveModelSwitchSelection(profile, requested)
	if err != nil {
		return ModelSwitchProjection{}, err
	}
	selected := cloneProfile(profile)
	selected.Model = selection.Ref
	selected.ConfiguredModel = selection.Ref
	selected.Provider = cloneProvider(selection.Provider)
	if err := validateProvider(selected.Type, selected.Provider); err != nil {
		return ModelSwitchProjection{}, fmt.Errorf("model %q: %w", selection.Ref, err)
	}
	args := append(ModelArgs(profile.Type, selection.Ref), stripModelSwitchExecutionArgs(profile.Type, storedArgs)...)
	args = appendDistributionDefaultProviderArgs(selected, selection.Ref, args, args)
	args = appendUpstreamProviderArgs(selected, selection.Ref, args)
	spec := ToPBProfileSpec(selected)
	spec.Model = selection.Ref
	return ModelSwitchProjection{
		ExtraArgs:   args,
		Env:         cloneStrings(profile.DefaultEnv),
		Selection:   selection,
		ProfileSpec: spec,
	}, nil
}

// resolveModelSwitchSelection resolves a set-model request. An exact model ref
// always wins. Otherwise the request may be a provider option's upstream model
// name (AgentModel) or unqualified ref suffix (`glm5.2` for
// `custom/example-model`); exactly one such match selects that option, several are
// an ambiguity error. With no match the standard resolution rules apply:
// enforced profiles reject the model as unsupported, others treat it as a
// native model name.
func resolveModelSwitchSelection(profile Profile, requested string) (ResolvedModelSelection, error) {
	selection, err := ResolveModelSelection(profile, requested)
	if err == nil {
		if _, ok := profile.ModelOptions[requested]; ok {
			return selection, nil
		}
	}
	matches := make([]ModelOption, 0, 1)
	for ref, option := range profile.ModelOptions {
		if strings.TrimSpace(option.AgentModel) == requested || modelRefSuffix(ref) == requested {
			matches = append(matches, option)
		}
	}
	switch len(matches) {
	case 0:
	case 1:
		option := matches[0]
		return ResolvedModelSelection{
			Ref:         option.Ref,
			Kind:        option.Kind,
			AgentModel:  option.AgentModel,
			ProviderRef: option.ProviderRef,
			Provider:    cloneProvider(option.Provider),
		}, nil
	default:
		refs := make([]string, 0, len(matches))
		for _, option := range matches {
			refs = append(refs, option.Ref)
		}
		sort.Strings(refs)
		return ResolvedModelSelection{}, fmt.Errorf("model %q is ambiguous for profile %s: matches %s; use the full model ref",
			requested, profile.Ref(), strings.Join(refs, ", "))
	}
	if err != nil {
		return ResolvedModelSelection{}, err
	}
	return selection, nil
}

// modelRefSuffix returns the unqualified model part of a qualified model ref
// (`custom/example-model` -> `example-model`), or "" for unqualified refs (those already
// match exactly).
func modelRefSuffix(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return strings.TrimSpace(ref[i+1:])
	}
	return ""
}

// stripModelSwitchExecutionArgs removes the previous execution selection from
// stored launch args: the model flag (via ReplaceArgsModel) plus every
// process-global `-c`/`--config` key the runtime/provider projections write,
// so a stale provider binding cannot leak into the switched launch.
func stripModelSwitchExecutionArgs(agentType string, args []string) []string {
	stripped := ReplaceArgsModel(agentType, args, "")
	out := make([]string, 0, len(stripped))
	for i := 0; i < len(stripped); i++ {
		arg := strings.TrimSpace(stripped[i])
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(stripped) && modelSwitchConfigOwnsExecution(agentType, stripped[i+1]) {
				i++
				continue
			}
		case strings.HasPrefix(arg, "-c="):
			if modelSwitchConfigOwnsExecution(agentType, strings.TrimPrefix(arg, "-c=")) {
				continue
			}
		case strings.HasPrefix(arg, "--config="):
			if modelSwitchConfigOwnsExecution(agentType, strings.TrimPrefix(arg, "--config=")) {
				continue
			}
		}
		out = append(out, stripped[i])
	}
	return out
}

// modelSwitchConfigOwnsExecution reports whether a `-c`/`--config` value sets
// a key owned by the execution selection: the runtime-level keys already
// covered by runtimeConfigOwnsExecutionSelection plus the keys written by the
// provider launch projections (`model_providers.*`, `models`,
// `model_context_window`, `model_max_output_tokens`, `disable_model_failover`,
// `web_search`).
func modelSwitchConfigOwnsExecution(agentType, value string) bool {
	if runtimeConfigOwnsExecutionSelection(agentType, value) {
		return true
	}
	agentType = strings.TrimSpace(agentType)
	if profileConfigProtocol(agentType) != "codex" {
		return false
	}
	key, _, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "model_providers.") {
		return true
	}
	switch key {
	case "models", "model_context_window", "model_max_output_tokens", "disable_model_failover", "web_search":
		return true
	}
	return false
}
