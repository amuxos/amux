package bridge

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/modelservice"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const modelServiceEntryPrefix = "@model-service:"

type managementModelService struct {
	Service modelservice.Service
	State   modelservice.State
}

// SetModelServicePrincipalResolver resolves the authenticated Feishu operator.
// A card-supplied OpenID/principal is never accepted.
func (b *Bridge) SetModelServicePrincipalResolver(fn func(string) (string, error)) {
	b.resolveModelServicePrincipal = fn
}
func serviceSupports(s modelservice.Service, runtime string) bool {
	for _, r := range s.Runtimes {
		if r == runtime {
			return true
		}
	}
	return false
}
func modelServiceStatusLabel(state string) any {
	key := "check"
	switch state {
	case "signed_in", "signed_out", "expired", "pending", "unavailable", "setup_required", "installing", "loading":
		key = state
	}
	return managementCopy("management.model.service.status." + key)
}
func (b *Bridge) loadModelServices(ctx context.Context, state managementState, openID, selectedService, action string) managementState {
	if b.resolveModelServicePrincipal == nil {
		return state
	}
	principal, err := b.resolveModelServicePrincipal(openID)
	if err != nil {
		return state
	}
	return b.loadModelServicesForPrincipal(ctx, state, principal, selectedService, action)
}

func (b *Bridge) loadModelServicesForPrincipal(ctx context.Context, state managementState, principal, selectedService, action string) managementState {
	if principal == "" {
		return state
	}
	state.ModelServices = nil
	// Editors may reuse their base snapshot while polling. Do not append catalog
	// entries into that shared snapshot on each refresh.
	state.Runtimes = slices.Clone(state.Runtimes)
	for i := range state.Runtimes {
		state.Runtimes[i].Models = slices.Clone(state.Runtimes[i].Models)
	}
	for _, service := range modelservice.List() {
		serviceAction := "status"
		if selectedService == service.ID {
			serviceAction = action
		}
		result, err := service.Query(ctx, principal, serviceAction)
		if err != nil {
			result = modelservice.State{Status: "unavailable"}
		}
		state.ModelServices = append(state.ModelServices, managementModelService{Service: service, State: result})
		if result.Status != "signed_in" {
			continue
		}
		for i := range state.Runtimes {
			if !serviceSupports(service, state.Runtimes[i].Ref) {
				continue
			}
			for _, entry := range result.Models {
				model, err := agentprofile.ResolveRuntimeModel(state.Runtimes[i], entry.Ref)
				if err != nil {
					continue
				}
				model.DisplayName = entry.Label
				state.Runtimes[i].Models = append(state.Runtimes[i].Models, model)
			}
		}
	}
	return state
}
func modelServiceOptions(state managementState, runtime string) []cardOption {
	var options []cardOption
	for _, service := range modelservice.List() {
		if !serviceSupports(service, runtime) {
			continue
		}
		status := ""
		for _, entry := range state.ModelServices {
			if entry.Service.ID == service.ID {
				status = entry.State.Status
			}
		}
		options = append(options, cardOption{Text: managementPlainText(managementCopy("management.model.service.option", service.Name, modelServiceStatusLabel(status))), Value: modelServiceEntryPrefix + service.ID})
	}
	return options
}
func modelServiceElements(state managementState, runtime string, values map[string]string, action string) []cardElement {
	elements := nativeModelElements(state, runtime, values, action)
	for _, entry := range state.ModelServices {
		if !serviceSupports(entry.Service, runtime) {
			continue
		}
		hint := managementConcat(entry.Service.Name, " · ", modelServiceStatusLabel(entry.State.Status))
		operation, kind := "refresh", "default"
		label := managementCopy("management.model.service.refresh.login")
		switch entry.State.Status {
		case "setup_required":
			label = managementCopy("management.model.service.recheck")
		case "installing":
			label = managementCopy("management.model.service.install.progress")
		case "signed_out", "expired":
			operation, label, kind = "login", managementCopy("management.model.service.login", entry.Service.Name), "primary"
		case "signed_in":
			hint = managementConcat(hint, " · ", managementCopy("management.model.service.model.count", len(entry.State.Models)))
			label = managementCopy("management.model.service.refresh.models")
		case "pending":
			hint = managementConcat(hint, " · ", managementCopy("management.model.service.login.pending"))
		}
		if entry.State.Notice != "" {
			hint = managementConcat(hint, "  ", entry.State.Notice)
		} else if entry.State.Status == "unavailable" {
			hint = managementConcat(hint, "  ", managementCopy("management.model.service.unavailable"))
		}
		elements = append(elements, managementHintElement(hint))
		var buttons []cardElement
		if entry.State.LoginURL != "" {
			buttons = append(buttons, managementButtonElement(managementButton{Text: managementCopy("management.model.service.open.login"), URL: entry.State.LoginURL, Type: "primary"}))
		}
		buttonValues := map[string]string{}
		for k, v := range values {
			buttonValues[k] = v
		}
		buttonValues["model_service"] = entry.Service.ID
		buttonValues["model_service_action"] = operation
		// Feishu requires unique element names across the card. These controls
		// share the editor callback action, but identify distinct service actions.
		namePrefix := action + ".model_service." + entry.Service.ID + "."
		if entry.State.Status == "setup_required" && entry.State.CanInstall {
			installValues := make(map[string]string, len(buttonValues))
			for k, v := range buttonValues {
				installValues[k] = v
			}
			installValues["model_service_action"] = "install"
			buttons = append(buttons, managementButtonElement(managementButton{Action: action, Name: namePrefix + "install", Text: managementCopy("management.model.service.install"), Type: "primary", Values: installValues}))
		}
		buttons = append(buttons, managementButtonElement(managementButton{Action: action, Name: namePrefix + operation, Text: label, Type: kind, Values: buttonValues}))
		elements = append(elements, managementFormButtonRowElement(buttons...))
	}
	return elements
}

