package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
)

const nativeSlashCommandsPath = "/open-apis/application/v7/app_slash_commands"
const nativeSlashCommandSyncTimeout = 15 * time.Second
const nativeSlashCommandLimit = 100

var retiredManagedNativeSlashCommands = map[string]string{
	"queue":  "查看当前 Session 的待处理消息",
	"unbind": "解除当前话题的 Session 绑定",
}

// nativeSlashController owns native Feishu slash-command synchronisation and
// advertised agent-command state.
type nativeSlashController struct {
	nativeSlashSyncOnce  sync.Once
	nativeSlashMu        sync.Mutex
	nativeSlashCommands  map[string]command.NativeSlashCommand
	nativeSlashTargetID  string
	nativeSlashSession   string
	nativeSlashAliases   map[string]map[string]advertisedAgentCommand
	nativeSlashSkills    map[string][]advertisedAgentCommand
	nativeSlashSyncing   bool
	nativeSlashSyncAgain bool
}

type nativeSlashCommandDescription struct {
	DefaultValue string            `json:"default_value"`
	I18n         map[string]string `json:"i18n,omitempty"`
}

type nativeSlashCommandIcon struct {
	IconKey string `json:"icon_key"`
}

// The live API and the official lark-cli both use a top-level icon field for
// create/update requests. The Open Platform create example that nests icon
// under description is incorrect.
type nativeSlashCommandPayload struct {
	Command     string                        `json:"command,omitempty"`
	Description nativeSlashCommandDescription `json:"description"`
	Icon        nativeSlashCommandIcon        `json:"icon"`
}

type nativeSlashCommandItem struct {
	CommandID string `json:"command_id"`
	nativeSlashCommandPayload
}

type nativeSlashCommandListResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items []nativeSlashCommandItem `json:"items"`
	} `json:"data"`
}

type nativeSlashCommandMutationResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (b *Bridge) startNativeSlashCommandSync(ctx context.Context) {
	if b == nil {
		return
	}
	b.nativeSlashSyncOnce.Do(func() {
		b.scheduleNativeSlashCommandSync()
	})
}

func (b *Bridge) scheduleNativeSlashCommandSync() {
	if b == nil || b.client == nil {
		return
	}
	b.nativeSlashMu.Lock()
	if b.nativeSlashSyncing {
		b.nativeSlashSyncAgain = true
		b.nativeSlashMu.Unlock()
		return
	}
	b.nativeSlashSyncing = true
	b.nativeSlashMu.Unlock()
	go b.runNativeSlashCommandSync()
}

func (b *Bridge) runNativeSlashCommandSync() {
	for {
		desired := b.desiredNativeSlashCommands()
		syncCtx, cancel := context.WithTimeout(context.Background(), nativeSlashCommandSyncTimeout)
		err := SyncNativeSlashCommands(syncCtx, b.client, desired)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("profile", b.profile).
				Msg("bridge: native slash command sync failed; continuing without command discovery")
		} else {
			log.Info().Str("profile", b.profile).Int("commands", len(desired)).
				Msg("bridge: native slash commands synchronized")
		}
		b.nativeSlashMu.Lock()
		if !b.nativeSlashSyncAgain {
			b.nativeSlashSyncing = false
			b.nativeSlashMu.Unlock()
			return
		}
		b.nativeSlashSyncAgain = false
		b.nativeSlashMu.Unlock()
	}
}

func (b *Bridge) desiredNativeSlashCommands() []command.NativeSlashCommand {
	desired := command.GlobalNativeSlashCommands()
	bound, singleton := b.currentSingletonSlashBinding()
	if singleton {
		desired = command.SingletonNativeSlashCommands(supportsNativeSkillDiscovery(bound.AgentType))
	}
	b.nativeSlashMu.Lock()
	if singleton && strings.TrimSpace(bound.TargetID) == b.nativeSlashTargetID &&
		strings.TrimSpace(bound.SessionName) == b.nativeSlashSession {
		aliases := make([]string, 0, len(b.nativeSlashCommands))
		for alias := range b.nativeSlashCommands {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			desired = append(desired, b.nativeSlashCommands[alias])
		}
	}
	b.nativeSlashMu.Unlock()
	if len(desired) > nativeSlashCommandLimit {
		desired = desired[:nativeSlashCommandLimit]
	}
	return desired
}

