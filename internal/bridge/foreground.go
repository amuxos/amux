package bridge

import (
	"regexp"
	"strings"

	"github.com/amuxos/amux/internal/config"
)

// foregroundInstructions is the short coordination protocol for a Feishu
// foreground session (singleton or thread/topic chat). It is delivered out of
// band as session system/developer instructions (see sessionSystemInstructions)
// so it applies to every turn; detailed tool usage lives in the amux MCP tool
// descriptions (instance_spawn/steer/followup/interrupt/wait).
const foregroundInstructions = "[amux 前台协调说明：你是飞书前台会话，负责与用户对话和调度。" +
	"任务是否耗时、是否交给后台，由你按任务规模自行判断：需要长时间执行或大量工具调用的，用 amux.instance_spawn 交给后台实例，简短告知用户后继续响应新消息；短问题直接回答。" +
	"后台任务结束时系统会自动送来一条以「后台任务完成」开头的通知（不是用户消息，用户看不到），无需轮询；收到后用自然语言向用户汇报结论，不要复述实例或任务 id；失败时由你决定重试、汇报还是询问用户。" +
	"你执行中被插入的新消息，请另起一段单独回答，与正在进行的任务回复明确区分，并保留已完成的进度。" +
	"各工具的具体用法见其说明。]"

// sessionSystemInstructions is the out-of-band instruction block for a Feishu
// chat session: the file-delivery note plus the foreground protocol. It is
// empty when steer_foreground is off; the file-delivery note then keeps its
// earlier per-turn inline delivery.
func (b *Bridge) sessionSystemInstructions(chat *LarkChat) string {
	if b == nil || chat == nil || !b.steerForeground.Load() || chat.RootMsgID == "" {
		return ""
	}
	return strings.TrimSpace(artifactInstructions) + "\n\n" + foregroundInstructions
}

// sessionStartInstructions is the instruction block applied from a new Feishu
// session's first launch (steer_foreground on only).
func (b *Bridge) sessionStartInstructions() string {
	if b == nil || !b.steerForeground.Load() {
		return ""
	}
	return strings.TrimSpace(artifactInstructions) + "\n\n" + foregroundInstructions
}

// setSteerForeground applies the experimental steer/foreground switch live.
// The next submit of each chat sends the matching session instructions: the
// runtime persists them for the next provider launch and, until the running
// launch carries them, appends them inline. Turning the switch off clears them
// for future launches; a running provider keeps its launch-time system prompt
// until the session restarts.
func (b *Bridge) setSteerForeground(on bool) {
	if b == nil {
		return
	}
	b.steerForeground.Store(on)
	config.SetLiveSteerForeground(on)
}

// runningExperimentsSettings is the startup experiment state plus the live
// steer/foreground switch, which the bridge owns at runtime.
func (b *Bridge) runningExperimentsSettings() config.ExperimentsSettings {
	running := config.GetExperimentsSettings()
	if b != nil {
		running.SteerForeground = b.steerForeground.Load()
		running.ShowBackgroundTasks = b.showBackgroundTasks.Load()
		running.MentionTurnInitiator = b.mentionTurnInitiator.Load()
		running.ReplyEndNotification = b.currentReplyEndNotification()
		running.ModelRecoveryQueueWaitSeconds = b.currentModelRecoveryQueueWaitSeconds()
	}
	return running
}

// amuxGuidancePattern matches the bracketed amux protocol blocks the bridge
// appends to model input (current and older wordings).
var amuxGuidancePattern = regexp.MustCompile(`\s*\[amux (?:文件交付|前台协调)说明：[^\]]*\]`)
var amuxCapabilityGuidancePattern = regexp.MustCompile(regexp.QuoteMeta(discoveryContextStart) + `(?s:.*?)` + regexp.QuoteMeta(discoveryContextEnd))
var amuxSourceGuidancePattern = regexp.MustCompile(regexp.QuoteMeta(sourceContextStart) + `(?s:.*?)` + regexp.QuoteMeta(sourceContextEnd))

// stripAmuxGuidance removes bridge-appended protocol text so user-visible
// surfaces (card question header, queue listing, echoes) show only what the
// user typed.
func stripAmuxGuidance(text string) string {
	text = amuxSourceGuidancePattern.ReplaceAllString(text, "")
	text = amuxCapabilityGuidancePattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, artifactInstructions, "")
	text = strings.ReplaceAll(text, "\n\n"+strings.TrimSpace(artifactInstructions)+"\n\n"+foregroundInstructions, "")
	text = strings.ReplaceAll(text, foregroundInstructions, "")
	return strings.TrimRight(amuxGuidancePattern.ReplaceAllString(text, ""), " \t\r\n")
}
