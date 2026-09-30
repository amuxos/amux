package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (h *mcpHandler) readLocalExpert(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Operation string `json:"operation"`
		TargetRef string `json:"target_ref"`
		SourceRef string `json:"source_ref"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	sessionID := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	if sessionID == "" {
		return mcp.ErrorText("local expert authoring is only available inside a Feishu Bot owner session"), nil
	}
	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return mcp.ErrorText("local expert authoring requires hub: " + err.Error()), nil
	}
	defer closeFn()
	resp, err := client.ReadLocalExpert(ctx, &pb.ReadLocalExpertRequest{SessionId: sessionID, Operation: input.Operation, TargetRef: input.TargetRef, SourceRef: input.SourceRef})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	files := make([]map[string]any, 0, len(resp.GetFiles()))
	for _, file := range resp.GetFiles() {
		view := map[string]any{"path": file.GetPath(), "binary": file.GetBinary()}
		if !file.GetBinary() {
			view["content"] = file.GetContent()
		}
		files = append(files, view)
	}
	return mcp.Text(toJSON(map[string]any{"schema_version": "mcp.local_expert_read.v1", "operation": resp.GetOperation(), "target_ref": resp.GetTargetRef(), "source_ref": resp.GetSourceRef(), "base_revision": resp.GetBaseRevision(), "files": files})), nil
}

const localExpertPreviewNextStep = "先展示并检查真实 diff。默认不评测；只有用户对本次候选明确要求，才可准备并运行一次评测。只要 Profile/Skill 本身或测试 case 可能产生写入、通知或其它副作用，即使 case 看起来只读，也必须先展示测试计划并取得用户后续确认。如已返回 evaluation_profile_ref，使用相同 case 与执行参数评测正式版和候选版，随后用 mode=cleanup + preview_receipt 清理；cleanup 不撤销 receipt。patch 变化或评测完成后，重新评测需要新的明确要求和必要的副作用确认。仅在 Bot owner 任意后续消息明确确认后才可发布；publish 必须原样携带 preview 时的 operation、target_ref、source_ref、base_revision、patch 与 preview_receipt，不能省略任何字段。中间普通对话不会撤销 receipt；评测确认不等于发布确认。"

func (h *mcpHandler) changeLocalExpert(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Mode              string `json:"mode"`
		Operation         string `json:"operation"`
		TargetRef         string `json:"target_ref"`
		SourceRef         string `json:"source_ref"`
		BaseRevision      string `json:"base_revision"`
		Patch             string `json:"patch"`
		PreviewReceipt    string `json:"preview_receipt"`
		PrepareEvaluation bool   `json:"prepare_evaluation"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	sessionID := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	if sessionID == "" {
		return mcp.ErrorText("local expert authoring is only available inside a Feishu Bot owner session"), nil
	}
	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return mcp.ErrorText("local expert authoring requires hub: " + err.Error()), nil
	}
	defer closeFn()
	resp, err := client.ChangeLocalExpert(ctx, &pb.ChangeLocalExpertRequest{SessionId: sessionID, Mode: input.Mode, Operation: input.Operation, TargetRef: input.TargetRef, SourceRef: input.SourceRef, BaseRevision: input.BaseRevision, Patch: input.Patch, PreviewReceipt: input.PreviewReceipt, PrepareEvaluation: input.PrepareEvaluation})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	view := map[string]any{"schema_version": "mcp.local_expert_change.v2", "mode": resp.GetMode(), "base_revision": resp.GetBaseRevision(), "tree": resp.GetTree(), "patch_digest": resp.GetPatchDigest(), "diff": resp.GetDiff(), "published": resp.GetPublished()}
	if receipt := strings.TrimSpace(resp.GetPreviewReceipt()); receipt != "" {
		view["preview_receipt"] = receipt
		view["next_step"] = localExpertPreviewNextStep
	}
	if ref := strings.TrimSpace(resp.GetEvaluationProfileRef()); ref != "" {
		view["evaluation_profile_ref"] = ref
		if expiresAt := resp.GetEvaluationExpiresAt(); expiresAt > 0 {
			view["evaluation_expires_at"] = expiresAt
		}
	}
	if resp.GetPublished() {
		view["profile_ref"] = resp.GetProfileRef()
		view["revision"] = resp.GetRevision()
	}
	return mcp.Text(toJSON(view)), nil
}
func (h *mcpHandler) listAgentProfilesPage(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Cursor    string `json:"cursor"`
		Namespace string `json:"namespace"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	offset, err := parseMCPListCursor(input.Cursor)
	if err != nil {
		return mcp.ErrorText("invalid cursor: " + err.Error()), nil
	}
	client, closeFn, err := newAgentProfileClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.ListAgentProfiles(ctx, &pb.ListAgentProfilesRequest{})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty agent profile list response"), nil
	}
	profiles, err := discoveredAgentProfilesFromPB(resp.GetProfiles())
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	profiles = filterDiscoveredAgentProfilesByNamespace(profiles, input.Namespace)
	if offset > len(profiles) {
		return mcp.ErrorText("cursor is past the end of the profile list; call profile_list again without cursor"), nil
	}
	pageEnd := offset + mcpProfileListPageSize
	if pageEnd > len(profiles) {
		pageEnd = len(profiles)
	}
	pageProfiles := profiles[offset:pageEnd]
	info := map[string]any{"schema_version": "mcp.list_agent_profiles.v6", "profiles": discoveredAgentProfileMCPViews(pageProfiles), "capability_sets": profileMCPCapabilitySets(pageProfiles), "profiles_scope": "hub_control_plane", "detail_tool": "amux.profile_get", "page_count": len(pageProfiles), "total_count": len(profiles), "has_more": pageEnd < len(profiles), "isolation": []string{"process", "private-home"}, "permission_policy": []string{"policy-allow", "escalate-to-main", "deny-all"}, "workspace_policy": []string{"temp-dir", "git-worktree"}, "submission_contract": map[string]any{"agent_field": "task_submit.agent / task_run.agent", "preferred_value": "profiles[].ref", "pinned_value": "profiles[].pinned_ref", "legacy_fallback": "raw Agent Type strings are accepted for backward compatibility but are not returned as discovery choices", "resolution_scope": "amux control plane resolves profile refs before scheduling", "profile_ref_usage": "Pass profiles[].ref unless you intentionally need a pinned pack revision; pass profiles[].pinned_ref to lock the exact revision shown here."}, "selection_guidance": "Choose a profile by matching description, supported_models, skill names and capability_sets[profiles[].agent_type]; pass profiles[].ref as task_submit.agent/task_run.agent and optionally one supported_models value as model. Use profile_get for skill descriptions and full profile detail.", "note": "process/private-home are task isolation request modes; identified Linux executions may additionally use the linux-dac backend. container/vm isolation is not yet available."}
	if namespace := strings.TrimSpace(input.Namespace); namespace != "" {
		info["namespace"] = namespace
	}
	if pageEnd < len(profiles) {
		info["next_cursor"] = strconv.Itoa(pageEnd)
		info["pagination_instruction"] = "Call amux.profile_list again with next_cursor as cursor (and the same namespace, if set) until has_more=false. Do not report the profile inventory as complete before reading every page."
	}
	if profilesErr := strings.TrimSpace(resp.GetProfilesError()); profilesErr != "" {
		info["profiles_error"] = profilesErr
	}
	return mcp.Text(toJSON(info)), nil
}
func parseMCPListCursor(cursor string) (int, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("expected a non-negative cursor returned by the previous page")
	}
	return offset, nil
}
func filterDiscoveredAgentProfilesByNamespace(profiles []discoveredAgentProfile, namespace string) []discoveredAgentProfile {
	namespace = strings.Trim(strings.TrimSpace(namespace), "/")
	if namespace == "" {
		return profiles
	}
	filtered := make([]discoveredAgentProfile, 0, len(profiles))
	for _, discovered := range profiles {
		if discovered.Profile.Namespace == namespace {
			filtered = append(filtered, discovered)
		}
	}
	return filtered
}
func profileMCPCapabilitySets(profiles []discoveredAgentProfile) map[string]map[string]any {
	sets := make(map[string]map[string]any)
	for _, discovered := range profiles {
		if _, exists := sets[discovered.Profile.Type]; !exists {
			sets[discovered.Profile.Type] = adapterCapabilitiesView(discovered.Profile.Type)
		}
	}
	return sets
}
func (h *mcpHandler) getAgentProfile(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	input.Ref = strings.TrimSpace(input.Ref)
	if input.Ref == "" {
		return mcp.ErrorText("ref is required"), nil
	}
	client, closeFn, err := newAgentProfileClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.GetAgentProfile(ctx, &pb.GetAgentProfileRequest{Ref: input.Ref})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty agent profile response"), nil
	}
	discovered, err := discoveredAgentProfileFromPB(resp.GetProfile())
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	view := profileMCPViewWithSkills(discovered.Profile, discovered.Skills)
	view["schema_version"] = "mcp.get_agent_profile.v1"
	return mcp.Text(toJSON(view)), nil
}
func discoveredAgentProfilesFromPB(infos []*pb.AgentProfileInfo) ([]discoveredAgentProfile, error) {
	profiles := make([]discoveredAgentProfile, 0, len(infos))
	for _, info := range infos {
		profile, err := discoveredAgentProfileFromPB(info)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Profile.Ref() < profiles[j].Profile.Ref()
	})
	return profiles, nil
}
func discoveredAgentProfileFromPB(info *pb.AgentProfileInfo) (discoveredAgentProfile, error) {
	if info == nil || info.GetProfile() == nil {
		return discoveredAgentProfile{}, fmt.Errorf("hub returned an empty agent profile")
	}
	profile, err := agentprofile.FromPBProfileSpec(info.GetProfile())
	if err != nil {
		return discoveredAgentProfile{}, fmt.Errorf("decode hub agent profile: %w", err)
	}
	skills := make([]agentprofile.SkillInfo, 0, len(info.GetSkills()))
	for _, skill := range info.GetSkills() {
		if skill == nil || strings.TrimSpace(skill.GetName()) == "" {
			continue
		}
		skills = append(skills, agentprofile.SkillInfo{Name: skill.GetName(), Description: skill.GetDescription()})
	}
	return discoveredAgentProfile{Profile: profile, Skills: skills}, nil
}
func discoveredAgentProfileMCPViews(profiles []discoveredAgentProfile) []map[string]any {
	views := make([]map[string]any, 0, len(profiles))
	for _, discovered := range profiles {
		views = append(views, profileMCPViewListSummary(discovered.Profile, discovered.Skills))
	}
	return views
}

func profileMCPViewListSummary(profile agentprofile.Profile, skills []agentprofile. // profileMCPViewListSummary is the compact per-profile row returned by
	// profile_list. The full profile view embeds skill descriptions, placement
	// requirements, envs, mcp config, etc.; with many profiles those bodies bloat
	// the single MCP tool response to tens of KB and agent-side MCP clients keep
	// only the head and tail of an oversized output — silently dropping profiles
	// in the middle (the same class of failure this function's sibling fixes for
	// schedule_list). The list keeps only what profile selection needs (ref, agent
	// type, description, supported models, and skill names without their long
	// descriptions), and full detail remains available on demand via profile_get.
	SkillInfo) map[string]any {
	view := map[string]any{"ref": profile.Ref(), "agent_type": profile.Type, "source": string(profile.Source), "writable": profile.Writable, "description": profile.Description, "profile_capabilities": profileSpecificCapabilitiesView(profile)}
	if pinned := profile.PinnedRef(); pinned != profile.Ref() {
		view["pinned_ref"] = pinned
	}
	if profile.Model != "" {
		view["default_model_ref"] = profile.Model
	}
	if len(profile.SupportedModels) > 0 {
		view["supported_models"] = profile.SupportedModels
	}
	if profile.PermissionPolicy != "" {
		view["permission_policy"] = profile.PermissionPolicy
	}
	if len(skills) > 0 {
		compact := make([]map[string]any, 0, len(skills))
		for _, s := range skills {
			compact = append(compact, map[string]any{"name": s.Name})
		}
		view["skills"] = compact
	}
	return view
}
func profileMCPView(profile agentprofile.Profile) map[string]any {
	return profileMCPViewWithSkills(profile, profile.ResolveSkills())
}
func profileMCPViewWithSkills(profile agentprofile.Profile, skills []agentprofile.SkillInfo) map[string]any {
	view := map[string]any{"ref": profile.Ref(), "agent_type": profile.Type, "source": string(profile.Source), "writable": profile.Writable, "description": profile.Description, "capabilities": profileCapabilitiesView(profile), "submission": map[string]any{"agent": profile.Ref(), "pinned_agent": profile.PinnedRef()}, "placement": map[string]any{"preferred_targets": profile.Placement.PreferredTargets, "requires": map[string]any{"agent_types": profile.Placement.Requires.AgentTypes, "runtime_tools": profile.Placement.Requires.RuntimeTools, "features": profile.Placement.Requires.Features, "credentials": profile.Placement.Requires.Credentials}}}
	if pinned := profile.PinnedRef(); pinned != profile.Ref() {
		view["pinned_ref"] = pinned
	}
	if profile.Model != "" {
		view["default_model_ref"] = profile.Model
	}
	if len(profile.SupportedModels) > 0 {
		view["supported_models"] = profile.SupportedModels
	}
	if len(skills) > 0 {
		view["skills"] = skills
	}
	if len(profile.SkillDirs) > 0 {
		view["skill_dirs"] = profile.SkillDirs
	}
	if profile.Agents != "" {
		view["agents"] = profile.Agents
	}
	if len(profile.DefaultArgs) > 0 {
		view["default_args"] = profile.DefaultArgs
	}
	if len(profile.DefaultEnv) > 0 {
		view["default_env"] = profile.DefaultEnv
	}
	if len(profile.Envs) > 0 {
		view["envs"] = profile.Envs
	}
	if len(profile.EnvRefs) > 0 {
		view["env_refs"] = profile.EnvRefs
	}
	if profile.MCPConfig != "" {
		view["mcp_config"] = profile.MCPConfig
	}
	if profile.PermissionPolicy != "" {
		view["permission_policy"] = profile.PermissionPolicy
	}
	return view
}
func profileCapabilitiesView(profile agentprofile.Profile) map[string]any {
	caps := profileSpecificCapabilitiesView(profile)
	for key, value := range adapterCapabilitiesView(profile.Type) {
		caps[key] = value
	}
	return caps
}
func profileSpecificCapabilitiesView(profile agentprofile.Profile) map[string]any {
	return map[string]any{"profile_ref_usable_as_agent": true, "skills": len(profile.Skills) > 0 || len(profile.SkillDirs) > 0, "agents": profile.Agents != "" || profile.AgentsContent != "", "default_args": len(profile.DefaultArgs) > 0, "default_env": len(profile.DefaultEnv) > 0 || len(profile.Envs) > 0 || len(profile.EnvRefs) > 0, "mcp_config": profile.MCPConfig != "", "permission_policy": profile.PermissionPolicy != ""}
}
func adapterCapabilitiesView(agentType string) map[string]any {
	caps := map[string]any{"base_agent": agentType}
	if a, err := adapter.Get(agentType); err == nil {
		ac := a.Capabilities()
		caps["protocol"] = string(ac.Protocol)
		caps["interrupt"] = ac.Interrupt
		caps["interrupt_strategy"] = string(ac.InterruptStrategy)
		caps["native_result"] = ac.NativeResult
		caps["adapter_supports_mcp_config"] = ac.ConfigLayout[adapter.SurfaceMCP].Supported
		caps["supports_skills"] = ac.ConfigLayout[adapter.SurfaceSkills].Supported
		caps["supports_rules"] = ac.ConfigLayout[adapter.SurfaceRules].Supported
		caps["supports_system_prompt"] = ac.ConfigLayout[adapter.SurfaceSystemPrompt].Supported
	} else {
		caps["adapter_error"] = err.Error()
	}
	return caps
}
