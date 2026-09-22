package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// managementPackState owns the agent-pack library card view state.
type managementPackState struct {
	packMu              sync.Mutex
	packLibraryCards    map[string]*managementPackLibraryCardState
	nextPackLibraryView uint64
}

func (b *Bridge) managementAgentPackSnapshot() (agentprofile.PackCatalogSnapshot, error) {
	if b == nil || b.managementPackActions == nil {
		return agentprofile.PackCatalogSnapshot{}, fmt.Errorf("Agent Pack catalog management is unavailable")
	}
	return b.managementPackActions.Snapshot()
}

func (b *Bridge) runManagementAgentPackLibraryOpen(openID, messageID string, surface managementSurface) {
	b.runManagementAgentPackLibraryView(openID, messageID, managementPackLibraryView{}, surface)
}

func (b *Bridge) runManagementAgentPackLibraryView(openID, messageID string, view managementPackLibraryView, surface managementSurface) {
	b.runManagementAgentPackLibraryViewWithNotice(openID, messageID, view, "", surface)
}

func (b *Bridge) runManagementAgentPackLibraryViewWithNotice(openID string, messageID string, view managementPackLibraryView, notice any, surface managementSurface) {
	version, viewLock := b.beginManagementPackLibraryView(openID, messageID)
	go func() {
		defer b.finishManagementPackLibraryView(openID, messageID, version)
		snapshot, err := b.managementAgentPackSnapshot()
		if !b.managementPackLibraryViewCurrent(openID, messageID, version) {
			return
		}
		card := renderAgentPackLibraryFailureCard()
		if err == nil {
			card = renderAgentPackLibraryCardViewForSurface(snapshot, view, notice, surface)
		} else {
			card = renderManagementCardForSurface(card, surface, managementViewLibrary)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		viewLock.Lock()
		defer viewLock.Unlock()
		if !b.managementPackLibraryViewCurrent(openID, messageID, version) {
			return
		}
		b.writeManagementCardFromCallback(ctx, openID, messageID, card)
	}()
}

func (b *Bridge) runManagementLoadedPackView(openID, messageID, namespace string, view managementPackLibraryView, surface managementSurface) {
	version, viewLock := b.beginManagementPackLibraryView(openID, messageID)
	go func() {
		defer b.finishManagementPackLibraryView(openID, messageID, version)
		snapshot, err := b.managementAgentPackSnapshot()
		if !b.managementPackLibraryViewCurrent(openID, messageID, version) {
			return
		}
		card := renderAgentPackLibraryFailureCard()
		if err == nil {
			if pack, ok := managementLoadedPackByNamespace(snapshot, namespace); ok {
				card = renderLoadedPackManagementCard(pack, view)
				card = renderManagementCardForSurface(card, surface, managementViewLibrary)
			} else {
				card = renderAgentPackLibraryCardViewForSurface(snapshot, view, managementCopy("management.this.pack.is.no.longer.in.the.expert.library"), surface)
			}
		} else {
			card = renderManagementCardForSurface(card, surface, managementViewLibrary)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		viewLock.Lock()
		defer viewLock.Unlock()
		if !b.managementPackLibraryViewCurrent(openID, messageID, version) {
			return
		}
		b.writeManagementCardFromCallback(ctx, openID, messageID, card)
	}()
}

func managementLoadedPackByNamespace(snapshot agentprofile.PackCatalogSnapshot, namespace string) (agentprofile.LoadedPackState, bool) {
	namespace = strings.TrimSpace(namespace)
	for _, pack := range snapshot.Loaded {
		if strings.TrimSpace(pack.Namespace) == namespace {
			return pack, true
		}
	}
	return agentprofile.LoadedPackState{}, false
}

func managementPackLibraryViewFromCallback(action *callback.CallBackAction) (managementPackLibraryView, bool) {
	if action == nil {
		return managementPackLibraryView{}, false
	}
	view := managementPackLibraryView{Section: managementPackLibrarySection(managementCallbackString(action, "section"))}
	switch view.Section {
	case managementPackLibrarySectionBuiltins, managementPackLibrarySectionLocal, managementPackLibrarySectionLoaded, managementPackLibrarySectionMarket:
	default:
		return managementPackLibraryView{}, false
	}
	page, err := strconv.Atoi(managementCallbackString(action, "page"))
	if err != nil || page < 0 || page > 10_000 {
		return managementPackLibraryView{}, false
	}
	view.Page = page
	return view, true
}

func (b *Bridge) runManagementAgentPackOperation(openID, messageID, action, target, expectedGeneration string, view managementPackLibraryView, surface managementSurface) {
	go func() {
		defer b.endManagementPackOperation(openID, messageID)
		displayName := managementValueOrPlaceholder(target, managementCopy("management.this.expert.pack"))
		if before, err := b.managementAgentPackSnapshot(); err == nil {
			displayName = managementPackDisplayName(before, target)
		}
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		unlockProgress := b.lockManagementPackLibraryView(openID, messageID)
		progressCard := renderFocusedManagementCardForSurface(renderAgentPackOperationCard(action), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, progressCard)
		unlockProgress()
		patchCancel()
		operationCtx, operationCancel := context.WithTimeout(context.Background(), 90*time.Second)
		var operationErr error
		var blockingReferences []string
		switch action {
		case managementActionPackInstall:
			_, operationErr = b.managementPackActions.InstallIfCurrent(operationCtx, target, expectedGeneration)
		case managementActionPackSync:
			_, operationErr = b.managementPackActions.SyncIfCurrent(operationCtx, target, expectedGeneration)
		case managementActionPackRemove:
			_, blockingReferences, operationErr = b.managementPackActions.RemoveIfCurrentAndUnreferenced(target, expectedGeneration, func() ([]string, error) {
				if b.db == nil {
					return nil, fmt.Errorf("bridge db unavailable")
				}
				return b.db.BotDefaultAgentReferences(target)
			})
		default:
			operationErr = fmt.Errorf("unsupported Agent Pack operation %q", action)
		}
		operationCancel()
		var notice any = ""
		success := operationErr == nil && len(blockingReferences) == 0
		if len(blockingReferences) > 0 {
			notice = managementCopy("management.experts.in.this.pack.are.defaults.for.d.feishu", len(blockingReferences))
		} else if errors.Is(operationErr, agentprofile.ErrCatalogStateChanged) {
			notice = managementCopy("management.the.expert.library.changed.and.has.been.refreshed.confirm")
		} else if operationErr != nil {
			notice = managementPackOperationFailureNotice(action, displayName)
		} else if action == managementActionPackInstall {
			notice = managementConcat(managementCopy("management.added"), displayName, managementCopy("management.with.automatic.updates.enabled.its.experts.are.now.available"))
		} else if action == managementActionPackSync {
			notice = managementConcat(displayName, managementCopy("management.updated"))
		} else {
			notice = managementConcat(managementCopy("management.removed"), displayName, managementCopy("management.existing.tasks.are.unaffected"))
		}
		snapshot, snapshotErr := b.managementAgentPackSnapshot()
		var finalCard string
		if snapshotErr != nil {
			finalCard = renderAgentPackOperationResultFallbackCard(notice, success)
			finalCard = renderManagementCardForSurface(finalCard, surface, managementViewLibrary)
		} else {
			finalCard = renderAgentPackLibraryCardViewForSurface(snapshot, view, notice, surface)
		}
		fallbackCard := renderManagementCardForSurface(renderAgentPackOperationResultFallbackCard(notice, success), surface, managementViewLibrary)
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		unlockFinal := b.lockManagementPackLibraryView(openID, messageID)
		defer unlockFinal()
		b.writeManagementCardFromCallbackWithFallback(finalCtx, openID, messageID, finalCard, fallbackCard)
	}()
}

func (b *Bridge) beginAndConsumeManagementPackOperation(openID, messageID, action, target, nonce string) (started, consumed bool, err error) {
	if !b.beginManagementPackOperation(openID, messageID) {
		return false, false, nil
	}
	consumed, err = b.consumeManagementPackOperation(openID, action, target, nonce)
	if err != nil || !consumed {
		b.endManagementPackOperation(openID, messageID)
	}
	return true, consumed, err
}

func (c *managementPackState) beginManagementPackOperation(openID, messageID string) bool {
	if c == nil {
		return false
	}
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	defer c.packMu.Unlock()
	state := c.managementPackLibraryCardStateLocked(key)
	if state.operationActive {
		return false
	}
	state.operationActive = true
	c.nextPackLibraryView++
	state.viewVersion = c.nextPackLibraryView
	return true
}

func (c *managementPackState) endManagementPackOperation(openID, messageID string) {
	if c == nil {
		return
	}
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	if state := c.packLibraryCards[key]; state != nil {
		state.operationActive = false
		delete(c.packLibraryCards, key)
	}
	c.packMu.Unlock()
}

func (c *managementPackState) managementPackOperationActive(openID, messageID string) bool {
	if c == nil {
		return false
	}
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	defer c.packMu.Unlock()
	state := c.packLibraryCards[key]
	return state != nil && state.operationActive
}

func (c *managementPackState) beginManagementPackLibraryView(openID, messageID string) (uint64, *sync.Mutex) {
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	defer c.packMu.Unlock()
	state := c.managementPackLibraryCardStateLocked(key)
	c.nextPackLibraryView++
	state.viewVersion = c.nextPackLibraryView
	return state.viewVersion, &state.viewLock
}

func (c *managementPackState) managementPackLibraryViewCurrent(openID, messageID string, version uint64) bool {
	if c == nil {
		return false
	}
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	defer c.packMu.Unlock()
	state := c.packLibraryCards[key]
	return state != nil && state.viewVersion == version
}

func (c *managementPackState) finishManagementPackLibraryView(openID, messageID string, version uint64) {
	if c == nil {
		return
	}
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	state := c.packLibraryCards[key]
	if state != nil && state.viewVersion == version && !state.operationActive {
		delete(c.packLibraryCards, key)
	}
	c.packMu.Unlock()
}

func (c *managementPackState) lockManagementPackLibraryView(openID, messageID string) func() {
	key := managementPackLibraryCardKey(openID, messageID)
	c.packMu.Lock()
	lock := &c.managementPackLibraryCardStateLocked(key).viewLock
	c.packMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (c *managementPackState) managementPackLibraryCardStateLocked(key string) *managementPackLibraryCardState {
	if c.packLibraryCards == nil {
		c.packLibraryCards = make(map[string]*managementPackLibraryCardState)
	}
	state := c.packLibraryCards[key]
	if state == nil {
		state = &managementPackLibraryCardState{}
		c.packLibraryCards[key] = state
	}
	return state
}

func managementPackLibraryCardKey(openID, messageID string) string {
	return strings.TrimSpace(openID) + "\x00" + strings.TrimSpace(messageID)
}

func (b *Bridge) consumeManagementPackOperation(openID, action, target, nonce string) (bool, error) {
	nonce = strings.TrimSpace(nonce)
	if b == nil || b.db == nil {
		return false, fmt.Errorf("bridge db unavailable")
	}
	if nonce == "" {
		return false, nil
	}
	key := "agent-pack:" + b.currentBridgeId() + ":" + strings.TrimSpace(openID) + ":" + action + ":" + strings.TrimSpace(target) + ":" + nonce
	return b.db.ConsumeManagementAction(key)
}

func managementPackDisplayName(snapshot agentprofile.PackCatalogSnapshot, target string) any {
	target = strings.TrimSpace(target)
	for _, pack := range snapshot.Loaded {
		if pack.Namespace == target {
			return valueOrPlaceholder(pack.DisplayName, pack.Namespace)
		}
	}
	for _, pack := range snapshot.Market {
		if pack.ID == target || pack.Namespace == target {
			return valueOrPlaceholder(pack.DisplayName, pack.Namespace)
		}
	}
	return managementValueOrPlaceholder(target, managementCopy("management.this.expert.pack"))
}

func managementPackOperationFailureNotice(action string, displayName any) any {
	if action == managementActionPackInstall {
		return managementConcat(managementCopy("management.could.not.add"), displayName, managementCopy("management.the.expert.library.is.unchanged.check.that.the.amux"))
	}
	if action == managementActionPackRemove {
		return managementConcat(managementCopy("management.could.not.remove"), displayName, managementCopy("management.the.expert.library.is.unchanged"))
	}
	return managementConcat(displayName, managementCopy("management.update.failed.current.experts.remain.available"))
}

func renderAgentPackOperationResultFallbackCard(notice any, success bool) string {
	title := managementCopy("management.expert.library.operation.incomplete")
	theme := "orange"
	icon := managementIcon("warning_outlined", "orange")
	if success {
		title = managementCopy("management.expert.library.updated")
		theme = "green"
		icon = managementIcon("check_outlined", "green")
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: managementCopy("management.expert.library.operation.results"), Summary: title, Theme: theme, Icon: icon, Elements: []cardElement{managementHintElement(managementTruncateText(notice, 240))}, Buttons: []managementButton{{Text: managementCopy("management.view.expert.library"), Type: "primary", Action: managementActionPackLibraryOpen}, {Text: managementCopy("management.back.to.assistant.settings"), Type: "default", Action: managementActionSettingsOpen}}})
}

func renderAgentPackLibraryCardViewForSurface(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice any, surface managementSurface) string {
	maxElements := managementPackLibraryMaxElements
	maxBytes := managementPackLibraryMaxBytes
	if surface == managementSurfaceAmux {
		maxElements -= managementFocusedLibraryElementReserve
		maxBytes -= managementFocusedLibraryByteReserve
	}
	card := renderAgentPackLibraryCardViewWithBudget(snapshot, view, notice, maxElements, maxBytes)
	card = renderManagementCardForSurface(card, surface, managementViewLibrary)
	if managementCardTaggedNodeCount(card) <= managementPackLibraryMaxElements && len(card) <= managementPackLibraryMaxBytes {
		return card
	}
	return renderManagementCardForSurface(renderAgentPackLibraryPageTooLargeCard(view), surface, managementViewLibrary)
}

func renderAgentPackLibraryCardViewWithBudget(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice any, maxElements, maxBytes int) string {
	view = normalizeManagementPackLibraryView(snapshot, view)
	operationNonce := newManagementConfirmationNonce()
	var pages [][]cardElement
	for {
		chunks := managementPackLibraryItemChunks(snapshot, view, operationNonce)
		pages = paginateManagementPackLibrary(snapshot, view, notice, chunks, maxElements, maxBytes)
		clampedPage := min(view.Page, len(pages)-1)
		if clampedPage == view.Page {
			break
		}
		view.Page = clampedPage
	}
	card := marshalNativeManagementCard(managementCardView{Title: managementCopy("management.expert.library"), Subtitle: managementCopy("management.manage.available.experts"), Summary: managementCopy("management.amux.expert.library"), Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: managementPackLibraryCardElements(snapshot, view, notice, pages[view.Page], len(pages))})
	if managementCardTaggedNodeCount(card) <= maxElements && len(card) <= maxBytes {
		return card
	}
	return renderAgentPackLibraryPageTooLargeCard(view)
}

func defaultManagementPackLibraryView(snapshot agentprofile.PackCatalogSnapshot) managementPackLibraryView {
	switch {
	case len(snapshot.LocalExperts) > 0:
		return managementPackLibraryView{Section: managementPackLibrarySectionLocal}
	case len(snapshot.Loaded) > 0:
		return managementPackLibraryView{Section: managementPackLibrarySectionLoaded}
	case len(snapshot.Market) > 0:
		return managementPackLibraryView{Section: managementPackLibrarySectionMarket}
	default:
		return managementPackLibraryView{Section: managementPackLibrarySectionBuiltins}
	}
}

func normalizeManagementPackLibraryView(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView) managementPackLibraryView {
	switch view.Section {
	case managementPackLibrarySectionBuiltins, managementPackLibrarySectionLocal, managementPackLibrarySectionLoaded, managementPackLibrarySectionMarket:
	default:
		view.Section = defaultManagementPackLibraryView(snapshot).Section
	}
	view.Page = max(view.Page, 0)
	return view
}

func managementPackLibraryCardElements(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice any, items []cardElement, totalPages int) []cardElement {
	elements := make([]cardElement, 0, len(items)+9)
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(managementTruncateText(notice, 240)))
	}
	hint := managementCopy("management.all.assistants.in.this.amux.system.share.the.expert")
	if view.Section == managementPackLibrarySectionLocal {
		hint = managementCopy("management.all.assistants.in.this.amux.system.share.local.experts")
	}
	elements = append(elements, managementHintElement(hint))
	elements = append(elements, managementPackLibrarySectionNavigation(snapshot, view.Section))
	backButton := managementButton{Text: managementCopy("management.back.to.assistant.settings"), Type: "default", Action: managementActionSettingsOpen, Name: managementActionSettingsOpen + ".pack_library.back"}
	elements = append(elements, managementDividerElement())
	elements = append(elements, managementSectionTitleElement(managementPackLibrarySectionTitle(view.Section)))
	if len(items) == 0 {
		elements = append(elements, managementHintElement(managementPackLibraryEmptyText(view.Section)))
	} else {
		elements = append(elements, items...)
	}
	if totalPages > 1 {
		elements = append(elements, managementPackLibraryPaginationElement(view, totalPages))
	}
	elements = append(elements, managementDividerElement())
	elements = append(elements, managementSplitActionRowElement(cardElement{}, managementNavigationButtonElement(backButton)))
	return elements
}

