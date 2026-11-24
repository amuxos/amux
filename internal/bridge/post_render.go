package bridge

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/i18n"
)

const (
	maxFeishuCardBytes                     = 30 * 1024
	maxPermissionOptions                   = 50
	maxPermissionInputRunes                = 3000
	maxPlanEntries                         = 50
	maxPlanEntryRunes                      = 200
	maxActivityStepRunes                   = maxAssistantProgressRunes
	maxActivitySubtasks                    = 20
	maxSubtaskDetailRunes                  = 160
	maxTurnCardActivityBytes               = 18 * 1024
	maxCompactActivityStepRunes            = 1000
	amuxBrandDividerID                     = "brand_divider"
	amuxBrandFooterID                      = "brand_footer"
	externalBotDelegationTargetElementID   = "external_target"
	externalBotDelegationTaskElementID     = "external_task"
	externalBotDelegationCallbackElementID = "external_callback"
)

func amuxExternalBotDelegationProtocolURL() string { return distribution.DelegationProtocol }
func amuxBrandFooterMarkdown() string              { return distribution.BrandFooterMarkdown }
func legacyAgentBrandFooter() string               { return distribution.LegacyBrandFooter }

type postDoc struct {
	ZhCN postLang `json:"zh_cn"`
}

type postLang struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

type postElement struct {
	Tag      string   `json:"tag"`
	Text     string   `json:"text,omitempty"`
	Href     string   `json:"href,omitempty"`
	UserID   string   `json:"user_id,omitempty"`
	Language string   `json:"language,omitempty"`
	Style    []string `json:"style,omitempty"`
}

func renderAgentReplyPost(text string) string {
	return renderAgentReplyPostWithMentions(text, nil)
}

func renderAgentReplyPostWithMentions(text string, openIDs []string) string {
	text = renderArtifactLinks(text)
	blocks := markdownLitePostBlocks(text)
	openIDs = uniqueOpenIDs(openIDs)
	if len(openIDs) > 0 {
		mention := make([]postElement, 0, len(openIDs)*2-1)
		for i, openID := range openIDs {
			if i > 0 {
				mention = append(mention, postElement{Tag: "text", Text: " "})
			}
			mention = append(mention, postElement{Tag: "at", UserID: openID})
		}
		blocks = append([][]postElement{mention}, blocks...)
	}
	doc := postDoc{ZhCN: postLang{Content: blocks}}
	if len(doc.ZhCN.Content) == 0 {
		doc.ZhCN.Content = [][]postElement{{{Tag: "text", Text: strings.TrimSpace(text)}}}
	}
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func renderBotHandoffPost(botOpenID, text string) string {
	blocks := markdownLitePostBlocks(text)
	if len(blocks) == 0 {
		blocks = [][]postElement{{{Tag: "text", Text: strings.TrimSpace(text)}}}
	}
	mention := []postElement{
		{Tag: "at", UserID: strings.TrimSpace(botOpenID)},
		{Tag: "text", Text: " 协作结果"},
	}
	blocks = append([][]postElement{mention}, blocks...)
	doc := postDoc{ZhCN: postLang{Content: blocks}}
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func renderExternalBotDelegationCard(botOpenID, callbackBotOpenID, text string) string {
	targetMention := `<at id="` + html.EscapeString(strings.TrimSpace(botOpenID)) + `"></at>`
	callbackMention := `<at id="` + html.EscapeString(strings.TrimSpace(callbackBotOpenID)) + `"></at>`
	doc := newCardDoc([]cardElement{
		{
			Tag:       "markdown",
			ElementID: cardKitReplyElementID,
			Content: targetMention + " 协作任务[\u200b](" + amuxExternalBotDelegationProtocolURL() + ")\n\n" +
				strings.TrimSpace(text),
		},
		{
			Tag:       "markdown",
			ElementID: externalBotDelegationCallbackElementID,
			Content:   "完成后请在本话题回复并 " + callbackMention + "。",
		},
	})
	doc.Config.WidthMode = "fill"
	doc.Config.Summary = &cardSummary{Content: "协作任务"}
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func renderAgentReplyCard(text string) string {
	elements := renderFinalResultElements(text)
	elements = append(elements, renderAmuxBrandFooterElements()...)
	doc := newTurnCardDoc(elements)
	bs, _ := json.Marshal(doc)
	return string(bs)
}

// renderAgentReplyCompletionCard retains the original activity only after a
// mentioned final answer was delivered. The answer, feedback and brand belong
// to the new final card, not to the completed progress card.
func renderAgentReplyCompletionCard(activity *turnCardActivity) string {
	elements := make([]cardElement, 0, 2)
	if activity != nil {
		if activity.Element.Tag != "" {
			elements = append(elements, collapsedCardElement(activity.Element))
		}
		for _, trailing := range activity.Trailing {
			elements = append(elements, collapsedCardElement(trailing))
		}
	}
	if len(elements) == 0 {
		elements = append(elements, renderExecutionSummaryLine(toolActivityCardMeta{Status: "done"}))
	}
	doc := newTurnCardDoc(elements)
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func collapsedCardElement(element cardElement) cardElement {
	raw, err := json.Marshal(element)
	if err != nil {
		return element
	}
	var clone cardElement
	if err := json.Unmarshal(raw, &clone); err != nil {
		return element
	}
	collapseCardPanels(&clone)
	return clone
}

func collapseCardPanels(element *cardElement) {
	if element == nil {
		return
	}
	if element.Tag == "collapsible_panel" {
		element.Expanded = false
	}
	for i := range element.Elements {
		collapseCardPanels(&element.Elements[i])
	}
	for i := range element.Columns {
		for j := range element.Columns[i].Elements {
			collapseCardPanels(&element.Columns[i].Elements[j])
		}
	}
}

func renderAgentReplyCardWithMentions(text string, openIDs []string) string {
	return renderMentionFinalCard(text, openIDs, false)
}

func renderMentionFinalCard(text string, openIDs []string, withConversationSection bool) string {
	elements := []cardElement{{Tag: "markdown", Content: renderAgentResultBody(text), ElementID: cardKitReplyElementID},
		{Tag: "hr", ElementID: amuxBrandDividerID}}
	if withConversationSection {
		elements = append(elements, mentionedConversationSection(openIDs))
	} else {
		elements = append(elements, renderMentionFooterRow(openIDs, nil))
	}
	doc := newTurnCardDoc(elements)
	updateMulti := true
	doc.Config.UpdateMulti = &updateMulti
	bs, _ := json.Marshal(doc)
	return string(bs)
}

// The footer row is also the quoting boundary: labels, native mentions and
// extension controls must never become quoted Agent answer text.
func renderMentionFooterRow(openIDs []string, feedback []cardElement) cardElement {
	columns := []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center",
		Elements: []cardElement{{Tag: "markdown", Content: amuxBrandFooterMarkdown(), TextSize: "notation"}}}}
	if mentions := renderTurnInitiatorMentionsMarkdown(openIDs); mentions != "" {
		label := sessionMessage("reply.sent.to")
		columns = append(columns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center",
			Elements: []cardElement{managementContentElement(i18n.Render(func(locale i18n.Locale) string {
				return "<font color='grey'>" + label.For(locale) + "</font> " + mentions
			}), cardElement{Tag: "markdown", TextSize: "notation"})}})
	}
	if len(feedback) > 0 {
		columns = append(columns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: feedback})
	}
	return cardElement{Tag: "column_set", ElementID: amuxBrandFooterID, FlexMode: "none", HorizontalSpacing: "8px", Columns: columns}
}

func newTurnCardDoc(elements []cardElement) cardDoc {
	doc := newCardDoc(wrapTurnCardElements(elements))
	doc.Config.WidthMode = "fill"
	doc.Body.Direction = "vertical"
	doc.Body.VerticalSpacing = "12px"
	doc.Body.Padding = "12px 16px 16px 16px"
	return doc
}

func normalizeAgentResultText(text string) string {
	text = strings.TrimRight(text, " \t\r\n")
	for _, footer := range []string{legacyAgentBrandFooter(), amuxBrandFooterMarkdown()} {
		if !strings.HasSuffix(text, footer) {
			continue
		}
		text = strings.TrimSpace(strings.TrimSuffix(text, footer))
		text = strings.TrimSpace(strings.TrimSuffix(text, "---"))
	}
	return strings.TrimSpace(text)
}

