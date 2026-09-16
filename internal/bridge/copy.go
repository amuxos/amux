package bridge

import (
	"fmt"
	"strings"
)

func ownerConfirmationRequiredText() string {
	return "我已准备好，但还需要负责人确认身份。\n" +
		"请负责人先私聊我任意消息，然后回到群里 @我发任务。"
}

func ordinarySessionEntryHelpText() string {
	return "@智能体 + 任务即可开始，例如：@智能体 修复 CI 失败。"
}

func memberBlockedInOwnerModeText() string {
	return "你没有权限操作当前任务。这个群当前由负责人控制。\n" +
		"请负责人 @我发起任务；需要多人使用时，让负责人发送“开启协作模式”。"
}

func memberBlockedByBotAccessText() string {
	return "你没有权限使用。这个智能体或当前群仅限负责人使用。\n" +
		"如需群成员使用，请负责人把当前群切换为团队协作模式。"
}

func privateWorkspaceBlockedText() string {
	return "你没有权限操作当前私聊工作区。请负责人使用此 bot。"
}

func taskSubmitFailureText() string {
	return "⚠️ 当前任务暂时不可用，消息未发送。\n请稍后重试；如持续失败，请私聊发送 /amux 查看状态。"
}

func taskBindingStatusText(chat *LarkChat) string {
	if chat == nil {
		return "话题标题：未绑定\n状态：未开始"
	}
	title := strings.TrimSpace(chat.DisplayTitle)
	if title == "" {
		title = chat.SessionName
	}
	expert := strings.TrimSpace(chat.AgentProfile)
	if expert == "" {
		expert = strings.TrimSpace(chat.AgentType)
	}
	if expert == "" {
		expert = "(default)"
	}
	return strings.Join([]string{
		"话题标题：" + title,
		"专家：" + expert,
		"状态：" + chat.Status,
		fmt.Sprintf("高级信息：%s/%s", chat.TargetName, chat.SessionName),
	}, "\n")
}

func advancedBindingStatusText(chat *LarkChat) string {
	if chat == nil {
		return "话题标题：未绑定"
	}
	return strings.Join([]string{
		taskBindingStatusText(chat),
		fmt.Sprintf("target/session: %s/%s", chat.TargetName, chat.SessionName),
		"agent_profile: " + strings.TrimSpace(chat.AgentProfile),
		"agent_type: " + strings.TrimSpace(chat.AgentType),
		"root_msg_id: " + strings.TrimSpace(chat.RootMsgID),
		"chat_id: " + strings.TrimSpace(chat.ChatID),
		"bridge_id: " + strings.TrimSpace(chat.BridgeId),
	}, "\n")
}