func managementPackLibraryPaginationElement(view managementPackLibraryView, totalPages int) cardElement {
	page := managementTextElement(managementCopy("management.page.d.d", view.Page+1, totalPages), "notation", "grey", nil)
	columns := []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{page}}}
	pageButtons := make([]managementButton, 0, 2)
	if view.Page > 0 {
		pageButtons = append(pageButtons, managementPackLibraryNavigateButton(managementCopy("management.previous.page"), view.Section, view.Page-1))
	}
	if view.Page+1 < totalPages {
		pageButtons = append(pageButtons, managementPackLibraryNavigateButton(managementCopy("management.next.page"), view.Section, view.Page+1))
	}
	for _, button := range pageButtons {
		columns = append(columns, cardColumn{Tag: "column", Width: "auto", HorizontalAlign: "right", VerticalAlign: "center", Elements: []cardElement{managementButtonElement(button)}})
	}
	return cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "8px", Columns: columns}
}

func managementPackLibraryItemChunks(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, operationNonce string) [][]cardElement {
	switch view.Section {
	case managementPackLibrarySectionBuiltins:
		chunks := make([][]cardElement, 0, len(snapshot.Builtins))
		for _, expert := range snapshot.Builtins {
			chunks = append(chunks, []cardElement{managementBuiltinExpertRowElement(expert)})
		}
		return chunks
	case managementPackLibrarySectionLocal:
		chunks := make([][]cardElement, 0, len(snapshot.LocalExperts))
		for _, expert := range snapshot.LocalExperts {
			chunks = append(chunks, []cardElement{managementLocalExpertRowElement(expert)})
		}
		return chunks
	case managementPackLibrarySectionLoaded:
		chunks := make([][]cardElement, 0, len(snapshot.Loaded))
		for _, pack := range snapshot.Loaded {
			chunks = append(chunks, managementLoadedPackElements(pack, view, operationNonce))
		}
		return chunks
	case managementPackLibrarySectionMarket:
		chunks := make([][]cardElement, 0, len(snapshot.Market))
		for _, pack := range snapshot.Market {
			chunks = append(chunks, managementMarketPackElements(pack, view, operationNonce))
		}
		return chunks
	default:
		return nil
	}
}

