package bridge

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
)

func validateDocumentCommentExecution(agentRef, runtimeRef, modelRef, reasoningEffort string) error {
	return validateCapabilityExecutionSelection("document comment", agentRef, runtimeRef, modelRef, reasoningEffort)
}

func validateGroupWatchExecution(agentRef, runtimeRef, modelRef, reasoningEffort string) error {
	return validateCapabilityExecutionSelection("group watch", agentRef, runtimeRef, modelRef, reasoningEffort)
}

func validateCapabilityExecutionSelection(capability, agentRef, runtimeRef, modelRef, reasoningEffort string) error {
	agentRef = strings.TrimSpace(agentRef)
	runtimeRef = strings.TrimSpace(runtimeRef)
	modelRef = strings.TrimSpace(modelRef)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if agentRef == "" {
		if runtimeRef != "" || modelRef != "" || reasoningEffort != "" {
			return fmt.Errorf("%s execution override requires an Agent Profile override", capability)
		}
		return nil
	}
	if runtimeRef == "" {
		if reasoningEffort != "" {
			return fmt.Errorf("%s reasoning effort override requires a runtime override", capability)
		}
		return validateCapabilityExecution(capability, agentRef, modelRef)
	}
	if _, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), agentRef); err != nil {
		return fmt.Errorf("resolve %s Agent Profile %q: %w", capability, agentRef, err)
	}
	runtime, err := agentprofile.ResolveRuntimeOption(runtimeRef)
	if err != nil {
		return fmt.Errorf("resolve %s runtime %q: %w", capability, runtimeRef, err)
	}
	model, err := agentprofile.ResolveRuntimeModel(runtime, modelRef)
	if err != nil {
		return fmt.Errorf("resolve %s model %q: %w", capability, modelRef, err)
	}
	if err := agentprofile.ValidateRuntimeReasoningEffort(runtime, model, reasoningEffort); err != nil {
		return fmt.Errorf("resolve %s reasoning effort %q: %w", capability, reasoningEffort, err)
	}
	return nil
}

func validateCapabilityExecution(capability, agentRef, modelRef string) error {
	agentRef = strings.TrimSpace(agentRef)
	modelRef = strings.TrimSpace(modelRef)
	if agentRef == "" {
		if modelRef != "" {
			return fmt.Errorf("%s model override requires an Agent Profile override", capability)
		}
		return nil
	}
	profile, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), agentRef)
	if err != nil {
		return fmt.Errorf("resolve %s Agent Profile %q: %w", capability, agentRef, err)
	}
	if modelRef == "" {
		return nil
	}
	if _, err := agentprofile.ResolveModelSelection(profile, modelRef); err != nil {
		return fmt.Errorf("%s model %q is not supported by Agent Profile %s: %w", capability, modelRef, profile.Ref(), err)
	}
	return nil
}