func (b *Bridge) currentSingletonSlashBinding() (*BotSession, bool) {
	if b == nil || b.db == nil {
		return nil, false
	}
	mode, err := b.interactionMode()
	if err != nil || mode != config.FeishuInteractionSingleton {
		return nil, false
	}
	bound, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil || !botSessionReady(bound) {
		return nil, false
	}
	return bound, true
}

func (b *Bridge) resetSingletonNativeSlashCommands(targetID, sessionName string) {
	bound, singleton := b.currentSingletonSlashBinding()
	targetID = strings.TrimSpace(targetID)
	sessionName = strings.TrimSpace(sessionName)
	if !singleton || strings.TrimSpace(bound.TargetID) != targetID || strings.TrimSpace(bound.SessionName) != sessionName {
		return
	}
	b.nativeSlashMu.Lock()
	delete(b.nativeSlashAliases, nativeSlashSessionKey(targetID, sessionName))
	delete(b.nativeSlashSkills, nativeSlashSessionKey(targetID, sessionName))
	b.nativeSlashCommands = make(map[string]command.NativeSlashCommand)
	b.nativeSlashTargetID = targetID
	b.nativeSlashSession = sessionName
	b.nativeSlashMu.Unlock()
	b.scheduleNativeSlashCommandSync()
}

// SyncNativeSlashCommands reconciles the desired application-level discovery
// commands. It deletes only stale entries whose complete registered signature
// matches an amux-managed static, dynamic, or historical format; all other
// registered commands remain untouched.
func SyncNativeSlashCommands(ctx context.Context, client *lark.Client, desired []command.NativeSlashCommand) error {
	if client == nil {
		return fmt.Errorf("native slash command sync: nil Lark client")
	}
	resp, err := client.Get(ctx, nativeSlashCommandsPath, nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return fmt.Errorf("native slash command list: %w", err)
	}
	var listed nativeSlashCommandListResponse
	if err := decodeNativeSlashCommandResponse(resp, &listed); err != nil {
		return fmt.Errorf("native slash command list: %w", err)
	}
	if listed.Code != 0 {
		return nativeSlashCommandAPIError("list", listed.Code, listed.Msg)
	}

	desiredNames := make(map[string]struct{}, len(desired))
	for _, command := range desired {
		desiredNames[command.Command] = struct{}{}
	}
	existing := make(map[string]nativeSlashCommandItem, len(listed.Data.Items))
	for _, item := range listed.Data.Items {
		if _, wanted := desiredNames[item.Command]; !wanted &&
			staleManagedNativeSlashCommand(item) {
			path := nativeSlashCommandsPath + "/" + url.PathEscape(item.CommandID)
			resp, err := client.Delete(ctx, path, nil, larkcore.AccessTokenTypeTenant)
			if err != nil {
				log.Warn().Err(err).Str("command", item.Command).
					Msg("bridge: failed to remove retired managed native slash command")
				existing[item.Command] = item
				continue
			}
			var deleted nativeSlashCommandMutationResponse
			if err := decodeNativeSlashCommandResponse(resp, &deleted); err != nil {
				log.Warn().Err(err).Str("command", item.Command).
					Msg("bridge: failed to decode retired managed native slash command deletion")
				existing[item.Command] = item
				continue
			}
			if deleted.Code != 0 {
				log.Warn().Err(nativeSlashCommandAPIError("delete retired managed "+item.Command, deleted.Code, deleted.Msg)).
					Str("command", item.Command).Msg("bridge: failed to remove retired managed native slash command")
				existing[item.Command] = item
				continue
			}
			continue
		}
		existing[item.Command] = item
	}
	for _, command := range desired {
		item, ok := existing[command.Command]
		if ok && nativeSlashCommandMatches(item, command) {
			continue
		}
		payload := nativeSlashPayload(command)
		if ok {
			payload.Command = ""
			path := nativeSlashCommandsPath + "/" + url.PathEscape(item.CommandID)
			resp, err := client.Patch(ctx, path, payload, larkcore.AccessTokenTypeTenant)
			if err != nil {
				return fmt.Errorf("native slash command update %q: %w", command.Command, err)
			}
			var updated nativeSlashCommandMutationResponse
			if err := decodeNativeSlashCommandResponse(resp, &updated); err != nil {
				return fmt.Errorf("native slash command update %q: %w", command.Command, err)
			}
			if updated.Code != 0 {
				return nativeSlashCommandAPIError("update "+command.Command, updated.Code, updated.Msg)
			}
			continue
		}
		if len(existing) >= nativeSlashCommandLimit {
			continue
		}
		resp, err := client.Post(ctx, nativeSlashCommandsPath, payload, larkcore.AccessTokenTypeTenant)
		if err != nil {
			return fmt.Errorf("native slash command create %q: %w", command.Command, err)
		}
		var created nativeSlashCommandMutationResponse
		if err := decodeNativeSlashCommandResponse(resp, &created); err != nil {
			return fmt.Errorf("native slash command create %q: %w", command.Command, err)
		}
		if created.Code != 0 {
			return nativeSlashCommandAPIError("create "+command.Command, created.Code, created.Msg)
		}
		existing[command.Command] = nativeSlashCommandItem{nativeSlashCommandPayload: payload}
	}
	return nil
}

type advertisedAgentCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

func nativeSlashSessionKey(targetID, sessionName string) string {
	return strings.TrimSpace(targetID) + "\x00" + strings.TrimSpace(sessionName)
}

func (b *Bridge) clearAdvertisedAgentCommands(targetID, sessionName string) {
	if b == nil {
		return
	}
	bound, projectToNativeMenu := b.currentSingletonSlashBinding()
	projectToNativeMenu = projectToNativeMenu &&
		strings.TrimSpace(bound.TargetID) == strings.TrimSpace(targetID) &&
		strings.TrimSpace(bound.SessionName) == strings.TrimSpace(sessionName)
	b.nativeSlashMu.Lock()
	if b.nativeSlashAliases == nil {
		b.nativeSlashAliases = make(map[string]map[string]advertisedAgentCommand)
	}
	if b.nativeSlashSkills == nil {
		b.nativeSlashSkills = make(map[string][]advertisedAgentCommand)
	}
	key := nativeSlashSessionKey(targetID, sessionName)
	b.nativeSlashAliases[key] = map[string]advertisedAgentCommand{}
	b.nativeSlashSkills[key] = nil
	changed := false
	if projectToNativeMenu {
		changed = len(b.nativeSlashCommands) != 0
		b.nativeSlashCommands = make(map[string]command.NativeSlashCommand)
		b.nativeSlashTargetID = strings.TrimSpace(targetID)
		b.nativeSlashSession = strings.TrimSpace(sessionName)
	}
	b.nativeSlashMu.Unlock()
	if changed {
		b.scheduleNativeSlashCommandSync()
	}
}

