package acptui

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// minPartialLines is the minimum number of lines always reserved above the
// separator for streaming (partial) output.
const minPartialLines = 3
const maxTimelineLines = 200

// PanelConfig configures the Panel rendering.
type PanelConfig struct {
	SessionName string
	AgentType   string
	TargetName  string
}

// TimelineEvent is one compact row in the attach activity panel.
type TimelineEvent struct {
	Time         string
	Source       string
	Text         string
	Pending      bool
	PendingIndex int
}

// Panel is the bottom-of-screen fixed panel that shows streaming output,
// a separator, the input area, and a status bar. It combines the state
// tracking (turn locking, status) with the rendering logic.
type Panel struct {
	mu         sync.Mutex
	width      int
	height     int
	session    string
	agentType  string
	target     string
	status     string
	queueLen   int
	sources    map[string]bool
	editor     *LineEditor
	idleTimer  *DelayedTimer
	turnLocked bool
	statusLive bool
	usage      *UsageStats

	// renderer state
	panelLines         int
	partialBuf         []byte
	toolDigest         *toolDigest
	lastTimelineSource string
	lastAgentStart     int
	lastAgentEvent     *TimelineEvent
	permission         *PermissionRequest
}

type toolDigest struct {
	time   string
	total  int
	counts map[string]int
	order  []string
	last   string
}

// NewPanel creates a Panel with the given configuration and editor.
func NewPanel(cfg PanelConfig, editor *LineEditor) *Panel {
	p := &Panel{
		session:   cfg.SessionName,
		agentType: cfg.AgentType,
		target:    cfg.TargetName,
		editor:    editor,
		status:    "idle",
		sources:   map[string]bool{"cli": true},
	}
	p.idleTimer = NewDelayedTimer(220*time.Millisecond, func() {
		p.setStatus("idle")
	})
	return p
}

// Close stops background timers.
func (p *Panel) Close() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
}

// SetSize updates the terminal dimensions.
func (p *Panel) SetSize(width, height int) {
	p.mu.Lock()
	p.width = width
	p.height = height
	p.mu.Unlock()
}

func (p *Panel) AddTimelineEvent(ev TimelineEvent) {
	if strings.TrimSpace(ev.Text) == "" {
		if ev.Source == "agent" && !ev.Pending && p.lastTimelineSource == "agent" {
			p.mu.Lock()
			if p.mergeLastAgentEventLocked(ev) {
				p.partialBuf = trimTimelineBuffer(p.partialBuf)
			}
			p.mu.Unlock()
		}
		return
	}
	p.mu.Lock()
	if p.sources == nil {
		p.sources = make(map[string]bool)
	}
	if ev.Source != "" {
		p.sources[ev.Source] = true
	}
	renderWidth := p.width - 2
	if renderWidth < 20 {
		renderWidth = 80
	}
	if ev.Source == "tool" && !ev.Pending {
		p.addToolEventLocked(ev)
		p.lastTimelineSource = "tool"
		p.partialBuf = trimTimelineBuffer(p.partialBuf)
		p.mu.Unlock()
		return
	}
	p.toolDigest = nil
	if ev.Source == "agent" && !ev.Pending && p.lastTimelineSource == "agent" && p.mergeLastAgentEventLocked(ev) {
		p.partialBuf = trimTimelineBuffer(p.partialBuf)
		p.mu.Unlock()
		return
	}
	start := len(p.partialBuf)
	p.partialBuf = append(p.partialBuf, []byte(formatTimelineEvent(ev, renderWidth))...)
	p.partialBuf = append(p.partialBuf, '\n')
	p.lastTimelineSource = ev.Source
	if ev.Source == "agent" && !ev.Pending {
		p.lastAgentStart = start
		copy := ev
		p.lastAgentEvent = &copy
	} else {
		p.lastAgentEvent = nil
	}
	p.partialBuf = trimTimelineBuffer(p.partialBuf)
	p.mu.Unlock()
}