func managementLoadedPackElements(pack agentprofile.LoadedPackState, view managementPackLibraryView, _ string) []cardElement {
	elements := []cardElement{managementDividerElement(), managementTextElement(pack.DisplayName, "normal", "default", nil)}
	elements = append(elements, managementLoadedPackSummaryElement(pack))
	if len(pack.DuplicateNamespaces) > 0 {
		return append(elements, managementHintElement(managementCopy("management.the.same.git.repository.is.loaded.more.than.once")))
	}
	viewValues := managementPackLibraryViewValues(view)
	buttons := []managementButton{{Text: managementCopy("management.choose.an.expert"), Type: "default", Action: managementActionSettingsEdit, Name: managementActionSettingsEdit + ".pack." + pack.Namespace}, {Text: managementCopy("management.manage.expert.library"), Type: "default", Action: managementActionPackManage, Name: managementActionPackManage + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace})}}
	elements = append(elements, managementButtonRowElement(buttons))
	return elements
}

func renderLoadedPackManagementCard(pack agentprofile.LoadedPackState, view managementPackLibraryView) string {
	elements := []cardElement{managementHintElement(managementCopy("management.all.assistants.in.this.amux.system.share.the.expert.481")), managementLoadedPackSummaryElement(pack)}
	if len(pack.DuplicateNamespaces) > 0 {
		elements = append(elements, managementHintElement(managementCopy("management.the.same.git.repository.is.loaded.more.than.once")))
	} else {
		operationNonce := newManagementConfirmationNonce()
		viewValues := managementPackLibraryViewValues(view)
		elements = append(elements, managementButtonRowElement([]managementButton{{Text: managementCopy("management.check.for.updates"), Type: "primary", Action: managementActionPackSync, Name: managementActionPackSync + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace, "operation_nonce": operationNonce, "expected_generation": pack.Generation})}, {Text: managementCopy("management.remove"), Type: "default", Action: managementActionPackRemove, Name: managementActionPackRemove + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace, "operation_nonce": operationNonce, "expected_generation": pack.Generation}), Confirm: managementConfirm(managementConcat(managementCopy("management.remove.483"), pack.DisplayName, "？"), managementCopy("management.new.tasks.will.no.longer.be.able.to.select"))}}))
		switch {
		case strings.TrimSpace(pack.LastError) != "":
			elements = append(elements, managementHintElement(managementCopy("management.the.last.update.failed.current.experts.remain.available")))
		case pack.AutoSync:
			elements = append(elements, managementHintElement(managementCopy("management.automatic.updates.are.enabled.you.can.also.check.manually")))
		default:
			elements = append(elements, managementHintElement(managementCopy("management.automatic.updates.are.off.you.can.check.for.updates")))
		}
	}
	elements = append(elements, managementDividerElement(), managementFormButtonRowElement(managementNavigationButtonElement(managementPackLibraryNavigateButton(managementCopy("management.back.to.expert.library"), view.Section, view.Page))))
	return marshalNativeManagementCard(managementCardView{Title: managementValueOrPlaceholder(pack.DisplayName, managementCopy("management.expert.library.management")), Subtitle: managementCopy("management.check.for.updates.or.remove"), Summary: managementCopy("management.amux.expert.library.management"), Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}

func managementLoadedPackSummaryElement(pack agentprofile.LoadedPackState) cardElement {
	status := managementCopy("management.loaded")
	if strings.TrimSpace(pack.LastError) != "" {
		status = managementCopy("management.update.failed")
	}
	if len(pack.DuplicateNamespaces) > 0 {
		status = managementCopy("management.action.needed")
	}
	return managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.status"), Body: status}, managementSummaryItem{Title: managementCopy("management.contents"), Body: managementCopy("management.d.experts.version.s", pack.ProfileCount, managementShortRevision(pack.CurrentRevision))})
}

func managementMarketPackElements(pack agentprofile.CatalogPackState, view managementPackLibraryView, operationNonce string) []cardElement {
	status := managementCopy("management.available.to.add")
	if pack.NamespaceConflict {
		status = managementCopy("management.configuration.needed")
	}
	elements := []cardElement{managementMarketPackHeaderElement(pack.DisplayName, pack.Description, pack.GitURL, status)}
	if pack.NamespaceConflict {
		return append(elements, managementHintElement(managementCopy("management.the.default.library.identifier.is.used.by.another.git")))
	}
	return append(elements, managementButtonRowElement([]managementButton{{Text: managementConcat(managementCopy("management.add"), pack.DisplayName), Type: "default", Action: managementActionPackInstall, Name: managementActionPackInstall + "." + pack.Namespace, Values: mergeManagementButtonValues(managementPackLibraryViewValues(view), map[string]string{"catalog_id": pack.ID, "operation_nonce": operationNonce, "expected_generation": pack.Generation}), Confirm: managementConfirm(managementConcat(managementCopy("management.add"), pack.DisplayName, "？"), managementCopy("management.its.experts.will.appear.in.assistant.settings.and.stay"))}}))
}

func paginateManagementPackLibrary(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice any, chunks [][]cardElement, maxElements, maxBytes int) [][]cardElement {
	if len(chunks) == 0 {
		return [][]cardElement{{}}
	}
	pages := make([][]cardElement, 0, 1)
	current := make([]cardElement, 0)
	budgetView := view
	budgetView.Page = 1
	for _, chunk := range chunks {
		candidate := append(append([]cardElement(nil), current...), chunk...)
		card := marshalNativeManagementCard(managementCardView{Title: managementCopy("management.expert.library"), Subtitle: managementCopy("management.manage.available.experts"), Summary: managementCopy("management.amux.expert.library"), Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: managementPackLibraryCardElements(snapshot, budgetView, notice, candidate, 999)})
		if len(current) > 0 && (managementCardTaggedNodeCount(card) > maxElements || len(card) > maxBytes) {
			pages = append(pages, current)
			current = append([]cardElement(nil), chunk...)
			continue
		}
		current = candidate
	}
	pages = append(pages, current)
	return pages
}

func managementPackLibrarySectionNavigation(snapshot agentprofile.PackCatalogSnapshot, active managementPackLibrarySection) cardElement {
	items := []struct {
		section managementPackLibrarySection
		label   any
		count   int
	}{{managementPackLibrarySectionBuiltins, managementCopy("management.built.in.experts"), len(snapshot.Builtins)}, {managementPackLibrarySectionLocal, managementCopy("management.local.experts"), len(snapshot.LocalExperts)}, {managementPackLibrarySectionLoaded, managementCopy("management.loaded.libraries"), len(snapshot.Loaded)}, {managementPackLibrarySectionMarket, managementCopy("management.expert.marketplace"), len(snapshot.Market)}}
	buttons := make([]managementButton, 0, len(items))
	for _, item := range items {
		buttonType := "default"
		if item.section == active {
			buttonType = "primary"
		}
		buttons = append(buttons, managementPackLibraryNavigateButton(managementConcat(item.label, " ", strconv.Itoa(item.count)), item.section, 0))
		buttons[len(buttons)-1].Name = fmt.Sprintf("%s.section.%s", managementActionPackLibraryNavigate, item.section)
		buttons[len(buttons)-1].Type = buttonType
	}
	return managementButtonRowElement(buttons)
}

func managementPackLibraryNavigateButton(text any, section managementPackLibrarySection, page int) managementButton {
	return managementButton{Text: text, Type: "default", Action: managementActionPackLibraryNavigate, Name: fmt.Sprintf("%s.page.%s.%d", managementActionPackLibraryNavigate, section, page), Values: managementPackLibraryViewValues(managementPackLibraryView{Section: section, Page: page})}
}

func managementPackLibraryViewValues(view managementPackLibraryView) map[string]string {
	return map[string]string{"section": string(view.Section), "page": strconv.Itoa(max(view.Page, 0))}
}

func mergeManagementButtonValues(base, extra map[string]string) map[string]string {
	values := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		values[key] = value
	}
	for key, value := range extra {
		values[key] = value
	}
	return values
}