func renderAmuxBrandFooterElements() []cardElement {
	return renderAmuxBrandFooterElementsWithMentions(nil)
}

func renderAmuxBrandFooterElementsWithMentions(openIDs []string) []cardElement {
	footer := cardElement{Tag: "markdown", ElementID: amuxBrandFooterID, Content: amuxBrandFooterMarkdown(), TextSize: "notation"}
	if mentions := renderTurnInitiatorMentionsMarkdown(openIDs); mentions != "" {
		label := sessionMessage("reply.sent.to")
		footer = managementContentElement(i18n.Render(func(locale i18n.Locale) string {
			return amuxBrandFooterMarkdown() + " <font color='grey'>| " + label.For(locale) + "</font> " + mentions
		}), footer)
	}
	return []cardElement{
		{Tag: "hr", ElementID: amuxBrandDividerID},
		footer,
	}
}

type cardDoc struct {
	Schema string      `json:"schema"`
	Config cardConfig  `json:"config"`
	Header *cardHeader `json:"header,omitempty"`
	Body   cardBody    `json:"body"`
}

type cardBody struct {
	Elements        []cardElement `json:"elements"`
	Direction       string        `json:"direction,omitempty"`
	VerticalSpacing string        `json:"vertical_spacing,omitempty"`
	Padding         string        `json:"padding,omitempty"`
}

type cardConfig struct {
	WideScreenMode bool         `json:"wide_screen_mode,omitempty"`
	WidthMode      string       `json:"width_mode,omitempty"`
	Summary        *cardSummary `json:"summary,omitempty"`
	EnableForward  *bool        `json:"enable_forward,omitempty"`
	UpdateMulti    *bool        `json:"update_multi,omitempty"`
}

func newCardDoc(elements []cardElement) cardDoc {
	return cardDoc{
		Schema: "2.0",
		Config: cardConfig{WideScreenMode: true},
		Body:   cardBody{Elements: elements},
	}
}

type cardHeader struct {
	Title       cardTitle     `json:"title"`
	Subtitle    *cardTitle    `json:"subtitle,omitempty"`
	Template    string        `json:"template,omitempty"`
	Icon        *cardIcon     `json:"icon,omitempty"`
	TextTagList []cardTextTag `json:"text_tag_list,omitempty"`
	Padding     string        `json:"padding,omitempty"`
}

type cardTextTag struct {
	Tag   string    `json:"tag"`
	Text  cardTitle `json:"text"`
	Color string    `json:"color,omitempty"`
}

type cardTitle struct {
	I18nContent map[string]string `json:"i18n_content,omitempty"`
	Tag         string            `json:"tag"`
	Content     string            `json:"content"`
	ElementID   string            `json:"element_id,omitempty"`
	TextSize    string            `json:"text_size,omitempty"`
	TextColor   string            `json:"text_color,omitempty"`
	TextAlign   string            `json:"text_align,omitempty"`
	Lines       int               `json:"lines,omitempty"`
}

type cardSummary struct {
	I18nContent map[string]string `json:"i18n_content,omitempty"`
	Content     string            `json:"content"`
}

type cardIcon struct {
	Tag   string `json:"tag"`
	Token string `json:"token,omitempty"`
	Color string `json:"color,omitempty"`
}

type cardConfirm struct {
	Title cardTitle `json:"title"`
	Text  cardTitle `json:"text"`
}

type cardElement struct {
	I18nContent       map[string]string `json:"i18n_content,omitempty"`
	Tag               string            `json:"tag"`
	ElementID         string            `json:"element_id,omitempty"`
	Content           string            `json:"content,omitempty"`
	Actions           []cardAction      `json:"actions,omitempty"`
	Text              *cardTitle        `json:"text,omitempty"`
	Icon              *cardIcon         `json:"icon,omitempty"`
	Type              string            `json:"type,omitempty"`
	Size              string            `json:"size,omitempty"`
	URL               string            `json:"url,omitempty"`
	Value             json.RawMessage   `json:"value,omitempty"`
	Behaviors         []cardBehavior    `json:"behaviors,omitempty"`
	Name              string            `json:"name,omitempty"`
	Disabled          bool              `json:"disabled,omitempty"`
	Expanded          bool              `json:"expanded,omitempty"`
	Header            *panelHeader      `json:"header,omitempty"`
	Elements          []cardElement     `json:"elements,omitempty"`
	Columns           []cardColumn      `json:"columns,omitempty"`
	FlexMode          string            `json:"flex_mode,omitempty"`
	Options           []cardOption      `json:"options,omitempty"`
	Placeholder       *cardTitle        `json:"placeholder,omitempty"`
	Required          bool              `json:"required,omitempty"`
	ActionType        string            `json:"action_type,omitempty"`
	InitialOption     string            `json:"initial_option,omitempty"`
	DefaultValue      string            `json:"default_value,omitempty"`
	Width             string            `json:"width,omitempty"`
	Direction         string            `json:"direction,omitempty"`
	VerticalSpacing   string            `json:"vertical_spacing,omitempty"`
	HorizontalSpacing string            `json:"horizontal_spacing,omitempty"`
	HorizontalAlign   string            `json:"horizontal_align,omitempty"`
	VerticalAlign     string            `json:"vertical_align,omitempty"`
	TextSize          string            `json:"text_size,omitempty"`
	BackgroundStyle   string            `json:"background_style,omitempty"`
	BackgroundColor   string            `json:"background_color,omitempty"`
	HasBorder         bool              `json:"has_border,omitempty"`
	BorderColor       string            `json:"border_color,omitempty"`
	CornerRadius      string            `json:"corner_radius,omitempty"`
	Padding           string            `json:"padding,omitempty"`
	Margin            string            `json:"margin,omitempty"`
	Confirm           *cardConfirm      `json:"confirm,omitempty"`
}

type cardBehavior struct {
	Type       string            `json:"type"`
	Value      map[string]string `json:"value,omitempty"`
	DefaultURL string            `json:"default_url,omitempty"`
	PCURL      string            `json:"pc_url,omitempty"`
	IOSURL     string            `json:"ios_url,omitempty"`
	AndroidURL string            `json:"android_url,omitempty"`
}

type cardOption struct {
	Text  cardTitle `json:"text"`
	Value string    `json:"value"`
}

type panelHeader struct {
	Title           cardTitle `json:"title"`
	BackgroundColor string    `json:"background_color,omitempty"`
	VerticalAlign   string    `json:"vertical_align,omitempty"`
	Padding         string    `json:"padding,omitempty"`
	Position        string    `json:"position,omitempty"`
}

type cardColumn struct {
	Tag             string        `json:"tag"`
	Width           string        `json:"width"`
	Weight          int           `json:"weight,omitempty"`
	Direction       string        `json:"direction,omitempty"`
	VerticalAlign   string        `json:"vertical_align,omitempty"`
	HorizontalAlign string        `json:"horizontal_align,omitempty"`
	VerticalSpacing string        `json:"vertical_spacing,omitempty"`
	BackgroundStyle string        `json:"background_style,omitempty"`
	Padding         string        `json:"padding,omitempty"`
	Margin          string        `json:"margin,omitempty"`
	Elements        []cardElement `json:"elements"`
}

type cardAction = cardElement

type toolActivityCardMeta struct {
	TurnSeq         int
	Status          string
	TitlePrefix     string
	Question        string
	Followup        string
	AgentProfile    string
	AgentType       string
	EffectiveModel  string
	QuotaBorrowed   bool
	ViewURL         string
	Activity        *agentui.ActivityView
	Subtasks        []subtaskProgress
	ThinkingProcess string
	// QueuedInputs is the number of inputs waiting behind this running turn
	// (steer_foreground experiment); shown as one line while not terminal.
	QueuedInputs int
	// SubtaskReport marks a turn started by a background-task completion
	// notice: it has no user question, so an empty execution summary renders
	// as a bare status line instead of a "no more details" panel.
	SubtaskReport bool
	// Placeholder marks the immediate-feedback state: the turn started but
	// produced no visible output yet. The card shows a "⏳ 处理中…" line
	// (with elapsed time) until the first output replaces it.
	Placeholder bool
	// PlaceholderError is the user-facing error shown in place of the
	// placeholder line when the turn fails before any output.
	PlaceholderError string
	// ModelRecovery renders same-engine fallback switch buttons inside the
	// activity content block when a turn hits a rate/usage limit or remains
	// queued/retrying without output for at least 60s.
	ModelRecovery *modelRecoveryCardPrompt
	Now           time.Time
}