func (p *Panel) addToolEventLocked(ev TimelineEvent) {
	name, last := summarizeToolEventText(ev.Text)
	if p.toolDigest == nil {
		p.toolDigest = &toolDigest{
			time:   ev.Time,
			counts: make(map[string]int),
		}
	}
	d := p.toolDigest
	d.total++
	if _, ok := d.counts[name]; !ok {
		d.order = append(d.order, name)
	}
	d.counts[name]++
	d.last = last

	if d.total == 1 {
		p.partialBuf = append(p.partialBuf, []byte(formatTimelineEvent(ev, 80))...)
		p.partialBuf = append(p.partialBuf, '\n')
		return
	}
	p.replaceLastTimelineLineLocked(formatToolDigest(d))
}

func (p *Panel) replaceLastTimelineLineLocked(line string) {
	buf := strings.TrimRight(string(p.partialBuf), "\n")
	idx := strings.LastIndex(buf, "\n")
	if idx < 0 {
		p.partialBuf = []byte(line + "\n")
		return
	}
	p.partialBuf = []byte(buf[:idx+1] + line + "\n")
}

func (p *Panel) mergeLastAgentEventLocked(ev TimelineEvent) bool {
	text := strings.Trim(ev.Text, "\r")
	if (strings.TrimSpace(text) == "" && !strings.Contains(text, "\n")) || len(p.partialBuf) == 0 || p.lastAgentEvent == nil {
		return false
	}
	p.lastAgentEvent.Text += text
	if p.lastAgentStart < 0 || p.lastAgentStart > len(p.partialBuf) {
		return false
	}
	renderWidth := p.width - 2
	if renderWidth < 20 {
		renderWidth = 80
	}
	p.partialBuf = append(p.partialBuf[:p.lastAgentStart], []byte(formatTimelineEvent(*p.lastAgentEvent, renderWidth)+"\n")...)
	return true
}

// AddOutput notifies the panel of new output data for status tracking.
// If shouldTriggerStatus is false, the output won't affect the status
// indicator (useful for filtering source frames).
func (p *Panel) SetPermissionRequest(req *PermissionRequest) {
	if req == nil {
		return
	}
	p.mu.Lock()
	copyReq := *req
	copyReq.Options = append([]PermissionOption(nil), req.Options...)
	p.permission = &copyReq
	p.mu.Unlock()
	p.AddTimelineEvent(TimelineEvent{Time: time.Now().Format("15:04"), Source: "permission", Text: formatPermissionRequest(req)})
}

func formatPermissionRequest(req *PermissionRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	title := req.Title
	if title == "" {
		title = "Agent requests permission"
	}
	b.WriteString(title)
	if req.RequestID != "" {
		b.WriteString("\nrequest: ")
		b.WriteString(req.RequestID)
	}
	for i, opt := range req.Options {
		label := opt.Label
		if label == "" {
			label = opt.OptionId
		}
		b.WriteString(fmt.Sprintf("\n  /permit %d  %s", i+1, label))
	}
	b.WriteString("\n也可使用 /allow 或 /reject 选择常见选项。")
	return b.String()
}

func (p *Panel) AddOutput(data []byte, shouldTriggerStatus bool) {
	text := normalizePanelOutput(data)
	if text == "" {
		return
	}
	if shouldTriggerStatus {
		p.setStatus("running")
		if p.idleTimer != nil {
			p.idleTimer.Schedule()
		}
	}
}

// LockTurn prevents new input. Call when the user sends a prompt.
func (p *Panel) LockTurn() {
	p.mu.Lock()
	p.turnLocked = true
	p.mu.Unlock()
}

// UnlockTurn re-enables input. Call when the agent turn completes.
func (p *Panel) UnlockTurn() {
	p.mu.Lock()
	p.turnLocked = false
	p.mu.Unlock()
}

// Blocked reports whether input is locked (agent turn in progress).
func (p *Panel) Blocked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnLocked
}

// SetStatus sets the status indicator text.
func (p *Panel) SetStatus(status string) {
	p.setStatus(status)
}

