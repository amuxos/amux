package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/rs/zerolog/log"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/cleanup"
	"github.com/amuxos/amux/internal/config"
)

func dailyCleanupSettingValue() string {
	settings, err := config.ReadDailyCleanupSettings()
	if err != nil {
		return ""
	}
	return strconv.FormatBool(settings.Enabled)
}
func dailyCleanupOptions() []cardOption {
	return []cardOption{
		{Text: managementPlainText(managementCopy("cleanup.enabled")), Value: "true"},
		{Text: managementPlainText(managementCopy("cleanup.disabled")), Value: "false"},
	}
}
func dailyCleanupInitial(value string) string {
	if value == "false" {
		return "false"
	}
	return "true"
}
func saveDailyCleanupChoice(value string) error {
	if value == "" {
		return nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return managementLocalizedError("cleanup.invalid")
	}
	if err := config.SaveDailyCleanupEnabled(enabled); err != nil {
		return fmt.Errorf("%s: %w", managementDefaultText(managementCopy("cleanup.save_failed")), err)
	}
	return nil
}

// CleanupRecipientKey includes the current owner so changing ownership cannot
// suppress a newly assigned administrator's pending report.
func (b *Bridge) CleanupRecipientKey() string {
	if b == nil || b.secrets == nil || strings.TrimSpace(b.secrets.OpenId) == "" {
		return ""
	}
	return b.currentBridgeId() + ":" + b.secrets.OpenId
}
func (b *Bridge) NotifyDailyCleanup(ctx context.Context, report cleanup.Report) error {
	if b.CleanupRecipientKey() == "" {
		return fmt.Errorf("cleanup report owner is unavailable")
	}
	card := renderDailyCleanupReport(report)
	var err error
	var messageID string
	if b.dmInteractive != nil {
		messageID, _, err = b.dmInteractive(ctx, b.secrets.OpenId, card)
	} else if b.client != nil {
		sum := sha256.Sum256([]byte(report.ID + ":" + b.CleanupRecipientKey()))
		messageID, _, err = b.sendInteractiveToDMWithUUID(ctx, b.secrets.OpenId, card, "daily cleanup report", hex.EncodeToString(sum[:16]))
	} else {
		err = fmt.Errorf("cleanup report client is unavailable")
	}
	if err == nil {
		log.Info().Str("report_id", report.ID).Str("message_id", messageID).Msg("bridge: daily cleanup report delivered")
	}
	return err
}
func renderDailyCleanupReport(r cleanup.Report) string {
	title := managementCopy("cleanup.report.title")
	theme := "green"
	status := managementCopy("cleanup.report.success")
	if r.Failed() {
		theme = "orange"
		status = managementCopy("cleanup.report.partial")
	}
	elements := []cardElement{managementHintElement(status), managementCompactSummaryElement(
		managementSummaryItem{Title: managementCopy("cleanup.report.runtime"), Body: r.Target},
		managementSummaryItem{Title: managementCopy("cleanup.report.time"), Body: r.FinishedAt.Format("2006-01-02 15:04:05 MST")},
	)}
	// Coalesce per-principal results. Do not disclose paths, account names,
	// provider payloads or raw filesystem/database error messages in a DM.
	order := []string{}
	steps := map[string]cleanup.Step{}
	for _, s := range r.Steps {
		if _, ok := steps[s.Name]; !ok {
			order = append(order, s.Name)
		}
		sum := steps[s.Name]
		sum.Name = s.Name
		sum.Candidates += s.Candidates
		sum.Deleted += s.Deleted
		sum.Skipped += s.Skipped
		sum.Bytes += s.Bytes
		sum.Errors = append(sum.Errors, s.Errors...)
		steps[s.Name] = sum
	}
	for _, name := range order {
		s := steps[name]
		key := "cleanup.step." + name
		switch name {
		case "sessions", "tasks", "task_sessions", "workspaces", "session_artifacts", "provider_rollouts", "provider_trust", "provider_home", "logs", "database", "isolation", "references":
		default:
			key = "cleanup.step.other"
		}
		elements = append(elements, managementCompactSummaryElement(managementSummaryItem{Title: managementCopy(key), Body: managementCopy("cleanup.report.counts", s.Deleted, s.Skipped, float64(s.Bytes)/(1024*1024), len(s.Errors))}))
	}
	elements = append(elements, managementHintElement(managementCopy("cleanup.report.details")), managementHintElement(managementConcat(managementCopy("cleanup.report.id"), r.ID)))
	return marshalNativeManagementCard(managementCardView{Title: title, Summary: title, Theme: theme, Elements: elements})
}