// observeAvailableCommands replaces one session's advertised command aliases
// and merges their discovery metadata into the application-level picker.
func (b *Bridge) observeAvailableCommands(targetID, sessionName string, raw json.RawMessage) bool {
	if b == nil {
		return false
	}
	var advertised []advertisedAgentCommand
	if err := json.Unmarshal(raw, &advertised); err != nil {
		log.Warn().Err(err).Str("session", sessionName).Msg("bridge: invalid available commands event")
		return false
	}
	static := make(map[string]struct{})
	for _, command := range command.ManagedNativeSlashCommands() {
		static[command.Command] = struct{}{}
	}
	for command := range retiredManagedNativeSlashCommands {
		static[command] = struct{}{}
	}
	aliases := make(map[string]advertisedAgentCommand, len(advertised))
	dynamic := make(map[string]command.NativeSlashCommand, len(advertised))
	skillsByName := make(map[string]advertisedAgentCommand)
	ambiguous := make(map[string]bool)
	for _, advertisedCommand := range advertised {
		name := strings.TrimPrefix(strings.TrimSpace(advertisedCommand.Name), "/")
		advertisedCommand.Name = name
		if advertisedCommand.Kind == "skill" {
			if name != "" {
				if _, duplicate := skillsByName[name]; !duplicate {
					skillsByName[name] = advertisedCommand
				}
			}
			continue
		}
		alias, ok := nativeAgentCommandAlias(name)
		if !ok {
			continue
		}
		if _, reserved := static[alias]; reserved {
			continue
		}
		if previous, exists := aliases[alias]; exists && (previous.Name != name || previous.Kind != advertisedCommand.Kind) {
			delete(aliases, alias)
			delete(dynamic, alias)
			ambiguous[alias] = true
			continue
		}
		if ambiguous[alias] {
			continue
		}
		aliases[alias] = advertisedCommand
		description := agentCommandDescription(advertisedCommand)
		dynamic[alias] = command.NativeSlashCommand{
			Command: alias, Description: description,
			I18n:    map[string]string{"zh_cn": description, "en_us": description},
			IconKey: "ai-agent_outlined",
		}
	}
	bound, projectToNativeMenu := b.currentSingletonSlashBinding()
	projectToNativeMenu = projectToNativeMenu &&
		strings.TrimSpace(bound.TargetID) == strings.TrimSpace(targetID) &&
		strings.TrimSpace(bound.SessionName) == strings.TrimSpace(sessionName)
	b.nativeSlashMu.Lock()
	if b.nativeSlashAliases == nil {
		b.nativeSlashAliases = make(map[string]map[string]advertisedAgentCommand)
	}
	if b.nativeSlashCommands == nil {
		b.nativeSlashCommands = make(map[string]command.NativeSlashCommand)
	}
	if b.nativeSlashSkills == nil {
		b.nativeSlashSkills = make(map[string][]advertisedAgentCommand)
	}
	key := nativeSlashSessionKey(targetID, sessionName)
	b.nativeSlashAliases[key] = aliases
	skillNames := make([]string, 0, len(skillsByName))
	for name := range skillsByName {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	skills := make([]advertisedAgentCommand, 0, len(skillNames))
	for _, name := range skillNames {
		skills = append(skills, skillsByName[name])
	}
	b.nativeSlashSkills[key] = skills
	changed := false
	if projectToNativeMenu {
		changed = b.nativeSlashTargetID != strings.TrimSpace(targetID) ||
			b.nativeSlashSession != strings.TrimSpace(sessionName) ||
			!nativeSlashCommandMapsEqual(b.nativeSlashCommands, dynamic)
		b.nativeSlashCommands = dynamic
		b.nativeSlashTargetID = strings.TrimSpace(targetID)
		b.nativeSlashSession = strings.TrimSpace(sessionName)
	}
	b.nativeSlashMu.Unlock()
	if changed {
		b.scheduleNativeSlashCommandSync()
	}
	return true
}

func nativeSlashCommandMapsEqual(left, right map[string]command.NativeSlashCommand) bool {
	if len(left) != len(right) {
		return false
	}
	for alias, command := range left {
		other, ok := right[alias]
		if !ok || !nativeSlashCommandEqual(command, other) {
			return false
		}
	}
	return true
}

func (c *nativeSlashController) advertisedSkills(targetID, sessionName string) []advertisedAgentCommand {
	if c == nil {
		return nil
	}
	c.nativeSlashMu.Lock()
	skills := append([]advertisedAgentCommand(nil), c.nativeSlashSkills[nativeSlashSessionKey(targetID, sessionName)]...)
	c.nativeSlashMu.Unlock()
	return skills
}

func (b *Bridge) advertisedSkill(targetID, sessionName, name string) (advertisedAgentCommand, bool) {
	name = strings.TrimSpace(name)
	for _, skill := range b.advertisedSkills(targetID, sessionName) {
		if skill.Name == name {
			return skill, true
		}
	}
	return advertisedAgentCommand{}, false
}

func (b *Bridge) expandAdvertisedAgentCommand(targetID, sessionName, text string) string {
	trimmed := strings.TrimSpace(text)
	parts := strings.Fields(trimmed)
	command, ok := b.advertisedAgentCommandForText(targetID, sessionName, text)
	if !ok {
		return text
	}
	prefix := "/"
	if command.Kind == "skill" {
		prefix = "$"
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, parts[0]))
	if rest == "" {
		return prefix + command.Name
	}
	return prefix + command.Name + " " + rest
}

func (b *Bridge) advertisedAgentCommandName(targetID, sessionName, text string) (string, bool) {
	command, ok := b.advertisedAgentCommandForText(targetID, sessionName, text)
	return command.Name, ok
}

func (c *nativeSlashController) advertisedAgentCommandForText(targetID, sessionName, text string) (advertisedAgentCommand, bool) {
	parts := strings.Fields(strings.TrimSpace(text))
	if c == nil || len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return advertisedAgentCommand{}, false
	}
	alias := strings.TrimPrefix(parts[0], "/")
	c.nativeSlashMu.Lock()
	command, ok := c.nativeSlashAliases[nativeSlashSessionKey(targetID, sessionName)][alias]
	c.nativeSlashMu.Unlock()
	return command, ok && command.Name != ""
}

func (b *Bridge) hasSingletonAdvertisedAgentCommand(text string) bool {
	if b == nil || b.db == nil {
		return false
	}
	bs, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil || !botSessionReady(bs) {
		return false
	}
	_, ok := b.advertisedAgentCommandName(bs.TargetID, bs.SessionName, text)
	return ok
}