// SetUsage records the latest token usage for the status-bar context
// indicator. Passing nil clears it.
func (p *Panel) SetUsage(u *UsageStats) {
	if u == nil {
		p.mu.Lock()
		p.usage = nil
		p.mu.Unlock()
		return
	}
	copyU := *u
	p.mu.Lock()
	p.usage = &copyU
	p.mu.Unlock()
}

// CurrentUsage returns a copy of the latest recorded usage, or nil.
func (p *Panel) CurrentUsage() *UsageStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.usage == nil {
		return nil
	}
	copyU := *p.usage
	return &copyU
}

func (p *Panel) setStatus(status string) {
	p.mu.Lock()
	p.status = status
	p.mu.Unlock()
}

// RenderPanel redraws the panel without new output.
func (p *Panel) RenderPanel() []byte {
	return p.replacePanel(nil)
}

func (p *Panel) RenderEvent(ev OutputEvent, data []byte, shouldTriggerStatus bool) []byte {
	p.AddOutput(data, shouldTriggerStatus)
	for _, item := range timelineEventsFromOutput(ev, data) {
		p.AddTimelineEvent(item)
	}
	return p.replacePanel(nil)
}

func (p *Panel) replacePanel(prefix []byte) []byte {
	var buf bytes.Buffer
	buf.Write(clearPreviousPanel(p.panelLines))
	if len(prefix) > 0 {
		buf.Write(prefix)
		if !bytes.HasSuffix(prefix, []byte("\n")) && !bytes.HasSuffix(prefix, []byte("\r")) {
			buf.WriteString("\r\n")
		}
	}
	partial := string(p.partialBuf)
	panel := p.snapshot(partial)
	buf.Write(panel)
	p.panelLines = p.panelLinesCount(partial)
	return buf.Bytes()
}

// snapshot renders the bottom panel content.
func (p *Panel) snapshot(partial string) []byte {
	p.mu.Lock()
	width := p.width
	status := p.status
	session := p.session
	agentType := p.agentType
	target := p.target
	queueLen := p.queueLen
	sourceText := panelSources(p.sources)
	editor := p.editor
	blocked := p.turnLocked
	var usage *UsageStats
	if p.usage != nil {
		copyU := *p.usage
		usage = &copyU
	}
	p.mu.Unlock()

	if width <= 0 {
		width = 80
	}

	inputText := ""
	if editor != nil {
		inputText = editor.Text()
	}
	innerWidth := width - 2
	if innerWidth < 10 {
		innerWidth = 10
	}
	inputLines := wrapInputLines(inputText, innerWidth)
	if len(inputLines) == 0 {
		inputLines = []string{"> "}
	}
	cursorWidth := DisplayWidth(inputLines[len(inputLines)-1])
	if strings.TrimSpace(inputText) == "" {
		inputLines = []string{"> 输入消息，/detach，/interrupt，/queue，/cwd"}
		cursorWidth = DisplayWidth("> ")
		for i, line := range inputLines {
			inputLines[i] = "\033[2m" + line + "\033[0m"
		}
	}
	if blocked {
		for i, line := range inputLines {
			inputLines[i] = "\033[2m" + line + "\033[0m"
		}
	}

	var buf strings.Builder
	buf.WriteString(boxTop(width, fmt.Sprintf(" amux attach: %s @ %s [%s] ", agentType, displayTarget(target), session)))
	buf.WriteString("\r\n")
	stateLine := fmt.Sprintf("state: %s | queue: %d | source: %s", status, queueLen, sourceText)
	if ctxText := usageContextText(usage); ctxText != "" {
		stateLine += " | " + ctxText
	}
	buf.WriteString(boxLine(width, stateLine))
	buf.WriteString("\r\n")
	buf.WriteString(boxSep(width))
	buf.WriteString("\r\n")

	var partialLines []string
	if partial != "" {
		partialText := normalizePanelOutput([]byte(partial))
		if partialText != "" {
			for _, line := range strings.Split(partialText, "\n") {
				line = displayMarkdownLine(line)
				partialLines = append(partialLines, WrapPlainLine(line, innerWidth)...)
			}
		}
	}
	for len(partialLines) < minPartialLines {
		partialLines = append(partialLines, "")
	}
	for _, line := range partialLines {
		buf.WriteString(boxLine(width, line))
		buf.WriteString("\r\n")
	}

	buf.WriteString(boxSep(width))
	buf.WriteString("\r\n")
	for i, line := range inputLines {
		if i > 0 {
			buf.WriteString("\r\n")
		}
		buf.WriteString(boxLine(width, line))
	}
	buf.WriteString("\r\n")
	buf.WriteString(boxBottom(width))
	buf.WriteString("\033[?25h\r\033[A")
	if cursorWidth > 0 {
		buf.WriteString(fmt.Sprintf("\033[%dC", cursorWidth+1))
	}
	return []byte(buf.String())
}

