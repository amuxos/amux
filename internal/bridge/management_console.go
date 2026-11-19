package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	managementActionConsoleOpen    = "system.console.open"
	managementActionConsoleEnable  = "system.console.enable"
	managementActionConsoleDisable = "system.console.disable"
	managementActionConsoleRefresh = "system.console.refresh"
)

// ManagementConsoleInfo is ephemeral, system-wide state. Password must never
// appear in an overview, callback value, log, URL, or persisted Bridge state.
type ManagementConsoleInfo struct {
	Enabled   bool
	URL       string
	Password  string
	ExpiresAt time.Time
}

// ManagementConsoleActions is shared by every Bot in this Hub installation.
// Open is idempotent and never extends the existing 30-minute lifetime.
type ManagementConsoleActions interface {
	Snapshot() ManagementConsoleInfo
	Open(context.Context) (ManagementConsoleInfo, error)
	Close(context.Context) error
}

// SetManagementConsoleActions must be called before Start.
func (b *Bridge) SetManagementConsoleActions(actions ManagementConsoleActions) {
	if b != nil {
		b.managementConsole = actions
	}
}

func isManagementConsoleAction(action string) bool {
	switch action {
	case managementActionConsoleOpen, managementActionConsoleEnable, managementActionConsoleDisable, managementActionConsoleRefresh:
		return true
	}
	return false
}

func (b *Bridge) handleManagementConsoleAction(ctx context.Context, event *callback.CardActionTriggerEvent, action string) *callback.CardActionTriggerResponse {
	openID, messageID := "", ""
	if event.Event.Operator != nil {
		openID = event.Event.Operator.OpenID
	}
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	// A callback is not proof of private delivery. Require both the configured
	// Bot owner and the server-recorded DM recipient, even for read/refresh.
	checkCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	private := b.db != nil && b.isBotOwner(openID) && b.ownerConfirmed() &&
		b.db.isPrivateManagementCardContext(checkCtx, b.currentBridgeId(), openID, messageID)
	cancel()
	if !private {
		return cardToast("error", managementCopy("management.console.owner_private_required"))
	}
	b.invalidateFeishuPermissionRecheck(messageID)
	version := b.advanceModelServiceEditor(messageID)
	if b.managementConsole == nil {
		go b.writeManagementConsoleCard(context.Background(), openID, messageID, ManagementConsoleInfo{}, "unavailable", version)
		return cardToast("error", managementCopy("management.console.unavailable"))
	}
	if action == managementActionConsoleOpen || action == managementActionConsoleRefresh {
		info := b.managementConsole.Snapshot()
		card := renderManagementConsoleCard(info, "", time.Now())
		var toast *callback.CardActionTriggerResponse
		if action == managementActionConsoleRefresh || b.managementCardPatch != nil {
			toast = cardToast("success", managementCopy("management.console.refreshed"))
		}
		return b.managementCardResponse(openID, messageID, card, version, toast)
	}
	// Starting/stopping a listener must not block the Feishu callback deadline.
	go func() {
		opCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b.writeManagementConsoleCard(opCtx, openID, messageID, ManagementConsoleInfo{}, "working", version)
		var err error
		if action == managementActionConsoleEnable {
			_, err = b.managementConsole.Open(opCtx)
		} else {
			err = b.managementConsole.Close(opCtx)
		}
		notice := ""
		if err != nil {
			notice = "failed" // No raw child diagnostics, credentials, or URL parameters.
		}
		b.writeManagementConsoleCard(opCtx, openID, messageID, b.managementConsole.Snapshot(), notice, version)
	}()
	return cardToast("success", managementCopy("management.console.updating"))
}

