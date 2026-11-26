package bridge

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
)

const discoveryContextStart = "\n\n[amux-capability-context]\n"
const discoveryContextEnd = "\n[/amux-capability-context]"

// The plaza runs on the Hub; its Agent may run on another machine. Send a small
// allowlisted snapshot instead of asking the Agent to guess the Hub's config
// from its own filesystem. Never serialize managementState or config wholesale.
func (b *Bridge) discoverySnapshot() map[string]any {
	s := b.managementState()
	now := time.Now()
	data := map[string]any{
		"observed_at":      now.UTC().Format(time.RFC3339),
		"hub_local_time":   now.Format(time.RFC3339),
		"hub_timezone":     now.Location().String(),
		"bot_profile":      s.ProfileName,
		"interaction_mode": s.Mode,
		"new_session_defaults": map[string]string{
			"expert": s.DefaultAgent, "engine": s.DefaultRuntimeRef,
			"model": s.DefaultModelRef, "reasoning_effort": s.DefaultReasoningEffort,
		},
		"experiments_running":               discoveryExperimentSnapshot(b.runningExperimentsSettings()),
		"experiments_saved_status":          "unavailable",
		"group_watch_enabled":               s.GroupWatch.Enabled,
		"group_watch_group_count":           len(s.GroupWatch.ChatIDs),
		"document_comments_enabled":         s.DocumentCommentsEnabled,
		"bot_collaboration_enabled":         s.BotToBot.Enabled,
		"version":                           s.SystemInfo.Version,
		"connected_target_count":            s.Targets,
		"full_system_health":                "not_checked",
		"native_model_availability":         "not_checked",
		"current_work_session_goal_support": "unknown",
	}
	if saved, err := b.managementConfigActions().ReadExperimentsSettings(); err == nil {
		data["experiments_saved_status"] = "ok"
		data["experiments_saved"] = discoveryExperimentSnapshot(saved)
	}
	var runtimes []map[string]any
	for _, runtime := range s.Runtimes {
		runtimes = append(runtimes, map[string]any{"ref": runtime.Ref, "name": runtime.DisplayName, "engine": runtime.AgentType})
	}
	data["configured_engines"] = runtimes
	return data
}

func discoveryExperimentSnapshot(s config.ExperimentsSettings) map[string]any {
	return map[string]any{
		"group_settings":           s.GroupSettingsEnabled,
		"memory":                   s.MemoryEnabled,
		"steer_foreground":         s.SteerForeground,
		"show_background_tasks":    s.ShowBackgroundTasks,
		"mention_turn_initiator":   s.MentionTurnInitiator,
		"completion_notification":  s.CompletionNotification.Enabled,
		"completion_after_seconds": s.CompletionNotification.AfterSeconds,
	}
}

func (b *Bridge) discoveryTaskPrompt(c discoveryCapability, owner, prompt string) string {
	// Appending prose to a formal command changes its meaning: for example a
	// read-only "goal" becomes "goal <objective>" and would create a goal.
	if _, goal, _ := command.ParseGoal(prompt); goal || strings.HasPrefix(strings.TrimSpace(prompt), "/") {
		return prompt
	}
	locale := b.guideLocale(owner)
	snapshot, _ := json.Marshal(b.discoverySnapshot())
	return prompt + discoveryContextStart + discoveryCopy("task.context", c.text("title").For(locale), c.text("body").For(locale), c.text("requires").For(locale), string(snapshot)).For(locale) +
		"\n" + discoveryCopy("task.guidance").For(locale) +
		"\n" + c.text("guidance").For(locale) +
		"\n" + bridgeExtensions.snapshot().Onboarding.PromptGuidance + discoveryContextEnd
}