// Service entries are actions, not models. Keep callback-carried draft values
// while discarding only the transient service entry from the submitted form.
func modelServiceDraftAction(action *callback.CallBackAction) *callback.CallBackAction {
	if action == nil {
		return nil
	}
	copy := *action
	copy.Value = make(map[string]interface{}, len(action.Value))
	for k, v := range action.Value {
		copy.Value[k] = v
	}
	copy.FormValue = make(map[string]interface{}, len(action.FormValue))
	for k, v := range action.FormValue {
		copy.FormValue[k] = v
	}
	if strings.HasPrefix(copy.Option, modelServiceEntryPrefix) {
		copy.Option, copy.Name = "", ""
		delete(copy.Value, managementDraftFieldKey)
	}
	for _, field := range []string{"default_model_ref", "group_model", "document_comments_model_ref", "group_watch_model_ref"} {
		if v, ok := copy.FormValue[field].(string); ok && strings.HasPrefix(v, modelServiceEntryPrefix) {
			delete(copy.FormValue, field)
		}
		if v, ok := copy.Value[field].(string); ok && strings.HasPrefix(v, modelServiceEntryPrefix) {
			delete(copy.Value, field)
		}
	}
	return &copy
}
func modelServiceSelection(action *callback.CallBackAction) (id, operation string) {
	if action == nil {
		return "", "status"
	}
	id = managementCallbackString(action, "model_service")
	operation = managementCallbackString(action, "model_service_action")
	if operation != "refresh" && operation != "login" && operation != "install" {
		operation = "status"
	}
	// An explicit button action wins over a still-selected service entry in
	// the submitted form. Otherwise installation would turn back into login.
	if id != "" {
		return id, operation
	}
	if strings.HasPrefix(action.Option, modelServiceEntryPrefix) {
		return strings.TrimPrefix(action.Option, modelServiceEntryPrefix), "login"
	}
	for _, field := range []string{"default_model_ref", "group_model", "document_comments_model_ref", "group_watch_model_ref"} {
		if value := managementCallbackString(action, field); strings.HasPrefix(value, modelServiceEntryPrefix) {
			return strings.TrimPrefix(value, modelServiceEntryPrefix), "login"
		}
	}
	return id, operation
}

// SDK/network calls run outside the Feishu callback deadline. The existing card
// is patched, and the unsaved editor draft remains the source of form values.
func (b *Bridge) handleModelServiceEditor(event *callback.CardActionTriggerEvent, action, openID, messageID string, version uint64) bool {
	if b.hub == nil && (len(modelservice.List()) == 0 || b.resolveModelServicePrincipal == nil) {
		return false
	}
	if action != managementActionSettingsEdit && action != managementActionOnboardingEdit && action != managementActionDocumentCommentsEdit && action != managementActionGroupWatchEdit {
		return false
	}
	surface := managementSurfaceFromCallback(event.Event.Action)
	service, operation := modelServiceSelection(event.Event.Action)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		base := b.managementState()
		if action == managementActionGroupWatchEdit {
			base = b.groupWatchManagementState(base, managementCallbackString(event.Event.Action, "chat_id"))
		}
		draftAction := modelServiceDraftAction(event.Event.Action)
		draft := managementDraftFromCallback(draftAction, base)
		documentDraft := managementDocumentCommentsDraftFromCallback(draftAction, base)
		watchDraft := managementGroupWatchDraftFromCallback(draftAction, base)
		base.Mode = draft.Interaction
		runtimeRef := draft.DefaultRuntimeRef
		if action == managementActionDocumentCommentsEdit {
			runtimeRef = documentDraft.RuntimeRef
		}
		if action == managementActionGroupWatchEdit {
			runtimeRef = watchDraft.RuntimeRef
		}
		render := func(current managementState) string {
			switch action {
			case managementActionOnboardingEdit:
				return renderOnboardingDraftCard(current, draft)
			case managementActionDocumentCommentsEdit:
				return renderDocumentCommentsEditCard(current, documentDraft)
			case managementActionGroupWatchEdit:
				return renderGroupWatchEditCard(current, watchDraft)
			default:
				return renderSettingsEditCardForSurface(current, draft, surface)
			}
		}
		publish := func(current managementState) {
			card := renderFocusedManagementCardForSurface(render(current), surface)
			b.writeManagementCardAtVersion(ctx, openID, messageID, card, version)
		}
		load := func(operation string, force bool) managementState {
			return b.refreshEditorModels(ctx, base, openID, messageID, runtimeRef, service, operation, force, publish)
		}
		state := load(operation, forceEditorModelRefresh(event.Event.Action))
		// Only this still-current editor may refresh while installing or logging in.
		for service != "" {
			pending := false
			for _, entry := range state.ModelServices {
				if entry.Service.ID == service && (entry.State.Status == "pending" || entry.State.Status == "installing") {
					pending = true
				}
			}
			if !pending {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			if !b.modelServiceEditorCurrent(messageID, version) {
				return
			}
			state = load("refresh", true)
			if !b.modelServiceEditorCurrent(messageID, version) {
				return
			}
		}
	}()
	return true
}

func (b *Bridge) advanceModelServiceEditor(messageID string) uint64 {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	if b.modelServiceVersions == nil {
		b.modelServiceVersions = map[string]uint64{}
	}
	b.modelServiceVersions[messageID]++
	return b.modelServiceVersions[messageID]
}
func (b *Bridge) modelServiceEditorCurrent(messageID string, version uint64) bool {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	return b.modelServiceVersions[messageID] == version
}