// panelLinesCount returns the number of terminal lines the panel occupies.
func (p *Panel) panelLinesCount(partial string) int {
	p.mu.Lock()
	width := p.width
	editor := p.editor
	p.mu.Unlock()
	if width <= 0 {
		width = 80
	}
	inputText := ""
	if editor != nil {
		inputText = editor.Text()
	}
	innerWidth := width - 2
	if innerWidth < 10 {
		innerWidth = 10
	}
	lines := len(wrapInputLines(inputText, innerWidth)) + 5
	if strings.TrimSpace(inputText) == "" {
		lines = 6
	}

	partialCount := 0
	if partial != "" {
		partialText := normalizePanelOutput([]byte(partial))
		if partialText != "" {
			for _, line := range strings.Split(partialText, "\n") {
				line = displayMarkdownLine(line)
				partialCount += len(WrapPlainLine(line, innerWidth))
			}
		}
	}
	if partialCount < minPartialLines {
		partialCount = minPartialLines
	}
	lines += partialCount
	return lines
}

func clearPreviousPanel(lines int) []byte {
	if lines <= 0 {
		return nil
	}
	var buf strings.Builder
	buf.WriteString("\033[1B\r\033[2K")
	for i := 1; i < lines; i++ {
		buf.WriteString("\033[1A\r\033[2K")
	}
	return []byte(buf.String())
}

func normalizePanelOutput(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	text := normalizePanelOutputPreserve(data)
	text = strings.TrimRight(text, "\n")
	return text
}

