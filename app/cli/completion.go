package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/schedule"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const completionTimeout = 300 * time.Millisecond

func completeNoFiles(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeScheduleCreateArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeSessionRefs(cmd, args, toComplete)
	case 1:
		return completeScheduleSpec(cmd, args, toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeScheduleSpec(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return scheduleSpecCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeScheduleRunModes(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return scheduleRunModeCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func scheduleRunModeCompletions(toComplete string) []string {
	return filterCompletionValues([]string{
		schedule.RunModeReuseSession + "\treuse the template session",
		schedule.RunModeNewSessionPerRun + "\tcreate a fresh session for each run",
	}, toComplete)
}

func completeScheduleDeliveryTargets(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterCompletionValues([]string{
		schedule.DeliveryTargetNone + "\tdo not proactively send run results",
		schedule.DeliveryTargetOrigin + "\tsend run results back to the originating chat/thread",
	}, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeScheduleDeliveryPolicies(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterCompletionValues([]string{
		schedule.DeliveryPolicyAlways + "\tsend on every terminal run",
		schedule.DeliveryPolicyOnFailure + "\tsend only when a run fails or times out",
		schedule.DeliveryPolicyOnNonEmpty + "\tsend only when a run fails or produces non-silent output",
	}, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func scheduleSpecCompletions(toComplete string) []string {
	candidates := []string{
		`every	interval, e.g. "every 30m"`,
		`daily	daily time, e.g. "daily 09:00"`,
		`at	one-shot time, e.g. "at 2026-06-01 09:00"`,
		`cron	5-field cron, e.g. "cron 0 9 * * 1-5"`,
	}
	return filterCompletionValues(candidates, toComplete)
}

func completeSessionRefs(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx, cancel := context.WithTimeout(cmdContext(cmd), completionTimeout)
	defer cancel()

	r, err := newCompletionSessionResolver(completionTimeout)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer r.Close()

	targets, err := r.listTargets(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return sessionRefCompletions(targets, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func newCompletionSessionResolver(timeout time.Duration) (*sessionResolver, error) {
	_ = loadUserConfig()

	var clients []sessionClient
	hub, err := newHubClientWithTimeout(hubAddr(), timeout)
	if err != nil {
		return nil, err
	}
	clients = append(clients, hub)

	r := &sessionResolver{clients: clients}
	r.listTargets = func(ctx context.Context) ([]targetSessions, error) {
		targets, err := hub.ListTargets(ctx)
		if err != nil {
			return nil, err
		}
		return targets, nil
	}
	return r, nil
}

func sessionRefCompletions(targets []targetSessions, toComplete string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, ts := range targets {
		for _, sess := range ts.sessions {
			if sess == nil || sess.Name == "" {
				continue
			}
			ref := sessionDisplayRef(ts.target, sess)
			if !strings.HasPrefix(ref, toComplete) {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
}

type scheduleCompletionFilter int

const (
	scheduleAny scheduleCompletionFilter = iota
	scheduleEnabledOnly
	scheduleDisabledOnly
)

func completeScheduleIDs(filter scheduleCompletionFilter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		client, closeClient, err := newAmuxdClientWithTimeout(completionTimeout)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer closeClient()

		ctx, cancel := context.WithTimeout(cmdContext(cmd), completionTimeout)
		defer cancel()
		resp, err := client.ListSchedules(ctx, &pb.ListSchedulesRequest{})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return scheduleIDCompletions(resp.Schedules, toComplete, filter), cobra.ShellCompDirectiveNoFileComp
	}
}

func scheduleIDCompletions(items []*pb.ScheduleItem, toComplete string, filter scheduleCompletionFilter) []string {
	items = append([]*pb.ScheduleItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Id < items[j].Id })

	var out []string
	for _, item := range items {
		if item == nil {
			continue
		}
		switch filter {
		case scheduleEnabledOnly:
			if !item.Enabled {
				continue
			}
		case scheduleDisabledOnly:
			if item.Enabled {
				continue
			}
		}
		id := strconv.FormatInt(item.Id, 10)
		if !strings.HasPrefix(id, toComplete) {
			continue
		}
		out = append(out, id+"\t"+scheduleIDDescription(item))
	}
	return out
}

func scheduleIDDescription(item *pb.ScheduleItem) string {
	name := item.Name
	if name == "" {
		name = "—"
	}
	session := item.SessionName
	if item.TargetName != "" {
		session = item.TargetName + "/" + item.SessionName
	} else if item.TargetId != "" {
		session = item.TargetId + "/" + item.SessionName
	}
	state := "disabled"
	if item.Enabled {
		state = "enabled"
	}
	return fmt.Sprintf("%s %s %s %s", name, session, item.Spec, state)
}

func filterCompletionValues(candidates []string, toComplete string) []string {
	if toComplete == "" {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		value := c
		if before, _, ok := strings.Cut(c, "\t"); ok {
			value = before
		}
		if strings.HasPrefix(value, toComplete) {
			out = append(out, c)
		}
	}
	return out
}

func completeDirectories(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

func completeFiles(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}

func completeBoolValues(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return boolValueCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func boolValueCompletions(toComplete string) []string {
	return filterCompletionValues([]string{"true", "false"}, toComplete)
}

func completeAgentTypes(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return agentTypeCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func agentTypeCompletions(toComplete string) []string {
	return filterCompletionValues(adapter.Names(), toComplete)
}

func completeRuntimeRefs(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return runtimeRefCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func runtimeRefCompletions(toComplete string) []string {
	refs, err := agentprofile.RuntimeRefs()
	if err != nil {
		return nil
	}
	return filterCompletionValues(refs, toComplete)
}

func completeRuntimeModels(runtimeFlag string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		runtimeRef, _ := cmd.Flags().GetString(runtimeFlag)
		return runtimeModelCompletions(runtimeRef, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func runtimeModelCompletions(runtimeRef, toComplete string) []string {
	runtime, err := agentprofile.ResolveRuntimeOption(runtimeRef)
	if err != nil {
		return nil
	}
	refs := make([]string, 0, len(runtime.Models))
	for _, model := range runtime.Models {
		refs = append(refs, model.Ref)
	}
	return filterCompletionValues(refs, toComplete)
}

func completeTargets(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx, cancel := context.WithTimeout(cmdContext(cmd), completionTimeout)
	defer cancel()

	r, err := newCompletionSessionResolver(completionTimeout)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer r.Close()

	targets, err := r.listTargets(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return targetCompletions(targets, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func targetCompletions(targets []targetSessions, toComplete string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(value, desc string) {
		if value == "" || !strings.HasPrefix(value, toComplete) {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		if desc != "" {
			out = append(out, value+"\t"+desc)
			return
		}
		out = append(out, value)
	}
	for _, ts := range targets {
		name := ts.target.Name
		id := ts.target.ID
		if name == "" {
			name = id
		}
		add(name, id)
		if id != "" && id != name {
			add(id, name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		vi, _, _ := strings.Cut(out[i], "\t")
		vj, _, _ := strings.Cut(out[j], "\t")
		return vi < vj
	})
	return out
}

func completeSendArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeSessionRefs(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeSingleSessionArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeSessionRefs(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeSingleTargetArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeTargets(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeConfigKeys(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return configKeyCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func configKeyCompletions(toComplete string) []string {
	keys := supportedConfigKeys()
	if profile := configCompletionProfileName(toComplete); profile != "" {
		keys = append(keys, feishuProfileConfigKeys(profile)...)
	}
	return filterCompletionValues(keys, toComplete)
}

func configCompletionProfileName(toComplete string) string {
	const prefix = "bridge.feishu.profiles."
	if !strings.HasPrefix(toComplete, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(toComplete, prefix)
	profile, _, ok := strings.Cut(rest, ".")
	if !ok || profile == "" {
		return ""
	}
	if !configProfileNamePattern.MatchString(profile) {
		return ""
	}
	return profile
}

func feishuProfileConfigKeys(profile string) []string {
	base := "bridge.feishu.profiles." + profile + ".workspace_defaults."
	presentationBase := "bridge.feishu.profiles." + profile + ".presentation."
	documentCommentsBase := "bridge.feishu.profiles." + profile + ".document_comments."
	groupWatchBase := "bridge.feishu.profiles." + profile + ".group_watch."
	return []string{
		base + "agent",
		base + "target",
		base + "cwd",
		base + "model",
		base + "args",
		presentationBase + "thinking_process",
		presentationBase + "completion_notification_after_seconds",
		"bridge.feishu.profiles." + profile + ".bot_e2e.sender_open_ids",
		"bridge.feishu.profiles." + profile + ".bot_e2e.sender_roles",
		documentCommentsBase + "enabled",
		documentCommentsBase + "agent_profile_ref",
		documentCommentsBase + "runtime_ref",
		documentCommentsBase + "model_ref",
		documentCommentsBase + "reasoning_effort",
		"bridge.feishu.profiles." + profile + ".bot_to_bot.enabled",
		"bridge.feishu.profiles." + profile + ".bot_to_bot.max_turns_per_thread",
		groupWatchBase + "enabled",
		groupWatchBase + "chat_ids",
		groupWatchBase + "sender_types",
		groupWatchBase + "chat_sender_types.",
		groupWatchBase + "poll_interval_seconds",
		groupWatchBase + "lookback_seconds",
		groupWatchBase + "dedup.enabled",
		groupWatchBase + "dedup.window_seconds",
		groupWatchBase + "max_active_sessions_per_chat",
		groupWatchBase + "idle_timeout_seconds",
		groupWatchBase + "agent_profile_ref",
		groupWatchBase + "runtime_ref",
		groupWatchBase + "model_ref",
		groupWatchBase + "reasoning_effort",
		groupWatchBase + "thinking_process",
	}
}

func completeConfigSetArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeConfigKeys(cmd, args, toComplete)
	case 1:
		return completeConfigValueForKey(args[0], toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeConfigGetUnsetArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeConfigKeys(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeConfigValueForKey(key, toComplete string) ([]string, cobra.ShellCompDirective) {
	if isThinkingProcessConfigKey(key) {
		return filterCompletionValues([]string{
			"expanded",
			"collapsed",
			"hidden",
		}, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	switch key {
	case "runtime.auto_start", "bridge.feishu.prompt.shared_thread_enabled", "bridge.feishu.prompt.topic_root_enabled",
		"bridge.feishu.document_comments.enabled", "bridge.feishu.bot_to_bot.enabled", "bridge.feishu.group_watch.enabled",
		"bridge.feishu.group_watch.dedup.enabled":
		return boolValueCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
	case "bridge.feishu.prompt.shared_thread_template_file", "bridge.feishu.workspace_defaults.cwd":
		return nil, cobra.ShellCompDirectiveDefault
	default:
		if parseFeishuProfileDocumentCommentsKey(key) == "enabled" {
			return boolValueCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		if field := parseFeishuProfileGroupWatchKey(key); field == "enabled" || field == "dedup.enabled" {
			return boolValueCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		if parseFeishuProfileBotToBotKey(key) == "enabled" {
			return boolValueCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		if field := groupWatchConfigField(key); field == "sender_types" || field == "chat_sender_types" {
			return filterCompletionValues([]string{"user", "app", "user app"}, toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		if parseFeishuProfileInteractionModeKey(key) {
			return filterCompletionValues([]string{"thread", "singleton"}, toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		if parseFeishuProfileWorkspaceDefaultsKey(key) == "cwd" {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeQueueCancelArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeSessionRefs(cmd, args, toComplete)
	case 1:
		return completeQueueIDs(cmd, args[0], toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeQueueIDs(cmd *cobra.Command, sessionRef, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx, cancel := context.WithTimeout(cmdContext(cmd), completionTimeout)
	defer cancel()

	r, err := newCompletionSessionResolver(completionTimeout)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer r.Close()

	resolved, err := r.ResolveSession(ctx, sessionRef)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	items, err := resolved.Target.Backend.QueueList(ctx, resolved.Target.ID, resolved.Session.Name)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return queueIDCompletions(items, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func queueIDCompletions(items []*pb.QueueItem, toComplete string) []string {
	items = append([]*pb.QueueItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Id < items[j].Id })
	var out []string
	for _, item := range items {
		if item == nil || item.Id == "" || !strings.HasPrefix(item.Id, toComplete) {
			continue
		}
		desc := item.Source
		if desc == "" {
			desc = oneLine(item.Text, 60)
		}
		if desc != "" {
			out = append(out, item.Id+"\t"+desc)
			continue
		}
		out = append(out, item.Id)
	}
	return out
}
