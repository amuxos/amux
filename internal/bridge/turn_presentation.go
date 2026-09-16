package bridge

import (
	"encoding/json"
	"strings"

	"github.com/amuxos/amux/internal/agentui"
)

type turnActivityPresentationMode uint8

const (
	turnActivityPresentationPending turnActivityPresentationMode = iota
	turnActivityPresentationNormal
	turnActivityPresentationExternalBot
)

// turnActivityPresentationGate classifies the turn from structured tool
// identity and input. External Bot discovery is deliberately the first visible
// action of that workflow, so its @ task post replaces the normal activity card.
type turnActivityPresentationGate struct {
	mode                turnActivityPresentationMode
	classifyExternalBot bool
	// initialActivityVisible records that the turn-start queue hint card was
	// already force-sent for this turn. Once the card exists, plain text
	// progress and terminal updates must keep patching it instead of leaving
	// the title frozen on the queue hint; classification stays pending so a
	// later external-bot tool signal still wins.
	initialActivityVisible bool
}

func newTurnActivityPresentationGate(classifyExternalBot bool) *turnActivityPresentationGate {
	mode := turnActivityPresentationNormal
	if classifyExternalBot {
		mode = turnActivityPresentationPending
	}
	return &turnActivityPresentationGate{mode: mode, classifyExternalBot: classifyExternalBot}
}

func (g *turnActivityPresentationGate) ShouldPublishActivity(view *agentui.ActivityView, hasSubtasks bool) bool {
	if g == nil {
		return true
	}
	switch g.mode {
	case turnActivityPresentationNormal:
		return true
	case turnActivityPresentationExternalBot:
		return false
	}
	if hasSubtasks {
		g.mode = turnActivityPresentationNormal
		return true
	}
	if view == nil {
		return false
	}
	for _, tool := range view.Tools {
		switch {
		case isDelegationCreateTool(tool):
			if externalBotCollaboratorFromTool(tool) {
				g.mode = turnActivityPresentationExternalBot
				return false
			}
			g.mode = turnActivityPresentationNormal
			return true
		case externalBotListTool(tool):
			g.mode = turnActivityPresentationExternalBot
			return false
		}
	}
	if len(view.Tools) > 0 {
		if onlyProfileDiscoveryTools(view.Tools) {
			return false
		}
		g.mode = turnActivityPresentationNormal
		return true
	}
	// Coordination profiles can emit reasoning before their first MCP tool. That
	// activity alone cannot classify this turn, so keep it internal until a real
	// tool/subtask appears. Pure question-answer turns still create their normal
	// result card from assistant output; other streams start in normal mode.
	// Once the turn-start queue hint card was force-sent, it is already visible
	// and must keep receiving plain text/terminal updates; the mode stays
	// pending so a later external-bot tool signal still classifies the turn.
	return g.initialActivityVisible
}

// ObserveCompletedAssistantMessage classifies a complete intermediate
// assistant message as ordinary work. External Bot coordination is required to
// use its structured tool signal as the first visible action, while reasoning
// alone remains insufficient to classify a pending turn.
func (g *turnActivityPresentationGate) ObserveCompletedAssistantMessage() {
	if g != nil && g.mode == turnActivityPresentationPending {
		g.mode = turnActivityPresentationNormal
	}
}

// ObserveForcedActivity records that the turn-start queue hint card bypassed
// the gate and is already visible to the user. Subsequent plain text and
// terminal updates are then allowed through so the visible card never freezes
// on the queue hint; it does not classify the turn.
func (g *turnActivityPresentationGate) ObserveForcedActivity() {
	if g != nil {
		g.initialActivityVisible = true
	}
}

func onlyProfileDiscoveryTools(tools []agentui.ToolEvent) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		name := strings.ToLower(strings.TrimSpace(tool.Name))
		if name != "amux.profile_list" && name != "amux.profile_get" {
			return false
		}
	}
	return true
}

func (g *turnActivityPresentationGate) ExternalDelegation() bool {
	return g != nil && g.mode == turnActivityPresentationExternalBot
}

func (g *turnActivityPresentationGate) Reset() {
	if g != nil {
		if g.classifyExternalBot {
			g.mode = turnActivityPresentationPending
		} else {
			g.mode = turnActivityPresentationNormal
		}
		g.initialActivityVisible = false
	}
}

func isCollaboratorListTool(tool agentui.ToolEvent) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(tool.Name)), "collaborator_list")
}

func isDelegationCreateTool(tool agentui.ToolEvent) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(tool.Name)), "delegation_create")
}

func externalBotListTool(tool agentui.ToolEvent) bool {
	if !isCollaboratorListTool(tool) {
		return false
	}
	var input struct {
		Kind string `json:"kind"`
	}
	return len(tool.InputJSON) > 0 && json.Unmarshal(tool.InputJSON, &input) == nil &&
		strings.TrimSpace(input.Kind) == "external_bot"
}

func externalBotCollaboratorFromTool(tool agentui.ToolEvent) bool {
	var input struct {
		CollaboratorID string `json:"collaborator_id"`
	}
	if len(tool.InputJSON) == 0 || json.Unmarshal(tool.InputJSON, &input) != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(input.CollaboratorID), externalBotCollaboratorPrefix)
}