type turnCardActivity struct {
	Element  cardElement
	Trailing []cardElement
	JSON     json.RawMessage
	Terminal bool
}

func (a turnCardActivity) elements() []cardElement {
	return wrapTurnCardElements(a.contentElements())
}

func (a turnCardActivity) contentElements() []cardElement {
	elements := []cardElement{a.Element}
	return append(elements, a.Trailing...)
}

func wrapTurnCardElements(elements []cardElement) []cardElement {
	return []cardElement{{
		Tag:       "column_set",
		ElementID: cardKitActivityElementID,
		FlexMode:  "none",
		Columns: []cardColumn{{
			Tag:             "column",
			Width:           "weighted",
			Weight:          1,
			VerticalAlign:   "top",
			VerticalSpacing: "12px",
			Elements:        elements,
		}},
	}}
}

func renderPermissionRequestCard(targetID, sessionName, permissionID string, payload permissionPayload, openID string) string {
	if strings.TrimSpace(payload.Tool.Question) != "" {
		return renderQuestionCard(targetID, sessionName, permissionID, payload, openID)
	}
	payload = compactPermissionPayload(payload)
	content := permissionSummary(payload)
	if content == "" {
		content = "Agent 请求执行需要确认的操作。"
	}
	headerTitle := "Agent 工具授权请求"
	actions := make([]cardAction, 0, len(payload.Options))
	for _, opt := range payload.Options {
		optionID := strings.TrimSpace(opt.OptionId)
		if optionID == "" {
			continue
		}
		label := opt.Name
		if label == "" {
			label = optionID
		}
		callbackValue := map[string]string{
			"permission_id": permissionID,
			"option_id":     optionID,
			"target_id":     targetID,
			"session_name":  sessionName,
		}
		actions = append(actions, cardAction{
			Tag:       "button",
			Text:      &cardTitle{Tag: "plain_text", Content: label},
			Type:      permissionButtonType(opt),
			Behaviors: []cardBehavior{{Type: "callback", Value: callbackValue}},
			Name:      optionID,
		})
	}
	if len(actions) == 0 {
		callbackValue := map[string]string{
			"permission_id": permissionID,
			"option_id":     "allow",
			"target_id":     targetID,
			"session_name":  sessionName,
		}
		actions = append(actions, cardAction{Tag: "button", Text: &cardTitle{Tag: "plain_text", Content: "Allow"}, Type: "primary", Behaviors: []cardBehavior{{Type: "callback", Value: callbackValue}}, Name: "allow"})
	}
	elements := []cardElement{
		{Tag: "markdown", Content: content},
		managementFormButtonRowElement(actions...),
	}
	doc := newCardDoc(elements)
	doc.Header = &cardHeader{Title: cardTitle{Tag: "plain_text", Content: headerTitle}, Template: "orange"}
	return marshalPermissionCard(doc)
}

func compactPermissionPayload(payload permissionPayload) permissionPayload {
	payload.Tool.Title = truncateCardText(payload.Tool.Title, 160)
	payload.Tool.Question = truncateCardText(payload.Tool.Question, 500)
	if len(payload.Options) > maxPermissionOptions {
		payload.Options = append([]permissionOption(nil), payload.Options[:maxPermissionOptions]...)
	}
	for i := range payload.Options {
		payload.Options[i].Name = truncateCardText(payload.Options[i].Name, 100)
	}
	return payload
}

func marshalPermissionCard(doc cardDoc) string {
	bs, _ := json.Marshal(doc)
	if len(bs) <= maxFeishuCardBytes {
		return string(bs)
	}
	var controls []cardElement
	for _, element := range doc.Body.Elements {
		if element.Tag == "column_set" || element.Tag == "form" {
			controls = append(controls, element)
		}
	}
	doc.Body.Elements = append([]cardElement{{Tag: "markdown", Content: "请求详情过长，已省略部分内容。"}}, controls...)
	for {
		bs, _ = json.Marshal(doc)
		if len(bs) <= maxFeishuCardBytes {
			return string(bs)
		}
		if !dropLastPermissionChoice(&doc) {
			break
		}
	}
	// A single provider option ID can itself exceed the card limit and cannot
	// be truncated without changing the wire response. Fail closed instead of
	// rendering an oversized or semantically incorrect approval control.
	doc.Body.Elements = []cardElement{{Tag: "markdown", Content: "授权选项过长，请在 CLI 或其他受支持的界面完成审批。"}}
	bs, _ = json.Marshal(doc)
	return string(bs)
}

func dropLastPermissionChoice(doc *cardDoc) bool {
	if doc == nil {
		return false
	}
	for i := len(doc.Body.Elements) - 1; i >= 0; i-- {
		element := &doc.Body.Elements[i]
		if element.Tag == "column_set" && len(element.Columns) > 1 {
			element.Columns = element.Columns[:len(element.Columns)-1]
			return true
		}
		if element.Tag != "form" {
			continue
		}
		for j := len(element.Elements) - 1; j >= 0; j-- {
			control := &element.Elements[j]
			if len(control.Options) > 1 {
				control.Options = control.Options[:len(control.Options)-1]
				return true
			}
		}
	}
	return false
}

func renderPermissionQuestionForm(payload permissionPayload, metadata json.RawMessage) cardElement {
	form := cardElement{Tag: "form", Name: "permission_form"}
	options := make([]cardOption, 0, len(payload.Options))
	for _, opt := range payload.Options {
		label := opt.Name
		if label == "" {
			label = opt.OptionId
		}
		options = append(options, cardOption{Text: managementPlainText(label), Value: opt.OptionId})
	}
	if len(options) > 0 {
		tag, name, placeholder := "select_static", "option_id", "question.choose_one"
		if payload.Tool.MultiSelect {
			tag, name, placeholder = "multi_select_static", "option_ids", "question.choose_many"
		}
		form.Elements = append(form.Elements, cardElement{
			Tag: tag, Name: name, Options: options, Required: !payload.Tool.AllowOther,
			Placeholder: managementPlainTextPtr(managementCopy(placeholder)),
		})
	}
	if len(options) == 0 || payload.Tool.AllowOther {
		placeholder := "question.answer"
		if len(options) > 0 {
			placeholder = "question.other"
		}
		form.Elements = append(form.Elements, cardElement{
			Tag: "input", Name: "answer", Required: len(options) == 0,
			Placeholder: managementPlainTextPtr(managementCopy(placeholder)),
		})
	}
	form.Elements = append(form.Elements, cardElement{
		Tag: "button", Name: "permission_submit", Type: "primary", ActionType: "form_submit",
		Text: managementPlainTextPtr(managementCopy("question.submit")), Value: metadata,
	})
	return form
}

