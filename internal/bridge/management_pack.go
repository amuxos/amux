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
func (b *Bridge) runManagementAgentPackLibraryViewWithNotice(openID, messageID string, view managementPackLibraryView, notice string, surface managementSurface) {
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
				card = renderAgentPackLibraryCardViewForSurface(snapshot, view, "这组专家已不在专家库中，已刷新当前列表。", surface)
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
		displayName := valueOrPlaceholder(target, "这组专家")
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
		notice := ""
		success := operationErr == nil && len(blockingReferences) == 0
		if len(blockingReferences) > 0 {
			notice = fmt.Sprintf("这组专家中有专家正被 %d 个飞书助手设为默认专家，请先更换后再移除。", len(blockingReferences))
		} else if errors.Is(operationErr, agentprofile.ErrCatalogStateChanged) {
			notice = "专家库已发生变化，已为你刷新。请重新确认后操作。"
		} else if operationErr != nil {
			notice = managementPackOperationFailureNotice(action, displayName)
		} else if action == managementActionPackInstall {
			notice = "已添加 " + displayName + "，并已开启自动更新。现在可以选择其中的专家。"
		} else if action == managementActionPackSync {
			notice = displayName + " 更新完成。"
		} else {
			notice = "已移除 " + displayName + "。已有任务不受影响。"
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
func managementPackDisplayName(snapshot agentprofile.PackCatalogSnapshot, target string) string {
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
	return valueOrPlaceholder(target, "这组专家")
}
func managementPackOperationFailureNotice(action, displayName string) string {
	if action == managementActionPackInstall {
		return "未能添加 " + displayName + "，专家库没有变化。请确认运行 amux 的机器状态正常并可访问组织仓库后重试。"
	}
	if action == managementActionPackRemove {
		return "未能移除 " + displayName + "，专家库没有变化。"
	}
	return displayName + " 更新失败，当前专家仍可继续使用。"
}
func renderAgentPackOperationResultFallbackCard(notice string, success bool) string {
	title := "专家库操作未完成"
	theme := "orange"
	icon := managementIcon("warning_outlined", "orange")
	if success {
		title = "专家库已更新"
		theme = "green"
		icon = managementIcon("check_outlined", "green")
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: "专家库操作结果", Summary: title, Theme: theme, Icon: icon, Elements: []cardElement{managementHintElement(truncateCardText(notice, 240))}, Buttons: []managementButton{{Text: "查看专家库", Type: "primary", Action: managementActionPackLibraryOpen}, {Text: "返回助手设置", Type: "default", Action: managementActionSettingsOpen}}})
}
func renderAgentPackLibraryCardViewForSurface(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice string, surface managementSurface) string {
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
func renderAgentPackLibraryCardViewWithBudget(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice string, maxElements, maxBytes int) string {
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
	card := marshalNativeManagementCard(managementCardView{Title: "专家库", Subtitle: "管理可用专家", Summary: "amux 专家库", Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: managementPackLibraryCardElements(snapshot, view, notice, pages[view.Page], len(pages))})
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
func managementPackLibraryCardElements(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice string, items []cardElement, totalPages int) []cardElement {
	elements := make([]cardElement, 0, len(items)+9)
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(truncateCardText(notice, 240)))
	}
	hint := "当前 amux 的所有助手共享专家库；添加、更新或移除会影响所有助手。"
	if view.Section == managementPackLibrarySectionLocal {
		hint = "当前 amux 的所有助手共享本地专家；这里只展示已发布版本，编辑和恢复请在运行 amux 的机器上完成。"
	}
	elements = append(elements, managementHintElement(hint))
	elements = append(elements, managementPackLibrarySectionNavigation(snapshot, view.Section))
	backButton := managementButton{Text: "返回助手设置", Type: "default", Action: managementActionSettingsOpen, Name: managementActionSettingsOpen + ".pack_library.back"}
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
	page := managementTextElement(fmt.Sprintf("第 %d / %d 页", view.Page+1, totalPages), "notation", "grey", nil)
	columns := []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{page}}}
	pageButtons := make([]managementButton, 0, 2)
	if view.Page > 0 {
		pageButtons = append(pageButtons, managementPackLibraryNavigateButton("上一页", view.Section, view.Page-1))
	}
	if view.Page+1 < totalPages {
		pageButtons = append(pageButtons, managementPackLibraryNavigateButton("下一页", view.Section, view.Page+1))
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
		return append(elements, managementHintElement("同一 Git 仓库被重复加载。请在运行 amux 的机器上处理后再更新或移除。"))
	}
	viewValues := managementPackLibraryViewValues(view)
	buttons := []managementButton{{Text: "选择专家", Type: "default", Action: managementActionSettingsEdit, Name: managementActionSettingsEdit + ".pack." + pack.Namespace}, {Text: "管理专家库", Type: "default", Action: managementActionPackManage, Name: managementActionPackManage + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace})}}
	elements = append(elements, managementButtonRowElement(buttons))
	return elements
}
func renderLoadedPackManagementCard(pack agentprofile.LoadedPackState, view managementPackLibraryView) string {
	elements := []cardElement{managementHintElement("当前 amux 的所有助手共享专家库；这里的修改会影响所有助手。"), managementLoadedPackSummaryElement(pack)}
	if len(pack.DuplicateNamespaces) > 0 {
		elements = append(elements, managementHintElement("同一 Git 仓库被重复加载。请在运行 amux 的机器上处理后再更新或移除。"))
	} else {
		operationNonce := newManagementConfirmationNonce()
		viewValues := managementPackLibraryViewValues(view)
		elements = append(elements, managementButtonRowElement([]managementButton{{Text: "检查更新", Type: "primary", Action: managementActionPackSync, Name: managementActionPackSync + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace, "operation_nonce": operationNonce, "expected_generation": pack.Generation})}, {Text: "移除", Type: "default", Action: managementActionPackRemove, Name: managementActionPackRemove + "." + pack.Namespace, Values: mergeManagementButtonValues(viewValues, map[string]string{"namespace": pack.Namespace, "operation_nonce": operationNonce, "expected_generation": pack.Generation}), Confirm: managementConfirm("移除 "+pack.DisplayName+"？", "移除后，新任务将不能再选择其中的专家；已有任务不受影响。")}}))
		switch {
		case strings.TrimSpace(pack.LastError) != "":
			elements = append(elements, managementHintElement("上次更新失败，当前专家仍可继续使用。"))
		case pack.AutoSync:
			elements = append(elements, managementHintElement("已开启自动更新；也可以随时手动检查。"))
		default:
			elements = append(elements, managementHintElement("自动更新尚未开启，可以手动检查更新。"))
		}
	}
	elements = append(elements, managementDividerElement(), managementFormButtonRowElement(managementNavigationButtonElement(managementPackLibraryNavigateButton("返回专家库", view.Section, view.Page))))
	return marshalNativeManagementCard(managementCardView{Title: valueOrPlaceholder(pack.DisplayName, "专家库管理"), Subtitle: "检查更新或移除", Summary: "amux 专家库管理", Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}
func managementLoadedPackSummaryElement(pack agentprofile.LoadedPackState) cardElement {
	status := "已加载"
	if strings.TrimSpace(pack.LastError) != "" {
		status = "更新失败"
	}
	if len(pack.DuplicateNamespaces) > 0 {
		status = "需要处理"
	}
	return managementCompactSummaryElement(managementSummaryItem{Title: "状态", Body: status}, managementSummaryItem{Title: "内容", Body: fmt.Sprintf("%d 个专家 · 版本 %s", pack.ProfileCount, managementShortRevision(pack.CurrentRevision))})
}
func managementMarketPackElements(pack agentprofile.CatalogPackState, view managementPackLibraryView, operationNonce string) []cardElement {
	status := "可添加"
	if pack.NamespaceConflict {
		status = "需要配置"
	}
	elements := []cardElement{managementMarketPackHeaderElement(pack.DisplayName, pack.Description, pack.GitURL, status)}
	if pack.NamespaceConflict {
		return append(elements, managementHintElement("默认专家库标识已被另一 Git 仓库使用。请在运行 amux 的机器上换一个标识添加。"))
	}
	return append(elements, managementButtonRowElement([]managementButton{{Text: "添加 " + pack.DisplayName, Type: "default", Action: managementActionPackInstall, Name: managementActionPackInstall + "." + pack.Namespace, Values: mergeManagementButtonValues(managementPackLibraryViewValues(view), map[string]string{"catalog_id": pack.ID, "operation_nonce": operationNonce, "expected_generation": pack.Generation}), Confirm: managementConfirm("添加 "+pack.DisplayName+"？", "添加后，其中的专家会出现在助手设置中并自动保持更新；当前专家不会改变。")}}))
}
func paginateManagementPackLibrary(snapshot agentprofile.PackCatalogSnapshot, view managementPackLibraryView, notice string, chunks [][]cardElement, maxElements, maxBytes int) [][]cardElement {
	if len(chunks) == 0 {
		return [][]cardElement{{}}
	}
	pages := make([][]cardElement, 0, 1)
	current := make([]cardElement, 0)
	budgetView := view
	budgetView.Page = 1
	for _, chunk := range chunks {
		candidate := append(append([]cardElement(nil), current...), chunk...)
		card := marshalNativeManagementCard(managementCardView{Title: "专家库", Subtitle: "管理可用专家", Summary: "amux 专家库", Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: managementPackLibraryCardElements(snapshot, budgetView, notice, candidate, 999)})
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
		label   string
		count   int
	}{{managementPackLibrarySectionBuiltins, "预置专家", len(snapshot.Builtins)}, {managementPackLibrarySectionLocal, "本地专家", len(snapshot.LocalExperts)}, {managementPackLibrarySectionLoaded, "已加载专家库", len(snapshot.Loaded)}, {managementPackLibrarySectionMarket, "专家库市场", len(snapshot.Market)}}
	buttons := make([]managementButton, 0, len(items))
	for _, item := range items {
		buttonType := "default"
		if item.section == active {
			buttonType = "primary"
		}
		buttons = append(buttons, managementPackLibraryNavigateButton(fmt.Sprintf("%s %d", item.label, item.count), item.section, 0))
		buttons[len(buttons)-1].Name = fmt.Sprintf("%s.section.%s", managementActionPackLibraryNavigate, item.section)
		buttons[len(buttons)-1].Type = buttonType
	}
	return managementButtonRowElement(buttons)
}
func managementPackLibraryNavigateButton(text string, section managementPackLibrarySection, page int) managementButton {
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
func managementPackLibrarySectionTitle(section managementPackLibrarySection) string {
	switch section {
	case managementPackLibrarySectionBuiltins:
		return "预置专家"
	case managementPackLibrarySectionLocal:
		return "本地专家"
	case managementPackLibrarySectionMarket:
		return "专家库市场"
	default:
		return "已加载专家库"
	}
}
func managementPackLibraryEmptyText(section managementPackLibrarySection) string {
	switch section {
	case managementPackLibrarySectionBuiltins:
		return "暂无预置专家。"
	case managementPackLibrarySectionLocal:
		return "暂无本地专家，可以从预置专家创建副本。"
	case managementPackLibrarySectionMarket:
		return "市场中的专家库都已加载。"
	default:
		return "当前还没有加载外部专家库。"
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
	return marshalNativeManagementCard(managementCardView{Title: "本页内容过多", Subtitle: "请重新打开当前分区", Summary: "amux 专家库内容过多", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement("内容超过飞书卡片限制，未能完整显示。")}, Buttons: []managementButton{managementPackLibraryNavigateButton("重新打开当前分区", view.Section, 0), {Text: "返回助手设置", Type: "default", Action: managementActionSettingsOpen}}})
}
func managementMarketPackHeaderElement(displayName, description, gitURL, status string) cardElement {
	title := managementTextElement(strings.TrimSpace(displayName)+" · "+strings.TrimSpace(status), "normal", "default", nil)
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
	detailParts := []string{managementAgentTypeName(expert.AgentType)}
	if revision := managementShortRevision(expert.Revision); revision != "" {
		detailParts = append(detailParts, "版本 "+revision)
	}
	if description := strings.Join(strings.Fields(expert.Description), " "); description != "" {
		detailParts = append(detailParts, truncateCardText(description, managementMarketPackDescriptionMaxRunes))
	}
	detail := managementTextElement(strings.Join(detailParts, " · "), "notation", "grey", nil)
	detail.Text.Lines = 2
	return cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", VerticalAlign: "center", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{title, detail}}}}
}
func renderAgentPackLibraryFailureCard() string {
	return marshalNativeManagementCard(managementCardView{Title: "专家库暂不可用", Subtitle: "请稍后重试", Summary: "amux 专家库不可用", Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: []cardElement{managementHintElement("无法读取专家库，本次未做任何更改。")}, Buttons: []managementButton{{Text: "重试", Type: "primary", Action: managementActionPackLibraryOpen}, {Text: "返回助手设置", Type: "default", Action: managementActionSettingsOpen}}})
}
func renderAgentPackOperationCard(action string) string {
	title := "正在检查专家库更新"
	subtitle := "正在读取最新版本"
	if action == managementActionPackInstall {
		title = "正在添加专家库"
		subtitle = "正在下载并检查"
	} else if action == managementActionPackRemove {
		title = "正在移除专家库"
		subtitle = "已有任务不会受到影响"
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: title, Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementHintElement("完成后自动刷新。")}})
}
func managementShortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 7 {
		return revision[:7]
	}
	return valueOrPlaceholder(revision, "未知")
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
