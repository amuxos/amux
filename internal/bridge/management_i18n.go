package bridge

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed locales/en.json locales/zh-CN.json
var managementLocales embed.FS
var managementCatalog = func() *i18n.Catalog {
	en, err := managementLocales.ReadFile("locales/en.json")
	if err != nil {
		panic(err)
	}
	zh, err := managementLocales.ReadFile("locales/zh-CN.json")
	if err != nil {
		panic(err)
	}
	catalog, err := i18n.New(en, zh)
	if err != nil {
		panic(err)
	}
	return catalog
}()

func managementCopy(key string, args ...any) i18n.Text { return managementCatalog.Text(key, args...) }

// Management presentation accepts product Text or literal strings. Conversion is
// explicit: user-supplied strings never participate in translation lookup.
func managementText(value any) i18n.Text {
	switch text := value.(type) {
	case i18n.Text:
		return text
	case string:
		return i18n.Literal(text)
	case nil:
		return i18n.Literal("")
	default:
		panic(fmt.Sprintf("unsupported management text %T", value))
	}
}

func managementDefaultText(value any) string { return managementText(value).For(i18n.Chinese) }

func managementPlainText(value any) cardTitle {
	text := managementText(value)
	title := cardTitle{Tag: "plain_text", Content: strings.TrimSpace(text.For(i18n.Chinese))}
	if _, localized := value.(i18n.Text); localized && text.For(i18n.English) != text.For(i18n.Chinese) {
		title.I18nContent = map[string]string{"en_us": text.For(i18n.English), "zh_cn": text.For(i18n.Chinese)}
	}
	return title
}

func managementPlainTextPtr(value any) *cardTitle { text := managementPlainText(value); return &text }

func managementSummary(value any) *cardSummary {
	text := managementPlainText(value)
	return &cardSummary{Content: text.Content, I18nContent: text.I18nContent}
}

func managementValueOrPlaceholder(value string, placeholder any) any {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return placeholder
}

func managementNonemptyText(value, fallback any) any {
	if strings.TrimSpace(managementDefaultText(value)) != "" {
		return value
	}
	return fallback
}

func managementStyledText(value any, style cardTitle) cardTitle {
	text := managementPlainText(value)
	style.Content = text.Content
	style.I18nContent = text.I18nContent
	return style
}

func managementSummaryValue(value any) cardSummary { return *managementSummary(value) }

func managementContentElement(value any, element cardElement) cardElement {
	text := managementPlainText(value)
	element.Content = text.Content
	element.I18nContent = text.I18nContent
	return element
}

func managementConcat(values ...any) i18n.Text {
	texts := make([]i18n.Text, len(values))
	for i, value := range values {
		texts[i] = managementText(value)
	}
	return i18n.Join(texts...)
}

type managementCopyError struct {
	text  i18n.Text
	cause error
}

func (e managementCopyError) Error() string { return e.text.For(i18n.Chinese) }

func managementLocalizedError(key string, args ...any) error {
	return managementCopyError{text: managementCopy(key, args...)}
}

func managementStyledTextPtr(value any, style cardTitle) *cardTitle {
	text := managementStyledText(value, style)
	return &text
}

func managementTruncateText(value any, limit int) any {
	if _, ok := value.(i18n.Text); !ok {
		return truncateCardText(managementDefaultText(value), limit)
	}
	return managementText(value).Map(func(s string) string { return truncateCardText(s, limit) })
}

func managementJoin(separator string, values ...any) any {
	parts := make([]any, 0, len(values)*2)
	for i, value := range values {
		if i > 0 {
			parts = append(parts, separator)
		}
		parts = append(parts, value)
	}
	return managementConcat(parts...)
}

func managementGroupWatchSuccessText(operation string) any {
	key := "saved"
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "add":
		key = "added"
	case "remove":
		key = "removed"
	case "pause":
		key = "paused"
	case "resume":
		key = "resumed"
	}
	return managementCopy("management.watch." + key)
}

func managementCapabilityName(issue capabilityPermissionIssue) any {
	switch issue.Capability {
	case "core_messaging", capabilityArtifactUpload, "native_commands", "bot_to_bot", "group_watch", "document_comments", "feishu_integration":
		return managementCopy("management.capability." + issue.Capability)
	}
	return managementValueOrPlaceholder(issue.Name, managementCopy("management.capability.fallback"))
}

func managementCapabilityPermissionNotice(issue capabilityPermissionIssue) any {
	var affected any = ""
	if len(issue.Affected) > 0 {
		parts := make([]any, 0, len(issue.Affected)*2)
		for index, name := range issue.Affected {
			if index > 0 {
				parts = append(parts, managementCopy("management.list.separator"))
			}
			var label any = name
			// The legacy permission auditor stores declaration names, not IDs. Only
			// known product capability declarations resolve here; custom labels remain
			// literal. This is not a translation lookup for arbitrary user content.
			for _, spec := range feishuCapabilitySpecs {
				if name == spec.Name {
					label = managementCapabilityName(capabilityPermissionIssue{Capability: spec.ID, Name: spec.Name})
					break
				}
			}
			parts = append(parts, label)
		}
		affected = managementCopy("management.permission.affected", managementConcat(parts...))
	}
	scopes := strings.Join(issue.Scopes, "、")
	if issue.Capability == capabilityFeishuIntegration {
		return managementCopy("management.permission.integration.notice", affected, scopes)
	}
	return managementCopy("management.permission.capability.notice", managementCapabilityName(issue), affected, scopes)
}

func managementScopeApplyErrorNotice(err error) any {
	key := "failed"
	switch {
	case isFeishuScopeApplyCode(err, 212001):
		key = "high.sensitivity"
	case isFeishuScopeApplyCode(err, 212002):
		key = "none.available"
	case isFeishuScopeApplyCode(err, 212003):
		key = "rate.limited"
	case isFeishuScopeApplyCode(err, 212004):
		key = "pending"
	}
	return managementCopy("management.permission." + key)
}

func (e managementCopyError) Unwrap() error { return e.cause }

func managementWrapError(err error, key string) error {
	return managementCopyError{text: managementCopy(key, managementErrorText(err)), cause: err}
}

func managementErrorText(err error) any {
	var localized managementCopyError
	if errors.As(err, &localized) {
		return localized.text
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func managementImpactText(result BotSettingsResult) any {
	return managementNonemptyText(result.ImpactMessage, result.ImpactSummary)
}