func managementPackLibrarySectionTitle(section managementPackLibrarySection) any {
	switch section {
	case managementPackLibrarySectionBuiltins:
		return managementCopy("management.built.in.experts")
	case managementPackLibrarySectionLocal:
		return managementCopy("management.local.experts")
	case managementPackLibrarySectionMarket:
		return managementCopy("management.expert.marketplace")
	default:
		return managementCopy("management.loaded.libraries")
	}
}

func managementPackLibraryEmptyText(section managementPackLibrarySection) any {
	switch section {
	case managementPackLibrarySectionBuiltins:
		return managementCopy("management.no.built.in.experts")
	case managementPackLibrarySectionLocal:
		return managementCopy("management.no.local.experts.create.a.copy.of.a.built")
	case managementPackLibrarySectionMarket:
		return managementCopy("management.all.marketplace.libraries.are.already.loaded")
	default:
		return managementCopy("management.no.external.expert.libraries.are.loaded.yet")
	}
}

func managementCardTaggedNodeCount(card string) int {
	var document any
	if err := json.Unmarshal([]byte(card), &document); err != nil {
		return managementPackLibraryMaxElements + 1
	}
	count := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if tag, ok := typed["tag"].(string); ok && strings.TrimSpace(tag) != "" {
				count++
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(document)
	return count
}

func renderAgentPackLibraryPageTooLargeCard(view managementPackLibraryView) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.too.much.content.on.this.page"), Subtitle: managementCopy("management.reopen.this.section"), Summary: managementCopy("management.amux.expert.library.page.too.large"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement(managementCopy("management.content.exceeds.feishu.card.limits.and.could.not.be"))}, Buttons: []managementButton{managementPackLibraryNavigateButton(managementCopy("management.reopen.this.section.514"), view.Section, 0), {Text: managementCopy("management.back.to.assistant.settings"), Type: "default", Action: managementActionSettingsOpen}}})
}