func renderPermissionResolvedCard(text any) string {
	doc := newCardDoc([]cardElement{managementContentElement(text, cardElement{Tag: "markdown"})})
	doc.Header = &cardHeader{Title: managementPlainText(managementCopy("permission.result"))}
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func permissionButtonType(opt permissionOption) string {
	switch {
	case strings.Contains(opt.Kind, "reject") || strings.Contains(strings.ToLower(opt.OptionId), "reject"):
		return "danger"
	case strings.Contains(opt.Kind, "always") || strings.Contains(strings.ToLower(opt.OptionId), "always"):
		return "primary"
	default:
		return "default"
	}
}

func renderToolActivityCard(meta toolActivityCardMeta, text string) string {
	activity := renderTurnActivity(meta)
	if meta.Activity == nil {
		content := i18n.Render(func(locale i18n.Locale) string {
			return toolActivityCardContent(meta.Question, meta.Followup, text, locale)
		})
		if strings.TrimSpace(content.For(i18n.Chinese)) != "" {
			setExecutionSummaryDetails(&activity.Element, []cardElement{managementContentElement(content, cardElement{Tag: "markdown"})})
			activity.JSON, _ = json.Marshal(activity.Element)
		}
	}
	doc := newCardDoc(activity.elements())
	return marshalActivityCard(doc, meta)
}

func renderTurnActivity(meta toolActivityCardMeta) turnCardActivity {
	if meta.Now.IsZero() {
		meta.Now = time.Now()
	}
	element := renderTurnActivityElement(meta)
	raw, _ := json.Marshal(element)
	if len(raw) > maxTurnCardActivityBytes && meta.Activity != nil && strings.TrimSpace(meta.Activity.CurrentStep) != "" {
		original := meta.Activity.CurrentStep
		low, high := 0, len([]rune(original))
		var bestElement cardElement
		var bestRaw []byte
		for low <= high {
			mid := low + (high-low)/2
			candidateMeta := meta
			candidateView := *meta.Activity
			candidateView.CurrentStep = truncateAssistantProgress(original, mid)
			candidateMeta.Activity = &candidateView
			candidateElement := renderTurnActivityElement(candidateMeta)
			candidateRaw, _ := json.Marshal(candidateElement)
			if len(candidateRaw) <= maxTurnCardActivityBytes {
				bestElement, bestRaw = candidateElement, candidateRaw
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if len(bestRaw) > 0 {
			element, raw = bestElement, bestRaw
		}
	}
	if len(raw) > maxTurnCardActivityBytes {
		element = renderCompactActivityElement(meta)
		raw, _ = json.Marshal(element)
		if len(raw) > maxTurnCardActivityBytes {
			element = managementContentElement(sessionMessage("details.long"), cardElement{Tag: "markdown", ElementID: cardKitActivityContentID})
			raw, _ = json.Marshal(element)
		}
	}
	terminal := meta.Status == "done" || meta.Status == "waiting" || meta.Status == "failed" || meta.Status == "stopped"
	if meta.QueuedInputs > 0 && !terminal && len(element.Columns) > 0 {
		// The queue line lives inside the activity content element, not in
		// Trailing: an existing CardKit card only ever replaces that element
		// (and skips the update when its JSON is unchanged), so a trailing
		// line added after the card exists never reached Feishu.
		notice := managementTextElement(sessionMessage("queue.notice", meta.QueuedInputs), "notation", "grey", nil)
		notice.ElementID = "queue_notice"
		element.Columns[0].Elements = append(element.Columns[0].Elements, notice)
		raw, _ = json.Marshal(element)
	}
	if recovery := renderModelRecoveryElements(meta.ModelRecovery); len(recovery) > 0 && len(element.Columns) > 0 {
		// Recovery controls also live inside the activity content element so
		// CardKit ReplaceElement can attach them after 60s of queueing or on
		// rate-limit failure, and automatically remove them once output resumes.
		element.Columns[0].Elements = append(element.Columns[0].Elements, recovery...)
		raw, _ = json.Marshal(element)
	}
	activity := turnCardActivity{
		Element:  element,
		JSON:     raw,
		Terminal: terminal,
	}
	if strings.TrimSpace(meta.ViewURL) != "" {
		activity.Trailing = []cardElement{{
			Tag:       "action",
			ElementID: "viewer_action",
			Actions: []cardAction{{
				Tag:  "button",
				Text: managementPlainTextPtr(sessionMessage("view")),
				Type: "default",
				URL:  strings.TrimSpace(meta.ViewURL),
			}},
		}}
	}
	return activity
}

func renderTurnActivityElement(meta toolActivityCardMeta) cardElement {
	details := renderTurnActivityDetails(meta)
	var summary cardElement
	switch {
	case len(details) > 0:
		summary = renderExecutionSummaryPanel(meta, details)
	case meta.SubtaskReport && strings.TrimSpace(meta.Question) == "":
		// A background-completion report has no user question and nothing
		// to expand: show the status line alone instead of a panel whose
		// only content is "no more details".
		summary = renderExecutionSummaryLine(meta)
	default:
		summary = renderExecutionSummaryPanel(meta, []cardElement{managementContentElement(sessionMessage("details.empty"), cardElement{Tag: "markdown"})})
	}
	children := []cardElement{summary}
	if line, ok := renderPlaceholderStatus(meta); ok {
		children = append(children, line)
	}
	if meta.ThinkingProcess != config.FeishuThinkingProcessHidden && meta.Activity != nil {
		if step := strings.TrimSpace(meta.Activity.CurrentStep); step != "" {
			children = append(children, renderProcessingUpdate(step, meta.ThinkingProcess))
		}
	}
	return cardElement{
		Tag:       "column_set",
		ElementID: cardKitActivityContentID,
		FlexMode:  "none",
		Columns: []cardColumn{{
			Tag:             "column",
			Width:           "weighted",
			Weight:          1,
			VerticalAlign:   "top",
			VerticalSpacing: "12px",
			Elements:        children,
		}},
	}
}

// renderCompactActivityElement produces a size-bounded fallback when the full
// activity details exceed the card budget. Unlike a single-sentence fallback,
// this preserves the panel header (status, agent, model, elapsed, usage), a
// compact count-based summary, and the latest processing step so users can
// still see progress and status in Feishu without opening the full output.
func renderCompactActivityElement(meta toolActivityCardMeta) cardElement {
	var summaryParts []any
	if meta.Activity != nil {
		if len(meta.Activity.Plan) > 0 {
			done := 0
			for _, entry := range meta.Activity.Plan {
				switch entry.Status {
				case "completed", "done":
					done++
				}
			}
			summaryParts = append(summaryParts, sessionMessage("plan", done, len(meta.Activity.Plan)))
		}
		if len(meta.Activity.Tools) > 0 {
			summaryParts = append(summaryParts, sessionMessage("calls", len(meta.Activity.Tools)))
		}
	}
	if len(meta.Subtasks) > 0 {
		if meta.Status == "done" || meta.Status == "waiting" || meta.Status == "failed" || meta.Status == "stopped" {
			summaryParts = append(summaryParts, sessionMessage("subtasks", len(meta.Subtasks)))
		} else {
			if count := activeSubtaskCount(meta.Subtasks); count > 0 {
				summaryParts = append(summaryParts, sessionMessage("subtasks.active", count))
			} else {
				summaryParts = append(summaryParts, sessionMessage("subtasks", len(meta.Subtasks)))
			}
		}
	}
	var summaryElements []cardElement
	if len(summaryParts) > 0 {
		summaryElements = []cardElement{renderActivitySectionBlock(managementJoin(" · ", summaryParts...), "compact_summary")}
	} else {
		summaryElements = []cardElement{managementContentElement(sessionMessage("details.long"), cardElement{Tag: "markdown"})}
	}
	children := []cardElement{renderExecutionSummaryPanel(meta, summaryElements)}
	if meta.ThinkingProcess != config.FeishuThinkingProcessHidden && meta.Activity != nil {
		if step := strings.TrimSpace(meta.Activity.CurrentStep); step != "" {
			children = append(children, renderProcessingUpdate(
				truncateAssistantProgress(step, maxCompactActivityStepRunes),
				meta.ThinkingProcess,
			))
		}
	}
	return cardElement{
		Tag:       "column_set",
		ElementID: cardKitActivityContentID,
		FlexMode:  "none",
		Columns: []cardColumn{{
			Tag:             "column",
			Width:           "weighted",
			Weight:          1,
			VerticalAlign:   "top",
			VerticalSpacing: "12px",
			Elements:        children,
		}},
	}
}

func renderExecutionSummaryPanel(meta toolActivityCardMeta, elements []cardElement) cardElement {
	titlePrefix := strings.TrimSpace(meta.TitlePrefix)
	if titlePrefix != "" {
		titlePrefix += " "
	}
	return cardElement{
		Tag:             "collapsible_panel",
		ElementID:       cardKitExecutionSummaryID,
		Expanded:        false,
		VerticalSpacing: "8px",
		Header:          &panelHeader{Title: managementStyledText(i18n.Render(func(locale i18n.Locale) string { return titlePrefix + toolActivityDisplayTitle(meta, locale) }), cardTitle{TextSize: "notation", TextColor: "grey", Tag: "plain_text"})},
		Elements:        elements,
	}
}

// renderExecutionSummaryLine is the execution summary without a collapsible
// body: the same status title as plain grey notation text.
func renderExecutionSummaryLine(meta toolActivityCardMeta) cardElement {
	panel := renderExecutionSummaryPanel(meta, nil)
	title := panel.Header.Title
	return cardElement{Tag: "div", ElementID: cardKitExecutionSummaryID, Text: &title, Width: "fill"}
}

func setExecutionSummaryDetails(activity *cardElement, details []cardElement) {
	if activity == nil || len(activity.Columns) == 0 || len(activity.Columns[0].Elements) == 0 {
		return
	}
	summary := &activity.Columns[0].Elements[0]
	if summary.Tag != "collapsible_panel" {
		return
	}
	summary.Elements = details
}

func renderTurnActivityDetails(meta toolActivityCardMeta) []cardElement {
	view := meta.Activity
	if view == nil {
		content := i18n.Render(func(locale i18n.Locale) string {
			return toolActivityCardContent(meta.Question, meta.Followup, "", locale)
		})
		if content.For(i18n.Chinese) == "" {
			return nil
		}
		return []cardElement{managementContentElement(content, cardElement{Tag: "markdown"})}
	}
	var elements []cardElement
	if question := strings.TrimSpace(meta.Question); question != "" {
		elements = append(elements, renderQuestionBlock(question, meta.Followup, "turn_question"))
	}
	if view.Status == "done" {
		return append(elements, renderCompletedActivityDetails(view, meta.Subtasks)...)
	}
	if len(view.Plan) > 0 {
		elements = append(elements, renderActivitySectionBlock(i18n.Render(func(locale i18n.Locale) string { return renderPlan(view.Plan, locale) }), "plan_progress"))
	}
	if len(view.Tools) > 0 {
		now := time.Now()
		elements = append(elements, renderExecutionRecordsBlock(i18n.Render(func(locale i18n.Locale) string { return renderToolSection(view.Tools, now, locale) })))
	}
	if len(meta.Subtasks) > 0 {
		elements = append(elements, renderActivitySectionBlock(i18n.Render(func(locale i18n.Locale) string { return renderSubtaskSection(meta.Subtasks, locale) }), "collaboration_tasks"))
	}
	return elements
}

func activeSubtaskCount(subtasks []subtaskProgress) int {
	count := 0
	for _, subtask := range subtasks {
		if !isTerminalActivityItemStatus(subtask.Status) {
			count++
		}
	}
	return count
}

func isTerminalActivityItemStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "success", "succeeded", "failed", "error", "cancelled", "canceled", "stopped", "timed_out", "timeout":
		return true
	default:
		return false
	}
}

func marshalActivityCard(doc cardDoc, meta toolActivityCardMeta) string {
	bs, _ := json.Marshal(doc)
	if len(bs) <= maxFeishuCardBytes {
		return string(bs)
	}
	elements := []cardElement{renderCompactActivityElement(meta)}
	if viewer, ok := findCardElementByID(doc.Body.Elements, "viewer_action"); ok {
		elements = append(elements, viewer)
	}
	doc.Body.Elements = wrapTurnCardElements(elements)
	bs, _ = json.Marshal(doc)
	if len(bs) > maxFeishuCardBytes {
		doc.Body.Elements = []cardElement{managementContentElement(sessionMessage("details.viewer"), cardElement{Tag: "markdown"})}
		bs, _ = json.Marshal(doc)
	}
	return string(bs)
}

func findCardElementByID(elements []cardElement, elementID string) (cardElement, bool) {
	for _, element := range elements {
		if element.ElementID == elementID {
			return element, true
		}
		if found, ok := findCardElementByID(element.Elements, elementID); ok {
			return found, true
		}
		for _, column := range element.Columns {
			if found, ok := findCardElementByID(column.Elements, elementID); ok {
				return found, true
			}
		}
	}
	return cardElement{}, false
}

func renderQuestionBlock(question, followup, elementID string) cardElement {
	content := sessionMessage("question", escapeCardMarkdown(strings.TrimSpace(question)))
	if followup = strings.TrimSpace(followup); followup != "" {
		content = i18n.Join(content, i18n.Literal("\n\n"), sessionMessage("followup", escapeCardMarkdown(followup)))
	}
	return renderPaddedActivityBlock(content, elementID, "blue-50", "8px 12px")
}

func renderActivitySectionBlock(content any, elementID string) cardElement {
	return renderPaddedActivityBlock(content, elementID, "grey-50", "10px 12px")
}

func renderExecutionRecordsBlock(content any) cardElement {
	return cardElement{
		Tag:       "column_set",
		ElementID: "execution_records",
		FlexMode:  "none",
		Columns: []cardColumn{{
			Tag:           "column",
			Width:         "weighted",
			Weight:        1,
			VerticalAlign: "top",
			Padding:       "0px 12px",
			Elements: []cardElement{
				{Tag: "hr"},
				managementContentElement(content, cardElement{Tag: "markdown", Margin: "8px 0px"}),
				{Tag: "hr"},
			},
		}},
	}
}

func renderProcessingUpdate(step, mode string) cardElement {
	return cardElement{
		Tag:       "collapsible_panel",
		ElementID: "processing_update",
		Expanded:  mode != config.FeishuThinkingProcessCollapsed,
		Header:    &panelHeader{Title: managementStyledText(sessionMessage("processing"), cardTitle{Tag: "plain_text", TextSize: "normal", TextColor: "grey"}), Padding: "4px 0px 6px 0px"},
		Elements: []cardElement{renderActivitySectionBlock(
			escapeCardMarkdown(truncateAssistantProgress(step, maxActivityStepRunes)),
			"processing_body",
		)},
	}
}

func renderPaddedActivityBlock(content any, elementID, background, padding string) cardElement {
	return cardElement{
		Tag:       "column_set",
		ElementID: elementID,
		FlexMode:  "none",
		Columns: []cardColumn{{
			Tag:             "column",
			Width:           "weighted",
			Weight:          1,
			VerticalAlign:   "top",
			BackgroundStyle: background,
			Padding:         padding,
			Elements:        []cardElement{managementContentElement(content, cardElement{Tag: "markdown"})},
		}},
	}
}

func renderCompletedActivityDetails(view *agentui.ActivityView, subtasks []subtaskProgress) []cardElement {
	var elements []cardElement
	if len(view.Plan) > 0 {
		elements = append(elements, renderActivitySectionBlock(i18n.Render(func(locale i18n.Locale) string { return renderPlan(view.Plan, locale) }), "plan_progress"))
	}
	if len(view.Tools) > 0 {
		now := time.Now()
		elements = append(elements, renderExecutionRecordsBlock(i18n.Render(func(locale i18n.Locale) string { return renderToolSection(view.Tools, now, locale) })))
	}
	if len(subtasks) > 0 {
		elements = append(elements, renderActivitySectionBlock(i18n.Render(func(locale i18n.Locale) string { return renderSubtaskSection(subtasks, locale) }), "collaboration_tasks"))
	}
	return elements
}

func renderSubtaskLine(item subtaskProgress, locales ...i18n.Locale) string {
	identity := ""
	agentType := strings.TrimSpace(item.AgentType)
	if profile := strings.TrimSpace(item.AgentProfile); profile != "" {
		identity = profile
		if agentType != "" {
			identity += " (" + agentType + ")"
		}
	} else if agentType != "" {
		identity = agentType
	}
	line := "- " + escapeCardMarkdown(item.Title) + " · " + sessionSubtaskStatus(item.Status, locales) + " · " + formatElapsed(item.Elapsed, locales...)
	if identity != "" {
		line += "  \n  " + escapeCardMarkdown(identity)
	}
	if detail := subtaskTerminalDetail(item, locales...); detail != "" {
		line += "  \n  " + escapeCardMarkdown(detail)
	}
	return line
}

var (
	subtaskSecretAssignmentRE           = regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|secret)\b(\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	subtaskBearerRE                     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	subtaskSecretTokenRE                = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	subtaskMarkdownHeadingRE            = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	subtaskMarkdownLinkRE               = regexp.MustCompile(`!?\[([^\]]+)\]\([^)]+\)`)
	subtaskMarkdownStrongStarRE         = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	subtaskMarkdownStrongUnderscoreRE   = regexp.MustCompile(`__([^_\n]+)__`)
	subtaskMarkdownStrikeRE             = regexp.MustCompile(`~~([^~\n]+)~~`)
	subtaskMarkdownCodeRE               = regexp.MustCompile("`([^`\\n]+)`")
	subtaskMarkdownDunderIdentifierRE   = regexp.MustCompile(`__[A-Za-z][A-Za-z0-9_]*__`)
	subtaskMarkdownEmphasisStarRE       = regexp.MustCompile(`(?m)(^|[\s(（\[【,，。:：;；!?！？])\*([\p{L}\p{N}](?:[^*\n]*[\p{L}\p{N}])?)\*($|[\s)）\]】,，。:：;；!?！？])`)
	subtaskMarkdownEmphasisUnderscoreRE = regexp.MustCompile(`(?m)(^|[\s(（\[【,，。:：;；!?！？])_([\p{L}\p{N}](?:[^_\n]*[\p{L}\p{N}])?)_($|[\s)）\]】,，。:：;；!?！？])`)
)

func subtaskTerminalDetail(item subtaskProgress, locales ...i18n.Locale) string {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	detail := ""
	switch status {
	case "completed", "done", "success", "succeeded", "partial":
		detail = item.Summary
	case "failed", "error":
		detail = item.Error
	case "timed_out", "timeout":
		detail = sessionCopy(locales, "subtasks.timeout")
	case "cancelled", "canceled", "stopped":
		detail = sessionCopy(locales, "subtasks.cancelled")
	}
	detail = plainSubtaskDetail(detail)
	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return ""
	}
	detail = subtaskSecretAssignmentRE.ReplaceAllString(detail, `${1}${2}[REDACTED]`)
	detail = subtaskBearerRE.ReplaceAllString(detail, "Bearer [REDACTED]")
	detail = subtaskSecretTokenRE.ReplaceAllString(detail, "[REDACTED]")
	return truncateCardText(detail, maxSubtaskDetailRunes)
}