func nativeAgentCommandAlias(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	hasHyphen := false
	for _, r := range name {
		if r == '-' {
			hasHyphen = true
			continue
		}
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		return "", false
	}
	if !hasHyphen {
		return name, true
	}
	var encoded strings.Builder
	encoded.WriteString("agent_")
	for _, r := range name {
		switch r {
		case '-':
			encoded.WriteByte('_')
		case '_':
			encoded.WriteString("__")
		default:
			encoded.WriteRune(r)
		}
	}
	return encoded.String(), true
}

func agentCommandDescription(command advertisedAgentCommand) string {
	prefix := "/"
	if command.Kind == "skill" {
		prefix = "$"
	}
	text := prefix + command.Name
	if description := strings.TrimSpace(command.Description); description != "" {
		text += " — " + description
	}
	if hint := strings.TrimSpace(command.InputHint); hint != "" {
		text += " (" + hint + ")"
	}
	return truncateRunes(text, 100)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

func nativeSlashCommandEqual(left, right command.NativeSlashCommand) bool {
	return left.Command == right.Command && left.Description == right.Description &&
		left.IconKey == right.IconKey && maps.Equal(left.I18n, right.I18n)
}

func legacyManagedSkillSlashCommand(item nativeSlashCommandItem) bool {
	return item.CommandID != "" && item.Icon.IconKey == "ai-agent_outlined" &&
		len(item.Description.I18n) == 0 &&
		strings.HasPrefix(strings.TrimSpace(item.Description.DefaultValue), "$")
}

func retiredManagedNativeSlashCommand(item nativeSlashCommandItem) bool {
	description, ok := retiredManagedNativeSlashCommands[item.Command]
	return ok && item.CommandID != "" && item.Icon.IconKey == "ai-agent_outlined" &&
		len(item.Description.I18n) == 0 &&
		strings.TrimSpace(item.Description.DefaultValue) == description
}

func staleManagedNativeSlashCommand(item nativeSlashCommandItem) bool {
	return legacyManagedSkillSlashCommand(item) || retiredManagedNativeSlashCommand(item) ||
		managedStaticNativeSlashCommand(item) || managedDynamicAgentSlashCommand(item)
}

func managedStaticNativeSlashCommand(item nativeSlashCommandItem) bool {
	for _, command := range command.ManagedNativeSlashCommands() {
		if item.Command == command.Command && nativeSlashCommandMatches(item, command) {
			return true
		}
	}
	return false
}

func managedDynamicAgentSlashCommand(item nativeSlashCommandItem) bool {
	description := strings.TrimSpace(item.Description.DefaultValue)
	if item.CommandID == "" || item.Icon.IconKey != "ai-agent_outlined" || description == "" ||
		len(item.Description.I18n) != 2 || item.Description.I18n["zh_cn"] != description ||
		item.Description.I18n["en_us"] != description {
		return false
	}
	fields := strings.Fields(description)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	name := strings.TrimPrefix(fields[0], "/")
	alias, ok := nativeAgentCommandAlias(name)
	return ok && alias == item.Command
}

func nativeSlashCommandAPIError(action string, code int, message string) error {
	if code == 99991640 {
		return fmt.Errorf(
			"native slash command %s: code=%d msg=%s; grant application:app_slash_command:read and application:app_slash_command:write, publish a new app version, then restart the amux system",
			action,
			code,
			message,
		)
	}
	return fmt.Errorf("native slash command %s: code=%d msg=%s", action, code, message)
}

func nativeSlashCommandMatches(item nativeSlashCommandItem, desired command.NativeSlashCommand) bool {
	return item.Description.DefaultValue == desired.Description &&
		maps.Equal(item.Description.I18n, desired.I18n) &&
		item.Icon.IconKey == desired.IconKey
}

func nativeSlashPayload(command command.NativeSlashCommand) nativeSlashCommandPayload {
	i18n := make(map[string]string, len(command.I18n))
	for locale, description := range command.I18n {
		i18n[locale] = description
	}
	return nativeSlashCommandPayload{
		Command: command.Command,
		Description: nativeSlashCommandDescription{
			DefaultValue: command.Description,
			I18n:         i18n,
		},
		Icon: nativeSlashCommandIcon{IconKey: command.IconKey},
	}
}

func decodeNativeSlashCommandResponse(resp *larkcore.ApiResp, dst any) error {
	if resp == nil {
		return fmt.Errorf("empty response")
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	if err := json.Unmarshal(resp.RawBody, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) &&
		(envelope.Code == nil || *envelope.Code == 0) {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(resp.RawBody, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