func normalizePanelOutputPreserve(data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func displayMarkdownLine(line string) string {
	if strings.HasPrefix(strings.TrimSpace(line), "```") {
		return line
	}
	line = stripInlineCode(line)
	line = strings.ReplaceAll(line, "**", "")
	return line
}

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

func renderAgentMarkdown(text string, width int) (string, bool) {
	if !looksLikeMarkdown(text) {
		return text, false
	}
	text = repairMarkdownBoundaries(text)
	if width < 20 {
		width = 80
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(styles.NoTTYStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text, false
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text, false
	}
	out = strings.Trim(out, "\n")
	if out == "" {
		return text, false
	}
	return out, true
}

func repairMarkdownBoundaries(text string) string {
	text = strings.ReplaceAll(text, "---##", "---\n##")
	text = strings.ReplaceAll(text, "||---", "|\n|---")
	text = strings.ReplaceAll(text, "---||", "---|\n|")
	text = strings.ReplaceAll(text, "|| ", "|\n| ")
	return splitInlineHeadings(text)
}

func splitInlineHeadings(text string) string {
	var out strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '#' {
			out.WriteRune(runes[i])
			continue
		}
		j := i
		for j < len(runes) && runes[j] == '#' {
			j++
		}
		level := j - i
		if level > 6 {
			out.WriteRune(runes[i])
			continue
		}
		prev := rune('\n')
		if i > 0 {
			prev = runes[i-1]
		}
		if prev != '\n' {
			out.WriteByte('\n')
		}
		out.WriteString(strings.Repeat("#", level))
		if j < len(runes) && runes[j] != ' ' && runes[j] != '\n' {
			out.WriteByte(' ')
		}
		i = j - 1
	}
	return out.String()
}

func looksLikeMarkdown(text string) bool {
	trimmed := strings.TrimSpace(text)
	if strings.Contains(trimmed, "\n") {
		return true
	}
	for _, marker := range []string{"# ", "## ", "### ", "|", "```", "- ", "* ", "> "} {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	return false
}

func trimTimelineBuffer(buf []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) <= maxTimelineLines {
		return buf
	}
	lines = lines[len(lines)-maxTimelineLines:]
	return []byte(strings.Join(lines, "\n") + "\n")
}

func wrapInputLines(text string, width int) []string {
	if width < 4 {
		width = 4
	}
	lines := WrapWithPrefix("> ", text, width, "  ")
	if len(lines) == 0 {
		return []string{"> "}
	}
	return lines
}

func formatTimelineEvent(ev TimelineEvent, width int) string {
	source := ev.Source
	if source == "" {
		source = "agent"
	}
	label := source
	if ev.Pending {
		if ev.PendingIndex > 0 {
			label = fmt.Sprintf("%s pending #%d", source, ev.PendingIndex)
		} else {
			label = source + " pending"
		}
	}
	text := strings.TrimSpace(ev.Text)
	if source == "agent" {
		text = strings.Trim(ev.Text, "\r")
		if rendered, ok := renderAgentMarkdown(text, width); ok {
			text = rendered
		}
		if strings.Contains(text, "\n") {
			return fmt.Sprintf("[%s] %s %s\n%s", ev.Time, sourceIcon(source), label, text)
		}
	}
	return fmt.Sprintf("[%s] %s %s: %s", ev.Time, sourceIcon(source), label, text)
}

func formatToolDigest(d *toolDigest) string {
	var counts []string
	for _, name := range d.order {
		counts = append(counts, fmt.Sprintf("%s x%d", name, d.counts[name]))
	}
	return fmt.Sprintf("[%s] 🔧 tool: tools: %d calls · %s · last: %s", d.time, d.total, strings.Join(counts, ", "), d.last)
}

func summarizeToolEventText(text string) (name, last string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "tool", "tool"
	}
	fields := strings.Fields(text)
	name = fields[0]
	last = text
	if len(fields) > 1 && strings.Contains(fields[1], "/") {
		last = name + " " + filepath.Base(fields[1])
	}
	return name, last
}

// formatStructuredToolEvent renders a structured ToolEvent into the compact
// text form the tool timeline expects: "<name> <title-or-path> <status>". This
// lets addToolEventLocked/summarizeToolEventText aggregate structured tool
// events the same way they aggregate the legacy "🔧 工具开始:" text, without
// text-prefix matching. See agent-adapter product model v0.1.32.
func formatStructuredToolEvent(te *ToolEvent) string {
	title := te.Title
	if title == "" {
		title = te.Name
	}
	// If the title carries a path (file changes), shorten to base so the digest
	// stays compact, matching summarizeToolEventText's path handling.
	if strings.Contains(title, "/") {
		title = filepath.Base(title)
	}
	status := te.Status
	switch status {
	case "running", "":
		status = "running"
	case "completed":
		status = "done"
	case "failed":
		status = "failed"
	}
	return fmt.Sprintf("%s %s %s", te.Name, title, status)
}

func timelineEventsFromOutput(ev OutputEvent, data []byte) []TimelineEvent {
	text := normalizePanelOutputPreserve(data)
	if text == "" {
		return nil
	}
	source := ev.Source
	if source == "" {
		source = "agent"
	}
	if ev.Input {
		switch source {
		case "lark", "cli", "desktop":
		default:
			source = "cli"
		}
	}
	if tool, ok := toolTimelineEvent(strings.TrimRight(text, "\n")); ok {
		source = "tool"
		text = tool
	}
	now := time.Now().Format("15:04")
	if source == "agent" {
		if text == "" {
			return nil
		}
		return []TimelineEvent{{Time: now, Source: source, Text: text}}
	}
	text = normalizePanelOutput([]byte(text))
	var out []TimelineEvent
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, TimelineEvent{Time: now, Source: source, Text: line})
	}
	return out
}