func plainSubtaskDetail(detail string) string {
	detail = subtaskMarkdownDunderIdentifierRE.ReplaceAllStringFunc(detail, func(identifier string) string {
		return `\_\_` + identifier[2:len(identifier)-2] + `\_\_`
	})
	detail = subtaskMarkdownHeadingRE.ReplaceAllString(detail, "")
	detail = subtaskMarkdownLinkRE.ReplaceAllString(detail, "$1")
	detail = subtaskMarkdownStrongStarRE.ReplaceAllString(detail, "$1")
	detail = subtaskMarkdownStrongUnderscoreRE.ReplaceAllString(detail, "$1")
	detail = subtaskMarkdownStrikeRE.ReplaceAllString(detail, "$1")
	detail = subtaskMarkdownCodeRE.ReplaceAllString(detail, "$1")
	detail = subtaskMarkdownEmphasisStarRE.ReplaceAllString(detail, "$1$2$3")
	detail = subtaskMarkdownEmphasisUnderscoreRE.ReplaceAllString(detail, "$1$2$3")
	return strings.ReplaceAll(detail, "*", `\*`)
}

func renderSubtaskSection(subtasks []subtaskProgress, locales ...i18n.Locale) string {
	if len(subtasks) == 0 {
		return ""
	}
	lines := make([]string, 0, min(len(subtasks), maxActivitySubtasks)+1)
	for i, item := range subtasks {
		if i == maxActivitySubtasks {
			lines = append(lines, sessionCopy(locales, "subtasks.more"))
			break
		}
		lines = append(lines, renderSubtaskLine(item, locales...))
	}
	return "**" + sessionCopy(locales, "subtasks.heading", len(subtasks)) + "**\n" + strings.Join(lines, "\n")
}