func managementMarketPackHeaderElement(displayName string, description string, gitURL string, status any) cardElement {
	title := managementTextElement(managementConcat(strings.TrimSpace(displayName), " · ", status), "normal", "default", nil)
	title.Text.Lines = 1
	content := []cardElement{title}
	if strings.TrimSpace(description) != "" {
		detail := managementTextElement(truncateCardText(strings.Join(strings.Fields(description), " "), managementMarketPackDescriptionMaxRunes), "notation", "grey", nil)
		detail.Text.Lines = 1
		content = append(content, detail)
	}
	if strings.TrimSpace(gitURL) != "" {
		source := managementTextElement("Git · "+strings.TrimSpace(gitURL), "notation", "grey", nil)
		content = append(content, source)
	}
	return cardElement{Tag: "column_set", FlexMode: "none", Margin: "6px 0px 2px 0px", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "2px", BackgroundStyle: "grey-50", Padding: "8px 10px 8px 10px", Elements: content}}}
}

func managementBuiltinExpertRowElement(expert agentprofile.BuiltinExpertState) cardElement {
	name := managementTextElement(expert.Name, "normal", "default", nil)
	name.Text.Lines = 1
	columns := []cardColumn{{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{name}}}
	description := strings.Join(strings.Fields(expert.Description), " ")
	if description != "" {
		detail := managementTextElement(truncateCardText(description, managementBuiltinExpertDescriptionMaxRunes), "notation", "grey", nil)
		detail.Text.Lines = 1
		columns = append(columns, cardColumn{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{detail}})
	}
	return cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", VerticalAlign: "center", Columns: columns}
}