func toolTimelineEvent(text string) (string, bool) {
	line := strings.TrimSpace(text)
	for _, prefix := range []string{
		"🔧 工具开始: ",
		"✅ 工具完成: ",
		"❌ 工具失败: ",
	} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	if strings.HasPrefix(line, "🔄 工具状态(") {
		if _, after, ok := strings.Cut(line, ": "); ok {
			return strings.TrimSpace(after), true
		}
	}
	return "", false
}

func sourceIcon(source string) string {
	switch source {
	case "lark":
		return "📱"
	case "desktop":
		return "🖥️"
	case "cli":
		return "💻"
	case "tool":
		return "🔧"
	default:
		return "🤖"
	}
}

func panelSources(sources map[string]bool) string {
	hasLark := sources["lark"]
	hasCLI := sources["cli"]
	hasDesktop := sources["desktop"]
	var active []string
	if hasLark {
		active = append(active, "lark")
	}
	if hasDesktop {
		active = append(active, "desktop")
	}
	if hasCLI {
		active = append(active, "cli")
	}
	if len(active) > 0 {
		return strings.Join(active, "+")
	}
	return "-"
}

func displayTarget(target string) string {
	if strings.TrimSpace(target) == "" {
		return "-"
	}
	return target
}

// usageContextText renders the status-bar context indicator. Empty when no
// usage has been reported.
func usageContextText(u *UsageStats) string {
	if u == nil || u.TotalTokens <= 0 {
		return ""
	}
	if u.ContextWindow > 0 {
		pct := u.TotalTokens * 100 / u.ContextWindow
		return fmt.Sprintf("ctx: %s/%s (%d%%)", compactTokenCount(u.TotalTokens), compactTokenCount(u.ContextWindow), pct)
	}
	return "ctx: " + compactTokenCount(u.TotalTokens)
}

// usageTimelineText renders the turn-end usage detail row.
func usageTimelineText(u *UsageStats) string {
	if u == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("in %s", compactTokenCount(u.InputTokens)),
		fmt.Sprintf("out %s", compactTokenCount(u.OutputTokens)),
	}
	if u.CachedInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cached %s", compactTokenCount(u.CachedInputTokens)))
	}
	if u.CacheCreationInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache-write %s", compactTokenCount(u.CacheCreationInputTokens)))
	}
	if u.ReasoningOutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("reasoning %s", compactTokenCount(u.ReasoningOutputTokens)))
	}
	if u.TTFTMs > 0 {
		parts = append(parts, fmt.Sprintf("ttft %s", compactDuration(u.TTFTMs)))
	}
	if u.TokensPerSec > 0 {
		parts = append(parts, fmt.Sprintf("%d tok/s", u.TokensPerSec))
	}
	line := "token usage: " + strings.Join(parts, " · ")
	if ctx := usageContextText(u); ctx != "" {
		line += " · " + ctx
	}
	return line
}

// compactDuration renders a millisecond count as a short human interval.
func compactDuration(ms int64) string {
	switch {
	case ms >= 60_000:
		return fmt.Sprintf("%dm%ds", ms/60_000, (ms%60_000)/1_000)
	case ms >= 1_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1_000)
	default:
		return fmt.Sprintf("%dms", ms)
	}
}

// compactTokenCount renders a token count with k/M suffixes.
func compactTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func boxTop(width int, title string) string {
	if width < 2 {
		width = 2
	}
	inner := width - 2
	title = "─" + title
	title = TruncateToWidth(title, inner)
	return "┌" + padRight(title, inner, "─") + "┐"
}

func boxSep(width int) string {
	if width < 2 {
		width = 2
	}
	return "├" + strings.Repeat("─", width-2) + "┤"
}

func boxBottom(width int) string {
	if width < 2 {
		width = 2
	}
	return "└" + strings.Repeat("─", width-2) + "┘"
}

func boxLine(width int, text string) string {
	if width < 2 {
		width = 2
	}
	inner := width - 2
	text = TruncateToWidth(text, inner)
	return "│" + padRight(text, inner, " ") + "│"
}

func padRight(text string, width int, pad string) string {
	missing := width - DisplayWidth(text)
	if missing <= 0 {
		return text
	}
	return text + strings.Repeat(pad, missing)
}