func renderPlan(entries []agentui.PlanEntry, locales ...i18n.Locale) string {
	done := 0
	lines := make([]string, 0, len(entries)+1)
	for i, entry := range entries {
		if i == maxPlanEntries {
			lines = append(lines, sessionCopy(locales, "plan.more", len(entries)-i))
			break
		}
		marker := "○"
		switch entry.Status {
		case "completed", "done":
			marker = "✓"
			done++
		case "in_progress", "running":
			marker = "●"
		}
		lines = append(lines, marker+" "+strconv.Itoa(i+1)+". "+escapeCardMarkdown(truncateCardText(entry.Content, maxPlanEntryRunes)))
	}
	return "**" + sessionCopy(locales, "plan", done, len(entries)) + "**\n" + strings.Join(lines, "\n")
}

type toolDisplayStatus string

const (
	toolDisplayRunning   toolDisplayStatus = "running"
	toolDisplaySucceeded toolDisplayStatus = "succeeded"
	toolDisplayFailed    toolDisplayStatus = "failed"
)

type toolDisplayRow struct {
	title        string
	sourcePrefix string
	status       toolDisplayStatus
	count        int
	startedAt    time.Time
}

func renderToolRows(tools []agentui.ToolEvent, now time.Time, locales ...i18n.Locale) (active, completed string) {
	var activeLines, completedLines []string
	for _, row := range groupToolDisplayRows(tools, locales...) {
		line := renderToolDisplayTitle(row.title, row.sourcePrefix)
		if row.status == toolDisplayRunning && !row.startedAt.IsZero() {
			line += " · " + formatElapsed(now.Sub(row.startedAt), locales...)
		}
		if row.count > 1 {
			line += " ×" + strconv.Itoa(row.count)
		}
		switch row.status {
		case toolDisplaySucceeded:
			completedLines = append(completedLines, "✓ "+line)
		case toolDisplayFailed:
			activeLines = append(activeLines, "✕ "+line)
		default:
			activeLines = append(activeLines, "● "+line)
		}
	}
	if len(activeLines) > 0 {
		active = "**" + sessionCopy(locales, "records") + "**\n" + strings.Join(activeLines, "\n")
	}
	if len(completedLines) > 0 {
		completed = strings.Join(completedLines, "\n")
	}
	return active, completed
}

func renderToolDisplayTitle(title, sourcePrefix string) string {
	if sourcePrefix == "" || !strings.HasPrefix(title, sourcePrefix) || len(title) == len(sourcePrefix) {
		return escapeCardMarkdown(title)
	}
	return "<font color='grey'>" + escapeCardMarkdown(sourcePrefix) + "</font>" + escapeCardMarkdown(strings.TrimPrefix(title, sourcePrefix))
}

func renderToolSection(tools []agentui.ToolEvent, now time.Time, locales ...i18n.Locale) string {
	active, completed := renderToolRows(tools, now, locales...)
	rows := strings.TrimPrefix(active, "**"+sessionCopy(locales, "records")+"**\n")
	if rows != "" && completed != "" {
		rows += "\n"
	}
	rows += completed
	if rows == "" {
		return ""
	}
	return "**" + sessionCopy(locales, "records.count", len(tools)) + "**\n" + rows
}

func groupToolDisplayRows(tools []agentui.ToolEvent, locales ...i18n.Locale) []toolDisplayRow {
	rows := make([]toolDisplayRow, 0, len(tools))
	rowByKey := make(map[string]int, len(tools))
	for _, tool := range tools {
		title := strings.TrimSpace(tool.Title)
		if title == "" {
			title = strings.TrimSpace(tool.Name)
		}
		title = strings.Join(strings.Fields(title), " ")
		if title == "" {
			title = sessionCopy(locales, "tool")
		}
		sourcePrefix := structuredToolSourcePrefix(tool.Name, title)
		status := toolDisplayStatusFor(tool.Status)
		key := string(status) + "\x00" + title
		if index, ok := rowByKey[key]; ok {
			row := &rows[index]
			row.count++
			if row.sourcePrefix != sourcePrefix {
				row.sourcePrefix = ""
			}
			if !tool.StartedAt.IsZero() && (row.startedAt.IsZero() || tool.StartedAt.Before(row.startedAt)) {
				row.startedAt = tool.StartedAt
			}
			continue
		}
		rowByKey[key] = len(rows)
		rows = append(rows, toolDisplayRow{
			title:        title,
			sourcePrefix: sourcePrefix,
			status:       status,
			count:        1,
			startedAt:    tool.StartedAt,
		})
	}
	return rows
}