func managementLocalExpertRowElement(expert agentprofile.LocalExpertState) cardElement {
	title := managementTextElement(expert.Ref, "normal", "default", nil)
	title.Text.Lines = 1
	detailParts := []any{managementAgentTypeName(expert.AgentType)}
	if revision := managementShortRevision(expert.Revision); revision != "" {
		detailParts = append(detailParts, managementConcat(managementCopy("management.version.515"), revision))
	}
	if description := strings.Join(strings.Fields(expert.Description), " "); description != "" {
		detailParts = append(detailParts, truncateCardText(description, managementMarketPackDescriptionMaxRunes))
	}
	detail := managementTextElement(managementJoin(" · ", detailParts...), "notation", "grey", nil)
	detail.Text.Lines = 2
	return cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", VerticalAlign: "center", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{title, detail}}}}
}

func renderAgentPackLibraryFailureCard() string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.expert.library.unavailable"), Subtitle: managementCopy("management.try.again.later"), Summary: managementCopy("management.amux.expert.library.unavailable"), Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: []cardElement{managementHintElement(managementCopy("management.cannot.read.the.expert.library.nothing.was.changed"))}, Buttons: []managementButton{{Text: managementCopy("management.retry"), Type: "primary", Action: managementActionPackLibraryOpen}, {Text: managementCopy("management.back.to.assistant.settings"), Type: "default", Action: managementActionSettingsOpen}}})
}

func renderAgentPackOperationCard(action string) string {
	title := managementCopy("management.checking.expert.library.updates")
	subtitle := managementCopy("management.reading.the.latest.version")
	if action == managementActionPackInstall {
		title = managementCopy("management.adding.expert.library")
		subtitle = managementCopy("management.downloading.and.checking")
	} else if action == managementActionPackRemove {
		title = managementCopy("management.removing.expert.library")
		subtitle = managementCopy("management.existing.tasks.will.not.be.affected")
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: title, Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementHintElement(managementCopy("management.refreshes.automatically.when.complete"))}})
}

func managementShortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 7 {
		return revision[:7]
	}
	return valueOrPlaceholder(revision, "unknown")
}

type managementPackLibrarySection string

const (
	managementPackLibrarySectionBuiltins managementPackLibrarySection = "builtins"
	managementPackLibrarySectionLocal    managementPackLibrarySection = "local"
	managementPackLibrarySectionLoaded   managementPackLibrarySection = "loaded"
	managementPackLibrarySectionMarket   managementPackLibrarySection = "market"
)

type managementPackLibraryView struct {
	Section managementPackLibrarySection

	Page int
}