// Secret-bearing cards use the same per-card ordering lock as all management
// navigation, but deliberately do not log returned Feishu/transport errors: a
// third-party error may include request content. No card content is persisted.
func (b *Bridge) writeManagementConsoleCard(parent context.Context, openID, messageID string, info ManagementConsoleInfo, notice string, version uint64) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	unlock, err := b.lockGroupSettingsRouteContext(ctx, "management-card\x00"+strings.TrimSpace(messageID))
	if err != nil {
		return
	}
	defer unlock()
	current := func() bool {
		return b.isBotOwner(openID) && b.ownerConfirmed() && b.modelServiceEditorCurrent(messageID, version) &&
			b.db != nil && b.db.isPrivateManagementCardContext(ctx, b.currentBridgeId(), openID, messageID)
	}
	if !current() {
		return
	}
	card := renderManagementConsoleCard(info, notice, time.Now())
	attempted, err := b.patchManagementCard(ctx, messageID, b.managementCardForRecipient(openID, card))
	if attempted && err == nil {
		return
	}
	if !current() || (b.dmInteractive == nil && b.client == nil) {
		return
	}
	// Fallback delivery is exclusively a new DM to the same confirmed owner.
	_ = b.sendManagementCardToDM(ctx, openID, card)
}

func renderManagementConsoleCard(info ManagementConsoleInfo, notice string, now time.Time) string {
	enabled := info.Enabled && info.ExpiresAt.After(now)
	status, theme, tagColor := managementCopy("management.console.off"), "wathet", "neutral"
	if enabled {
		status, tagColor = managementCopy("management.console.on"), "green"
	}
	rows := []cardElement{
		managementKeyValueRowElement(managementCopy("management.console.status"), status),
	}
	if enabled {
		remaining := info.ExpiresAt.Sub(now)
		seconds := int((remaining + time.Second - 1) / time.Second)
		rows = append(rows,
			managementKeyValueRowElement(managementCopy("management.console.address"), info.URL),
			managementKeyValueRowElement(managementCopy("management.console.remaining"), fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)),
			managementKeyValueRowElement(managementCopy("management.console.password"), info.Password),
		)
	}
	summary := managementVerticalSummaryElement("amux_console_summary", rows...)
	if enabled {
		summary.Columns[0].BackgroundStyle = "blue-50"
	}
	elements := []cardElement{
		managementBackToOverviewElement(), managementDividerElement(),
		summary,
	}
	elements = append(elements, managementHintElement(managementCopy("management.console.explanation")))
	if enabled && strings.HasPrefix(info.URL, "http://") {
		elements = append(elements, managementHintElement(managementCopy("management.console.http_warning")))
	}
	if notice != "" {
		elements = append(elements, managementHintElement(managementCopy("management.console."+notice)))
		if notice == "failed" || notice == "unavailable" {
			theme = "orange"
		}
	}
	buttons := []managementButton{}
	if enabled {
		buttons = append(buttons, managementButton{Text: managementCopy("management.console.visit"), Type: "primary", URL: info.URL})
		buttons = append(buttons, managementButton{Text: managementCopy("management.console.close"), Type: "danger", Action: managementActionConsoleDisable, Name: "amux.console.disable"})
	} else if notice != "working" && notice != "unavailable" {
		buttons = append(buttons, managementButton{Text: managementCopy("management.console.enable"), Type: "primary", Action: managementActionConsoleEnable, Name: "amux.console.enable"})
	}
	buttons = append(buttons, managementButton{Text: managementCopy("management.console.refresh"), Type: "default", Action: managementActionConsoleRefresh, Name: "amux.console.refresh"})
	iconColor := "blue"
	if theme == "orange" {
		iconColor = "orange"
	}
	card := marshalNativeManagementCard(managementCardView{
		Title: managementCopy("management.console.title"), Summary: managementCopy("management.console.title"),
		Theme: theme, Icon: managementIcon("version_outlined", iconColor),
		Tags:     []cardTextTag{{Tag: "text_tag", Text: managementPlainText(status), Color: tagColor}},
		Elements: elements, Buttons: buttons,
	})
	return renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
}