func structuredToolSourcePrefix(name, title string) string {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	prefix, suffix, found := strings.Cut(name, "/")
	if !found || strings.TrimSpace(prefix) == "" || strings.TrimSpace(suffix) == "" || title != name {
		return ""
	}
	return prefix + "/"
}

func toolDisplayStatusFor(status string) toolDisplayStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "success", "succeeded":
		return toolDisplaySucceeded
	case "failed", "error":
		return toolDisplayFailed
	default:
		return toolDisplayRunning
	}
}

func usageSummary(raw json.RawMessage, locales ...i18n.Locale) string {
	if len(raw) == 0 {
		return ""
	}
	var usage map[string]any
	if json.Unmarshal(raw, &usage) != nil {
		return ""
	}
	var total int64
	var summary string
	if used, ok := numberValue(usage["used"]); ok {
		total = used
		if size, ok := numberValue(usage["size"]); ok {
			summary = compactNumber(used) + " / " + compactNumber(size) + " tokens"
		} else {
			summary = compactNumber(used) + " tokens"
		}
	} else if t, ok := numberValue(usage["total_tokens"]); ok {
		total = t
		summary = compactNumber(t) + " tokens"
	} else {
		input, inputOK := numberValue(usage["input_tokens"])
		output, outputOK := numberValue(usage["output_tokens"])
		if inputOK || outputOK {
			total = input + output
			summary = compactNumber(input+output) + " tokens"
		}
	}
	// app-server EventUsage carries the model context window; show the
	// consumption ratio next to the total so users can spot context
	// exhaustion in the collapsed title (same data as CLI attach status bar).
	if window, ok := numberValue(usage["context_window"]); ok && window > 0 && total > 0 {
		summary += fmt.Sprintf(" (%d%%)", total*100/window)
	}
	// Consumption is the provider's thread total; the context ratio above
	// remains based on its latest call. Never add repeated usage snapshots.
	var counts []string
	if input, ok := numberValue(usage["cumulative_input_tokens"]); ok {
		text := sessionCopy(locales, "usage.input", compactNumber(input))
		if cached, ok := numberValue(usage["cumulative_cached_input_tokens"]); ok {
			text = sessionCopy(locales, "usage.cached", compactNumber(cached), compactNumber(input))
		}
		counts = append(counts, text)
	}
	if output, ok := numberValue(usage["cumulative_output_tokens"]); ok {
		counts = append(counts, sessionCopy(locales, "usage.output", compactNumber(output)))
	}
	if len(counts) > 0 {
		if summary != "" {
			summary += " · "
		}
		summary += strings.Join(counts, " · ")
	}
	// Speed metrics measured by the app-server proc (TTFT from turn/start to
	// the first streamed delta; output tokens per second since then). Both are
	// optional; missing fields omit the segment instead of showing a placeholder.
	if ttft, ok := numberValue(usage["ttft_ms"]); ok && ttft > 0 {
		if summary != "" {
			summary += " · "
		}
		summary += sessionCopy(locales, "usage.ttft", compactDurationText(ttft))
	}
	if tps, ok := numberValue(usage["tokens_per_sec"]); ok && tps > 0 {
		if summary != "" {
			summary += " · "
		}
		summary += strconv.FormatInt(tps, 10) + " tok/s"
	}
	if cost, ok := usage["cost"].(map[string]any); ok {
		if amount, ok := cost["amount"].(float64); ok {
			currency, _ := cost["currency"].(string)
			costText := strconv.FormatFloat(amount, 'f', 2, 64)
			if currency != "" {
				costText += " " + currency
			}
			if summary != "" {
				summary += " · "
			}
			summary += costText
		}
	}
	return summary
}

func numberValue(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), true
	case json.Number:
		got, err := value.Int64()
		return got, err == nil
	default:
		return 0, false
	}
}

// compactNumber renders a count with an adaptive magnitude suffix so long-running
// cumulative totals stay readable: 999 -> "999", 22800 -> "22.8k",
// 128400000 -> "128.4M", 12345678901 -> "12.3B". The scaled value keeps one
// decimal digit and drops a trailing ".0". Magnitudes are never rounded up a
// step, so 999999 stays "999.9k" rather than becoming "1M"; this keeps the two
// numbers in a "cached/total" pair ordered the same way they are written.
func compactNumber(value int64) string {
	divisor := int64(1)
	suffix := ""
	for _, unit := range compactNumberUnits {
		if value < unit.divisor {
			break
		}
		divisor, suffix = unit.divisor, unit.suffix
	}
	if divisor == 1 {
		return strconv.FormatInt(value, 10)
	}
	whole := value / divisor
	fraction := (value % divisor) / (divisor / 10)
	if fraction > 0 {
		return strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(fraction, 10) + suffix
	}
	return strconv.FormatInt(whole, 10) + suffix
}

// compactNumberUnits lists the magnitude suffixes in ascending order.
var compactNumberUnits = []struct {
	divisor int64
	suffix  string
}{
	{1_000, "k"},
	{1_000_000, "M"},
	{1_000_000_000, "B"},
}

// compactDurationText renders a millisecond count as a short human interval
// for the collapsed activity title.
func compactDurationText(ms int64) string {
	switch {
	case ms >= 60_000:
		return fmt.Sprintf("%dm%ds", ms/60_000, (ms%60_000)/1_000)
	case ms >= 1_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1_000)
	default:
		return strconv.FormatInt(ms, 10) + "ms"
	}
}

