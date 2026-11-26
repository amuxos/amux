package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/principal"
	"github.com/google/uuid"
)

const replyEndExcerptMaxRunes = 80

// The experiment is installation-wide. Reading its saved value also applies
// live changes made through another Bot Profile, without mutating Viper.
// Retain the last applied value if the configuration is temporarily unreadable.
func (b *Bridge) currentReplyEndNotification() bool {
	if b == nil {
		return false
	}
	if saved, err := b.managementConfigActions().ReadExperimentsSettings(); err == nil {
		b.replyEndNotification.Store(saved.ReplyEndNotification)
	}
	return b.replyEndNotification.Load()
}

func (b *Bridge) notifyIsolatedReplyEnd(ctx context.Context, chat *LarkChat, bridgeID, sessionID, turnID, kind, answer string) error {
	if sessionID == "" || b.hub == nil || b.resolveFeishuPrincipal == nil || chat.OpenId == "" ||
		chat.RootMsgID == singletonStreamKey(bridgeID) {
		return nil
	}
	session := b.boundSession(chat.TargetID, chat.SessionName)
	if session.GetId() != sessionID {
		return nil
	}
	identity := principal.FromProtoIdentity(session.GetExecutionIdentity())
	if !feishumsg.IsPersonalSession(identity, identity.OriginActorPrincipalID) {
		return nil
	}
	groupRoot, err := b.db.FindGroupSessionOrigin(bridgeID, chat.ChatID, chat.RootMsgID)
	if err != nil || groupRoot != "" {
		return err
	}
	topicPrincipal, err := b.resolveFeishuPrincipal(chat.OpenId)
	if err != nil || topicPrincipal != identity.OriginActorPrincipalID {
		return nil
	}
	locale := i18n.Resolve(string(b.managementLocale(chat.OpenId)), "", "")
	excerpt := replyEndExcerpt(answer)
	if excerpt == "" {
		excerpt = sessionMessage("notification.empty." + kind).For(locale)
	}
	linkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	jumpURL, _ := b.completionNotificationURL(linkCtx, chat)
	cancel()
	title := replyEndLimitText(replyEndSanitizedText(chat.DisplayTitle), 12)
	if title == "" {
		title = "amux"
	}
	var source string
	if jumpURL == "" {
		source = sessionMessage("notification.source.unavailable").For(locale)
	} else {
		source = "[" + sessionMessage("notification.source.open").For(locale) + "](" + jumpURL + ")"
	}
	body := fmt.Sprintf("%s｜%s\n%s", title, excerpt, source)

	// Give this Session Turn one durable claim. A lost response can mean
	// Feishu accepted the send, so it must not trigger a second delivery.
	claimed, err := b.db.ClaimTurnNotification(bridgeID, turnID, "reply_end:"+sessionID)
	if err != nil || !claimed {
		return err
	}
	reply, err := b.hub.Route(ctx, chat.TargetID, feishumsg.PersonalRoute, feishumsg.PersonalRequest{
		SessionID: sessionID, PrincipalID: topicPrincipal,
		Message: feishumsg.Message{
			ReceiveIDType: feishumsg.Self, Text: body, Format: "markdown",
			UUID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(bridgeID+"\x00"+turnID)).String(),
		},
	})
	if err != nil {
		return err
	}
	var delivered feishumsg.PersonalResult
	if reply == nil || len(reply.Payload) == 0 || json.Unmarshal(reply.Payload, &delivered) != nil || delivered.MessageID == "" {
		return errors.New("reply-end notification: delivery receipt unavailable")
	}
	return nil
}

var (
	replyEndLinkPattern       = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^)\n]*\)`)
	replyEndHTMLPattern       = regexp.MustCompile(`<[^>\n]*>`)
	replyEndInlineCodePattern = regexp.MustCompile("`[^`\n]*`")
	replyEndListPattern       = regexp.MustCompile(`^(?:[-*+]\s+|[0-9]+[.)]\s+)`)
	replyEndCredentialPattern = regexp.MustCompile(`(?i)\b((?:access[_-]?key|app[_-]?secret|client[_-]?secret|private[_-]?key)["']?\s*[:=]\s*)["']?[^\s"',;}]+`)
)

// Only the frozen final answer supplies notification content. Provider logs,
// tool output and the Session's mutable latest output are never consulted.
func replyEndExcerpt(answer string) string {
	var fallback string
	inResult := false
	for _, line := range strings.Split(replyEndProse(answer), "\n") {
		line = strings.TrimSpace(line)
		heading := strings.HasPrefix(line, "#")
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		line = replyEndListPattern.ReplaceAllString(line, "")
		label, body := line, ""
		if i := strings.IndexAny(line, ":："); i >= 0 {
			label, body = line[:i], strings.TrimSpace(strings.TrimLeft(line[i:], ":："))
		}
		switch strings.ToLower(strings.Trim(label, " *_")) {
		case "结论", "最终结论", "结果", "最终结果", "交付结果", "总结", "conclusion", "result", "final result", "summary", "outcome":
			inResult, line = true, body
		default:
			if heading {
				inResult = false
				continue
			}
		}
		if (!inResult && fallback != "") || strings.HasPrefix(line, "|") {
			continue
		}
		line = replyEndSanitizedText(line)
		prose := strings.ReplaceAll(strings.ReplaceAll(line, "<redacted>", ""), "<path>", "")
		if !strings.ContainsFunc(prose, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			continue
		}
		if inResult {
			return replyEndLimitText(line, replyEndExcerptMaxRunes)
		}
		fallback = replyEndLimitText(replyEndFirstSentence(line), replyEndExcerptMaxRunes)
	}
	return fallback
}

func replyEndFirstSentence(line string) string {
	// ASCII full stops only end a sentence before whitespace, so versions
	// and decimals stay intact.
	for i, r := range line {
		if strings.ContainsRune("。！？!?", r) || r == '.' && (i+1 == len(line) || line[i+1] == ' ') {
			return line[:i+len(string(r))]
		}
	}
	return line
}

func replyEndProse(answer string) string {
	var lines []string
	fence := ""
	for _, line := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
			continue
		}
		if fence == "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func replyEndLimitText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit-1])) + "…"
	}
	return text
}

func replyEndSanitizedText(text string) string {
	text = replyEndLinkPattern.ReplaceAllString(text, "$1")
	text = errorSummaryURLPattern.ReplaceAllString(text, "")
	text = replyEndInlineCodePattern.ReplaceAllString(text, "")
	text = replyEndHTMLPattern.ReplaceAllString(text, "")
	text = replyEndCredentialPattern.ReplaceAllString(text, "$1<redacted>")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "**", ""), "__", "")
	text = strings.Trim(text, "*_~> ")
	return redactProviderErrorText(text)
}