func shortActivityText(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func truncateCardText(text string, maxRunes int) string {
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func toolActivityCardTitle(meta toolActivityCardMeta, locales ...i18n.Locale) string {
	status := sessionCopy(locales, "status.running")
	switch meta.Status {
	case "done":
		status = sessionCopy(locales, "status.done")
	case "waiting":
		status = sessionCopy(locales, "status.waiting")
	case "failed":
		status = sessionCopy(locales, "status.failed")
	case "stopped":
		status = sessionCopy(locales, "status.stopped")
	}
	now := meta.Now
	if now.IsZero() {
		now = time.Now()
	}
	identity := strings.TrimSpace(meta.AgentProfile)
	identity = displayProfileRefWithoutRevision(identity)
	if identity == "" {
		identity = strings.TrimSpace(meta.AgentType)
	}
	identityText := ""
	if identity != "" {
		identityText = " · " + identity
	}
	modelText := ""
	if model := strings.TrimSpace(meta.EffectiveModel); model != "" {
		modelText = " · " + sessionCopy(locales, "model", truncateCardText(managementDefaultText(managementRuntimeModelLabel(model)), 80))
	}
	if meta.QuotaBorrowed {
		modelText += " · " + sessionCopy(locales, "quota.shared_pool")
	}
	usageText := ""
	if meta.Activity != nil {
		if usage := usageSummary(meta.Activity.UsageJSON, locales...); usage != "" {
			usageText = " · " + usage
		}
	}
	if meta.Status == "running" || meta.Status == "" {
		if meta.Activity != nil && meta.Activity.RetryCount > 0 {
			elapsed := time.Duration(0)
			if !meta.Activity.StartedAt.IsZero() {
				elapsed = now.Sub(meta.Activity.StartedAt)
				if elapsed < 0 {
					elapsed = 0
				}
			}
			retryKey := "retry.provider"
			if meta.Activity.RetryCode == "rate_limit_retry" {
				retryKey = "retry.rate_limit"
			}
			return sessionCopy(locales, "status.model") + identityText + modelText + " · " + sessionCopy(locales, retryKey, meta.Activity.RetryCount, formatElapsed(elapsed, locales...))
		}
		if meta.Activity != nil && !meta.Activity.LastActivityAt.IsZero() {
			silentFor := now.Sub(meta.Activity.LastActivityAt)
			possiblyQueued := distributionActivityPossiblyQueued(meta.AgentType, meta.Activity)
			if possiblyQueued && silentFor < turnStatusSilentDelay {
				return sessionCopy(locales, "status.model") + identityText + modelText + " · " + sessionCopy(locales, "queue.submitted")
			}
			switch {
			case silentFor >= time.Minute:
				if possiblyQueued {
					return sessionCopy(locales, "status.model") + identityText + modelText + " · " + sessionCopy(locales, "queue.long", formatElapsed(silentFor, locales...))
				}
				return sessionCopy(locales, "status.still") + identityText + modelText + " · " + sessionCopy(locales, "status.silent", formatElapsed(silentFor, locales...)) + usageText
			case silentFor >= turnStatusSilentDelay:
				if possiblyQueued {
					return sessionCopy(locales, "status.model") + identityText + modelText + " · " + sessionCopy(locales, "queue.waiting", formatElapsed(silentFor, locales...))
				}
				return sessionCopy(locales, "status.running") + identityText + modelText + " · " + sessionCopy(locales, "status.recent", formatElapsed(silentFor, locales...)) + usageText
			}
		}
	}
	elapsed := ""
	if meta.Activity != nil && !meta.Activity.StartedAt.IsZero() {
		end := now
		if !meta.Activity.FinishedAt.IsZero() {
			end = meta.Activity.FinishedAt
		}
		elapsed = " · " + formatElapsed(end.Sub(meta.Activity.StartedAt), locales...)
	}
	return status + identityText + modelText + elapsed + usageText
}

func toolActivityDisplayTitle(meta toolActivityCardMeta, locales ...i18n.Locale) string {
	title := toolActivityCardTitle(meta, locales...)
	if len(meta.Subtasks) == 0 {
		return title
	}
	if meta.Status == "done" || meta.Status == "waiting" || meta.Status == "failed" || meta.Status == "stopped" {
		return title + " · " + sessionCopy(locales, "subtasks", len(meta.Subtasks))
	}
	if meta.Status == "running" || meta.Status == "" {
		if count := activeSubtaskCount(meta.Subtasks); count > 0 {
			return title + " · " + sessionCopy(locales, "subtasks.active", count)
		}
		return title + " · " + sessionCopy(locales, "subtasks", len(meta.Subtasks))
	}
	return title
}

func toolActivityCardContent(question, followup, text string, locales ...i18n.Locale) string {
	text = strings.TrimSpace(text)
	question = strings.TrimSpace(question)
	followup = strings.TrimSpace(followup)
	if question == "" {
		return text
	}
	prefix := "Q: " + question
	if followup != "" {
		prefix += "\n" + sessionCopy(locales, "followup.plain", followup)
	}
	if text == "" {
		return prefix
	}
	return prefix + "\n\n" + text
}

func toolActivityQuestionTitle(text string) string {
	text = stripAmuxGuidance(text)
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if idx := strings.Index(text, "📱 来自飞书: "); idx >= 0 {
		text = strings.TrimSpace(text[idx+len("📱 来自飞书: "):])
	}
	if strings.HasPrefix(text, "[Amux Feishu Thread Mode]") {
		const userRequestMarker = "\n[User request]\n"
		if idx := strings.LastIndex(text, userRequestMarker); idx >= 0 {
			text = strings.TrimSpace(text[idx+len(userRequestMarker):])
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxRunes = 240
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return text
}

func markdownLitePostBlocks(text string) [][]postElement {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks [][]postElement
	var para []string
	inCode := false
	codeLang := "plain"
	var codeLines []string
	var tableLines []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(para, "\n"))
		if body != "" {
			blocks = append(blocks, parseInlineElements(body))
		}
		para = nil
	}
	flushCode := func() {
		blocks = append(blocks, []postElement{{
			Tag:      "code_block",
			Language: codeLang,
			Text:     strings.TrimRight(strings.Join(codeLines, "\n"), "\n"),
		}})
		codeLines = nil
		codeLang = "plain"
	}
	flushTable := func() {
		if len(tableLines) == 0 {
			return
		}
		for _, tableLine := range tableLines {
			blocks = append(blocks, parseInlineElements(tableLine))
		}
		tableLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				flushCode()
				inCode = false
				continue
			}
			flushPara()
			inCode = true
			codeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			if codeLang == "" {
				codeLang = "plain"
			}
			continue
		}
		if inCode {
			codeLines = append(codeLines, line)
			continue
		}
		if isMarkdownTableLine(trimmed) {
			flushPara()
			tableLines = append(tableLines, trimmed)
			continue
		}
		flushTable()
		if trimmed == "" || trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushPara()
			continue
		}
		if heading, ok := markdownHeading(trimmed); ok {
			flushPara()
			blocks = append(blocks, []postElement{{Tag: "text", Text: heading, Style: []string{"bold"}}})
			continue
		}
		if item, ok := markdownListItem(trimmed); ok {
			flushPara()
			blocks = append(blocks, parseInlineElements("• "+item))
			continue
		}
		if after, ok := strings.CutPrefix(trimmed, "> "); ok {
			flushPara()
			blocks = append(blocks, parseInlineElements(after))
			continue
		}
		para = append(para, line)
	}
	if inCode {
		flushCode()
	}
	flushTable()
	flushPara()
	return blocks
}

func isMarkdownTableLine(line string) bool {
	return strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") && strings.Count(line, "|") >= 2
}

// parseInlineElements splits a line of text into postElements, handling
// **bold** markers and stripping inline `code` backticks.
func parseInlineElements(line string) []postElement {
	line = stripInlineCode(line)
	parts := strings.Split(line, "**")
	if len(parts) == 1 {
		return []postElement{{Tag: "text", Text: line}}
	}
	var elements []postElement
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i%2 == 1 {
			elements = append(elements, postElement{Tag: "text", Text: part, Style: []string{"bold"}})
		} else {
			elements = append(elements, postElement{Tag: "text", Text: part})
		}
	}
	if len(elements) == 0 {
		return []postElement{{Tag: "text", Text: line}}
	}
	return elements
}

// stripInlineCode removes backtick-quoted inline code markers from text,
// since Feishu post format has no inline-code element.
func stripInlineCode(s string) string {
	var out strings.Builder
	inBacktick := false
	for _, r := range s {
		if r == '`' {
			inBacktick = !inBacktick
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func markdownHeading(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[level+1:]), true
}

func markdownListItem(line string) (string, bool) {
	if len(line) < 3 || line[1] != ' ' {
		return "", false
	}
	switch line[0] {
	case '-', '*':
		return strings.TrimSpace(line[2:]), true
	default:
		return "", false
	}
}

// jsonQuote returns the JSON string encoding of s (with surrounding quotes).
func jsonQuote(s string) string {
	bs, _ := json.Marshal(s)
	return string(bs)
}

// renderPlaceholderStatus renders the immediate-feedback line of a turn card
// that has not produced output yet. Terminal states replace it with the
// failure/interrupt outcome so the placeholder never hangs.
func renderPlaceholderStatus(meta toolActivityCardMeta) (cardElement, bool) {
	if !meta.Placeholder {
		return cardElement{}, false
	}
	var text i18n.Text
	switch meta.Status {
	case "running", "":
		elapsed := time.Duration(0)
		if meta.Activity != nil && !meta.Activity.StartedAt.IsZero() {
			now := meta.Now
			if now.IsZero() {
				now = time.Now()
			}
			elapsed = now.Sub(meta.Activity.StartedAt)
		}
		if elapsed >= time.Second {
			text = i18n.Render(func(locale i18n.Locale) string {
				locales := []i18n.Locale{locale}
				return sessionCopy(locales, "placeholder.processing.elapsed", formatElapsed(elapsed, locales...))
			})
		} else {
			text = sessionMessage("placeholder.processing")
		}
	case "failed":
		if errText := strings.TrimSpace(meta.PlaceholderError); errText != "" {
			text = sessionMessage("placeholder.failed", errText)
		} else {
			text = i18n.Render(func(locale i18n.Locale) string {
				locales := []i18n.Locale{locale}
				return sessionCopy(locales, "placeholder.failed", sessionCopy(locales, "status.failed"))
			})
		}
	case "stopped":
		text = sessionMessage("placeholder.stopped")
	default:
		return cardElement{}, false
	}
	element := managementContentElement(text, cardElement{Tag: "markdown"})
	element.ElementID = "placeholder_status"
	return element, true
}
