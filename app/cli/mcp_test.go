package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/mcp"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type fakeAgentProfileClient struct {
	pb.AmuxdClient
	list func() (*pb.ListAgentProfilesResponse, error)
	get  func(string) (*pb.GetAgentProfileResponse, error)
}

func (f *fakeAgentProfileClient) ListAgentProfiles(context.Context, *pb.ListAgentProfilesRequest, ...grpc.CallOption) (*pb.ListAgentProfilesResponse, error) {
	return f.list()
}

func (f *fakeAgentProfileClient) GetAgentProfile(_ context.Context, req *pb.GetAgentProfileRequest, _ ...grpc.CallOption) (*pb.GetAgentProfileResponse, error) {
	return f.get(req.GetRef())
}

func useLocalAgentProfileClient(t *testing.T) {
	t.Helper()
	previous := newAgentProfileClient
	newAgentProfileClient = func() (pb.AmuxdClient, func() error, error) {
		return &fakeAgentProfileClient{
			list: func() (*pb.ListAgentProfilesResponse, error) {
				profiles := agentprofile.Builtins()
				registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
				if err == nil {
					profiles, err = agentprofile.DiscoverProfiles(registry, false)
				}
				out := &pb.ListAgentProfilesResponse{}
				for _, profile := range profiles {
					out.Profiles = append(out.Profiles, testAgentProfileInfo(profile))
				}
				if err != nil {
					out.ProfilesError = err.Error()
				}
				return out, nil
			},
			get: func(ref string) (*pb.GetAgentProfileResponse, error) {
				profile, err := resolveAgentProfile(ref)
				if err != nil {
					return nil, err
				}
				return &pb.GetAgentProfileResponse{Profile: testAgentProfileInfo(profile)}, nil
			},
		}, func() error { return nil }, nil
	}
	t.Cleanup(func() { newAgentProfileClient = previous })
}

func testAgentProfileInfo(profile agentprofile.Profile) *pb.AgentProfileInfo {
	info := &pb.AgentProfileInfo{Profile: agentprofile.ToPBProfileSpec(profile)}
	for _, skill := range profile.ResolveSkills() {
		info.Skills = append(info.Skills, &pb.AgentProfileSkillInfo{Name: skill.Name, Description: skill.Description})
	}
	return info
}

func TestProfileMCPViewExposesModelSelectionContract(t *testing.T) {
	view := profileMCPView(agentprofile.Profile{
		Namespace:       "team",
		Name:            "coder",
		Type:            "codex",
		Model:           "gpt-5.4",
		SupportedModels: []string{"gpt-5.4", "gpt-5.5"},
	})
	if got := view["default_model_ref"]; got != "gpt-5.4" {
		t.Fatalf("default_model_ref = %#v", got)
	}
	got, ok := view["supported_models"].([]string)
	if !ok || len(got) != 2 || got[0] != "gpt-5.4" || got[1] != "gpt-5.5" {
		t.Fatalf("supported_models = %#v", view["supported_models"])
	}
}

func TestMCPGetAgentProfileReturnsFullDetail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	useLocalAgentProfileClient(t)
	res, err := (&mcpHandler{}).getAgentProfile(context.Background(), json.RawMessage(`{"ref":"builtin/codex"}`))
	if err != nil {
		t.Fatalf("getAgentProfile: %v", err)
	}
	if res.IsError {
		t.Fatalf("getAgentProfile returned error: %s", res.Content[0].Text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode get_agent_profile: %v", err)
	}
	if got["schema_version"] != "mcp.get_agent_profile.v1" || got["ref"] != "builtin/codex" {
		t.Fatalf("get_agent_profile = %+v", got)
	}
}

func TestMCPListAgentProfilesIncludesPackProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	rev := gitRev(t, repo)
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")
	useLocalAgentProfileClient(t)

	res, err := (&mcpHandler{}).listAgentProfilesPage(context.Background(), nil)
	if err != nil {
		t.Fatalf("listAgentProfilesPage: %v", err)
	}
	view := decodeMCPListAgentProfiles(t, res.Content[0].Text)
	coder := findMCPProfile(t, view.Profiles, "team/coder")
	if coder.PinnedRef != "team/coder@"+rev {
		t.Fatalf("pinned_ref = %q, want team/coder@%s", coder.PinnedRef, rev)
	}
	if coder.AgentType != "" || coder.Description != "Team coding profile" {
		t.Fatalf("team/coder = %+v, want content-only Team coding profile", coder)
	}
	skill, ok := findMCPSkill(coder.Skills, "amux-dev")
	if !ok {
		t.Fatalf("skills = %v, want amux-dev", coder.Skills)
	}
	// list_agent_profiles is a compact discovery view: skill descriptions are
	// intentionally omitted here (they live in profile_get), keeping the list
	// response small enough that agent-side MCP clients never truncate it.
	if skill.Description != "" {
		t.Fatalf("amux-dev skill description = %q, want empty in compact list view (full detail via profile_get)", skill.Description)
	}
	if got, ok := coder.ProfileCapabilities["profile_ref_usable_as_agent"].(bool); !ok || !got {
		t.Fatalf("profile_ref_usable_as_agent = %#v, want true", coder.ProfileCapabilities["profile_ref_usable_as_agent"])
	}
}

func TestMCPListAgentProfilesUsesHubWhenAMUXHomeIsUnreadable(t *testing.T) {
	blockedHome := filepath.Join(t.TempDir(), "service-amux-home")
	if err := os.Mkdir(blockedHome, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMUX_HOME", blockedHome)
	profile := agentprofile.Profile{Namespace: "team", Name: "coder", Source: agentprofile.SourcePack}
	previous := newAgentProfileClient
	newAgentProfileClient = func() (pb.AmuxdClient, func() error, error) {
		return &fakeAgentProfileClient{
			list: func() (*pb.ListAgentProfilesResponse, error) {
				return &pb.ListAgentProfilesResponse{Profiles: []*pb.AgentProfileInfo{testAgentProfileInfo(profile)}}, nil
			},
			get: func(string) (*pb.GetAgentProfileResponse, error) { return nil, nil },
		}, func() error { return nil }, nil
	}
	t.Cleanup(func() { newAgentProfileClient = previous })

	res, err := (&mcpHandler{}).listAgentProfilesPage(context.Background(), nil)
	if err != nil {
		t.Fatalf("listAgentProfilesPage: %v", err)
	}
	view := decodeMCPListAgentProfiles(t, res.Content[0].Text)
	coder := findMCPProfile(t, view.Profiles, "team/coder")
	if coder.AgentType != "" {
		t.Fatalf("team/coder agent_type = %q, want unresolved content", coder.AgentType)
	}
}

func TestMCPListAgentProfilesPagesWithoutLosingCapabilities(t *testing.T) {
	profiles := make([]*pb.AgentProfileInfo, 0, 31)
	for i := 0; i < 31; i++ {
		namespace := "expert-agents"
		source := agentprofile.SourcePack
		if i < 7 {
			namespace = "builtin"
		}
		if i == 7 {
			namespace = "local"
			source = agentprofile.SourceLocal
		}
		profiles = append(profiles, testAgentProfileInfo(agentprofile.Profile{
			Namespace: namespace,
			Name:      fmt.Sprintf("profile-%02d", i),
			Type:      "codex",
			Source:    source,
		}))
	}
	previous := newAgentProfileClient
	newAgentProfileClient = func() (pb.AmuxdClient, func() error, error) {
		return &fakeAgentProfileClient{list: func() (*pb.ListAgentProfilesResponse, error) {
			return &pb.ListAgentProfilesResponse{Profiles: profiles}, nil
		}}, func() error { return nil }, nil
	}
	t.Cleanup(func() { newAgentProfileClient = previous })

	h := &mcpHandler{}
	var cursor string
	var refs []string
	for pageNumber := 0; ; pageNumber++ {
		args, _ := json.Marshal(map[string]string{"cursor": cursor})
		res, err := h.listAgentProfilesPage(context.Background(), args)
		if err != nil || res.IsError {
			t.Fatalf("profile page %d: result=%+v err=%v", pageNumber, res, err)
		}
		if len(res.Content[0].Text) > mcpResultMaxBytes {
			t.Fatalf("profile page %d bytes=%d, want <= %d", pageNumber, len(res.Content[0].Text), mcpResultMaxBytes)
		}
		view := decodeMCPListAgentProfiles(t, res.Content[0].Text)
		if view.TotalCount != 31 || view.CapabilitySets["codex"]["protocol"] != "codex_app_server" {
			t.Fatalf("profile page metadata = %+v", view)
		}
		for _, profile := range view.Profiles {
			refs = append(refs, profile.Ref)
			if profile.Ref == "local/profile-07" && (profile.Source != "local" || !profile.Writable) {
				t.Fatalf("local profile discovery metadata = %+v, want source=local writable=true", profile)
			}
		}
		if !view.HasMore {
			break
		}
		if view.NextCursor == "" {
			t.Fatal("has_more page missing next_cursor")
		}
		cursor = view.NextCursor
	}
	if len(refs) != 31 {
		t.Fatalf("paged refs count = %d, want 31: %v", len(refs), refs)
	}
}

func TestMCPResultContinuationIsBoundedAndLossless(t *testing.T) {
	original := strings.Repeat("能力-αβγ-", 2500)
	h := &mcpHandler{}
	pageResult := h.boundResult(mcp.Text(original))
	firstPageText := pageResult.Content[0].Text
	var firstPage struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(firstPageText), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if retry := h.continuationPage(formatMCPContinuationCursor(strings.Split(firstPage.NextCursor, ":")[0], 0)); retry.Content[0].Text != firstPageText {
		t.Fatal("retrying the same continuation cursor returned a different page")
	}
	var rebuilt strings.Builder
	for pageNumber := 0; ; pageNumber++ {
		if len(pageResult.Content) != 1 || len(pageResult.Content[0].Text) > mcpResultMaxBytes {
			t.Fatalf("page %d bytes=%d, want one content <= %d", pageNumber, len(pageResult.Content[0].Text), mcpResultMaxBytes)
		}
		var page struct {
			Complete     bool   `json:"complete"`
			ContentChunk string `json:"content_chunk"`
			NextCursor   string `json:"next_cursor"`
		}
		if err := json.Unmarshal([]byte(pageResult.Content[0].Text), &page); err != nil {
			t.Fatalf("decode page %d: %v", pageNumber, err)
		}
		rebuilt.WriteString(page.ContentChunk)
		if page.Complete {
			break
		}
		pageResult = h.continueResult(context.Background(), json.RawMessage(fmt.Sprintf(`{"cursor":%q}`, page.NextCursor)))
		if pageNumber > 100 {
			t.Fatal("continuation did not complete")
		}
	}
	if rebuilt.String() != original {
		t.Fatalf("rebuilt bytes=%d, want %d", rebuilt.Len(), len(original))
	}
}

func TestMCPToolSchemaGuidesProfileSelection(t *testing.T) {
	tools := amuxTools()
	var foundList, foundDetail, foundSubmit bool
	for _, tool := range tools {
		switch tool.Name {
		case "amux.profile_list":
			foundList = true
			if !strings.Contains(tool.Description, "Agent Profile") {
				t.Fatalf("profile_list description = %q, want Agent Profile", tool.Description)
			}
			if strings.Contains(tool.Description, "raw subagent types") {
				t.Fatalf("profile_list description = %q, should not advertise raw subagent types", tool.Description)
			}
		case "amux.task_submit":
			foundSubmit = true
			var schema struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("submit schema parse: %v", err)
			}
			if !strings.Contains(schema.Properties["agent"].Description, "Agent Profile 引用") {
				t.Fatalf("agent description = %q, want Agent Profile 引用", schema.Properties["agent"].Description)
			}
			if !strings.Contains(schema.Properties["agent"].Description, "开发版 Profile 引用") ||
				!strings.Contains(tool.Description, "profile_get") {
				t.Fatalf("task_submit guidance must support exact dev profiles: tool=%q agent=%q", tool.Description, schema.Properties["agent"].Description)
			}
			if !strings.Contains(schema.Properties["agent"].Description, "旧调用方兼容入口") {
				t.Fatalf("agent description = %q, want legacy fallback boundary", schema.Properties["agent"].Description)
			}
		case "amux.profile_get":
			foundDetail = true
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("profile_get schema parse: %v", err)
			}
			if !sameStrings(schema.Required, []string{"ref"}) {
				t.Fatalf("profile_get required = %v, want ref", schema.Required)
			}
		}
	}
	if !foundList || !foundDetail || !foundSubmit {
		t.Fatalf("tools found list=%v detail=%v submit=%v, want all", foundList, foundDetail, foundSubmit)
	}
}

func TestMCPToolSchemaGuidesLocalExpertConfirmation(t *testing.T) {
	readTool := findMCPTool(t, amuxTools(), "amux.local_expert_read")
	changeTool := findMCPTool(t, amuxTools(), "amux.local_expert_change")
	if !strings.Contains(readTool.Description, "飞书 Bot owner turn") {
		t.Fatalf("local_expert_read description = %q, want owner-turn boundary", readTool.Description)
	}
	if !strings.Contains(changeTool.Description, "后续明确确认的 owner turn") ||
		!strings.Contains(changeTool.Description, "相同 patch 与 receipt") {
		t.Fatalf("local_expert_change description = %q, want later-turn confirmation rule", changeTool.Description)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(changeTool.InputSchema, &schema); err != nil {
		t.Fatalf("local_expert_change schema parse: %v", err)
	}
	if !sameStrings(schema.Properties["mode"].Enum, []string{"preview", "publish", "cleanup"}) {
		t.Fatalf("local_expert_change mode enum = %v", schema.Properties["mode"].Enum)
	}
	if !sameStrings(schema.Required, []string{"mode"}) {
		t.Fatalf("local_expert_change required = %v", schema.Required)
	}
	if _, ok := schema.Properties["preview_receipt"]; !ok {
		t.Fatal("local_expert_change schema missing preview_receipt")
	}
	if _, ok := schema.Properties["prepare_evaluation"]; !ok {
		t.Fatal("local_expert_change schema missing prepare_evaluation")
	}
	evaluationDescription := schema.Properties["prepare_evaluation"].Description
	if !strings.Contains(evaluationDescription, "默认 false") ||
		!strings.Contains(evaluationDescription, "本次候选明确要求") ||
		!strings.Contains(evaluationDescription, "Profile/Skill 本身") ||
		!strings.Contains(evaluationDescription, "case 看起来只读") ||
		!strings.Contains(evaluationDescription, "副作用") ||
		!strings.Contains(evaluationDescription, "新的明确要求") ||
		!strings.Contains(evaluationDescription, "确认") {
		t.Fatalf("prepare_evaluation description = %q, want opt-in and side-effect confirmation rules", evaluationDescription)
	}
}

func TestMCPToolSchemaRequiresCompleteLocalExpertChange(t *testing.T) {
	changeTool := findMCPTool(t, amuxTools(), "amux.local_expert_change")
	var schema struct {
		AllOf []struct {
			If struct {
				Properties map[string]struct {
					Const string   `json:"const"`
					Enum  []string `json:"enum"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Required []string `json:"required"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal(changeTool.InputSchema, &schema); err != nil {
		t.Fatalf("local_expert_change schema parse: %v", err)
	}
	wantByMode := map[string][]string{
		"preview": {"operation", "target_ref", "base_revision", "patch"},
		"publish": {"operation", "target_ref", "base_revision", "patch", "preview_receipt"},
		"cleanup": {"preview_receipt"},
	}
	gotByMode := make(map[string][]string)
	var forkRequired []string
	for _, condition := range schema.AllOf {
		mode := condition.If.Properties["mode"]
		if mode.Const != "" {
			gotByMode[mode.Const] = condition.Then.Required
		}
		if operation := condition.If.Properties["operation"]; operation.Const == "fork" {
			forkRequired = condition.Then.Required
		}
	}
	for mode, want := range wantByMode {
		if !sameStrings(gotByMode[mode], want) {
			t.Fatalf("local_expert_change %s required = %v, want %v", mode, gotByMode[mode], want)
		}
	}
	if !sameStrings(forkRequired, []string{"source_ref"}) {
		t.Fatalf("local_expert_change fork required = %v, want source_ref", forkRequired)
	}
	for _, requiredGuidance := range []string{"operation", "target_ref", "source_ref", "base_revision", "patch", "preview_receipt", "cleanup 不撤销 receipt", "中间普通对话不会撤销 receipt"} {
		if !strings.Contains(localExpertPreviewNextStep, requiredGuidance) {
			t.Fatalf("local expert preview next_step missing %q: %q", requiredGuidance, localExpertPreviewNextStep)
		}
	}
}

func TestMCPToolsIncludeScheduleTools(t *testing.T) {
	tools := amuxTools()
	byName := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = struct{}{}
	}
	for _, name := range []string{
		"amux.schedule_create",
		"amux.schedule_list",
		"amux.schedule_get",
		"amux.schedule_delete",
		"amux.schedule_update",
		"amux.schedule_set_enabled",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing MCP tool %s", name)
		}
	}
}

func TestMCPScheduleListSchemaRequiresReadingAllPages(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.schedule_list")
	if !strings.Contains(tool.Description, "next_cursor") || !strings.Contains(tool.Description, "has_more=false") {
		t.Fatalf("schedule_list description = %q, want pagination guidance", tool.Description)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("schedule_list schema parse: %v", err)
	}
	cursor, ok := schema.Properties["cursor"]
	if !ok || cursor.Type != "string" || !strings.Contains(cursor.Description, "上一页 schedule_list") {
		t.Fatalf("schedule_list cursor schema = %+v, want string continuation cursor", cursor)
	}
}

func TestMCPToolsIncludeAgentLevelTools(t *testing.T) {
	tools := amuxTools()
	byName := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = struct{}{}
	}
	for _, name := range []string{
		"amux.instance_spawn",
		"amux.instance_send",
		"amux.instance_steer",
		"amux.instance_followup",
		"amux.instance_wait",
		"amux.instance_list",
		"amux.instance_interrupt",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing agent-level MCP tool %s", name)
		}
	}
}

func TestMCPToolsIncludeTaskListTool(t *testing.T) {
	tools := amuxTools()
	byName := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = struct{}{}
	}
	if _, ok := byName["amux.task_list"]; !ok {
		t.Fatalf("missing MCP tool amux.task_list")
	}
}

func TestMCPToolsIncludeTaskPruneTool(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.task_prune")
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("task_prune schema parse: %v", err)
	}
	for _, prop := range []string{"dry_run", "older_than_seconds", "statuses"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Fatalf("task_prune schema missing %s property", prop)
		}
	}
}

var retiredMCPToolRenames = map[string]string{ // retired name -> canonical name
	"amux.list_agent_profiles":      "amux.profile_list",
	"amux.submit_agent":             "amux.task_submit",
	"amux.run_agent":                "amux.task_run",
	"amux.get_agent":                "amux.task_get",
	"amux.stream_agent":             "amux.task_stream",
	"amux.cancel_agent":             "amux.task_cancel",
	"amux.respond_agent_permission": "amux.task_respond_permission",
}

func TestMCPToolsAdvertiseCanonicalNamesOnly(t *testing.T) {
	tools := amuxTools()
	byName := make(map[string]mcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	// Retired pre-domain names stay absent from tools/list.
	for oldName, newName := range retiredMCPToolRenames {
		newTool, ok := byName[newName]
		if !ok {
			t.Fatalf("missing primary MCP tool %s", newName)
		}
		if strings.Contains(newTool.Description, "已弃用") {
			t.Fatalf("canonical MCP tool %s must not be marked deprecated: %q", newName, newTool.Description)
		}
		if _, ok := byName[oldName]; ok {
			t.Fatalf("retired MCP tool %s must not be advertised", oldName)
		}
	}
	removedNames := []string{
		"amux.list_subagents",
		"amux.submit_subtask",
		"amux.run_subtask",
		"amux.get_subtask",
		"amux.stream_subtask",
		"amux.cancel_subtask",
		"amux.respond_permission",
	}
	for _, name := range removedNames {
		if _, ok := byName[name]; ok {
			t.Fatalf("removed MCP tool name %s must not be advertised in tools/list", name)
		}
	}
}

func TestMCPTaskSubmitSchemaHasModeEnum(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.task_submit")
	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("submit schema parse: %v", err)
	}
	mode, ok := schema.Properties["mode"]
	if !ok {
		t.Fatalf("submit schema missing mode property")
	}
	if !sameStrings(mode.Enum, []string{"ephemeral", "persistent"}) {
		t.Fatalf("mode enum = %v, want ephemeral/persistent", mode.Enum)
	}
	if _, ok := schema.Properties["agent_instance_id"]; !ok {
		t.Fatalf("submit schema missing agent_instance_id property")
	}
}

// TestMCPSubmitSchemaHasResultGranularityEnum verifies the result_granularity
// field is exposed on submit/spawn/followup so the main agent can request the
// full transcript instead of only the summary.
func TestMCPSubmitSchemaHasResultGranularityEnum(t *testing.T) {
	for _, name := range []string{"amux.task_submit", "amux.instance_spawn", "amux.instance_followup"} {
		tool := findMCPTool(t, amuxTools(), name)
		var schema struct {
			Properties map[string]struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s schema parse: %v", name, err)
		}
		g, ok := schema.Properties["result_granularity"]
		if !ok {
			t.Fatalf("%s schema missing result_granularity property", name)
		}
		if !sameStrings(g.Enum, []string{"summary", "full", "structured-summary"}) {
			t.Fatalf("%s result_granularity enum = %v, want summary/full/structured-summary", name, g.Enum)
		}
	}
}

// TestMCPTaskViewExposesFullTranscriptAndDetailsRef verifies taskView surfaces
// the inlined full transcript (full mode) and the details_ref (structured-summary
// mode) so the main agent can read the complete conclusion.
func TestMCPTaskViewExposesFullTranscriptAndDetailsRef(t *testing.T) {
	run := &task.TaskRun{ID: "task-1", Status: task.StatusSucceeded, Result: &task.TaskResult{
		Status:         task.ResultOK,
		Summary:        "did it",
		FullTranscript: "the full agent transcript",
		DetailsRef:     "task-1",
	}}
	view := taskView(run)
	res, ok := view["result"].(map[string]any)
	if !ok {
		t.Fatalf("result view missing: %#v", view)
	}
	if res["full_transcript"] != "the full agent transcript" {
		t.Fatalf("full_transcript not exposed: %v", res["full_transcript"])
	}
	if res["details_ref"] != "task-1" {
		t.Fatalf("details_ref not exposed: %v", res["details_ref"])
	}
}

// TestMCPTaskViewOmitsEmptyTranscriptFields: when neither field is set (summary
// mode), taskView must not add empty keys.
func TestMCPTaskViewOmitsEmptyTranscriptFields(t *testing.T) {
	run := &task.TaskRun{ID: "task-1", Status: task.StatusSucceeded, Result: &task.TaskResult{
		Status:  task.ResultOK,
		Summary: "done",
	}}
	view := taskView(run)
	res := view["result"].(map[string]any)
	if _, ok := res["full_transcript"]; ok {
		t.Fatalf("full_transcript should be omitted when empty")
	}
	if _, ok := res["details_ref"]; ok {
		t.Fatalf("details_ref should be omitted when empty")
	}
}

func TestMCPSpawnAgentGeneratesInstanceIDAndPersistentMode(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent":"builtin/codex","task":"do work"}`)
	res, err := h.spawnAgent(context.Background(), args)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	if res.IsError {
		t.Fatalf("spawnAgent error: %s", res.Content[0].Text)
	}
	var spec task.Spec
	if err := json.Unmarshal(cap.specJSON, &spec); err != nil {
		t.Fatalf("decode captured spec: %v", err)
	}
	if spec.Mode != task.ModePersistent {
		t.Fatalf("mode = %q, want persistent", spec.Mode)
	}
	if spec.AgentInstanceID == "" || !strings.HasPrefix(spec.AgentInstanceID, "agent-") {
		t.Fatalf("agent_instance_id = %q, want agent-<uuid>", spec.AgentInstanceID)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("decode spawn result: %v", err)
	}
	if out["agent_instance_id"] != spec.AgentInstanceID {
		t.Fatalf("result agent_instance_id = %v, want %q", out["agent_instance_id"], spec.AgentInstanceID)
	}
	if out["task_id"] != "t1" {
		t.Fatalf("result task_id = %v, want t1", out["task_id"])
	}
}

func TestMCPRunAgentCancelsSubmittedTaskWhenRequestIsCancelled(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan mcp.ToolResult, 1)
	go func() {
		res, _ := h.submit(ctx, json.RawMessage(`{"agent":"builtin/codex","task":"do work"}`), true)
		done <- res
	}()

	select {
	case <-cap.streamStarted:
	case <-time.After(time.Second):
		t.Fatal("run_agent did not start streaming")
	}
	cancel()
	select {
	case res := <-done:
		if !res.IsError {
			t.Fatalf("cancelled run_agent result = %+v, want error", res)
		}
	case <-time.After(time.Second):
		t.Fatal("run_agent did not return after cancellation")
	}
	if cap.cancelReq == nil || cap.cancelReq.Id != "t1" {
		t.Fatalf("CancelSubtask request = %+v, want task t1", cap.cancelReq)
	}
}

func TestMCPSpawnAgentHonorsExplicitInstanceID(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent":"builtin/codex","task":"do work","agent_instance_id":"agent-custom"}`)
	res, err := h.spawnAgent(context.Background(), args)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	if res.IsError {
		t.Fatalf("spawnAgent error: %s", res.Content[0].Text)
	}
	var spec task.Spec
	if err := json.Unmarshal(cap.specJSON, &spec); err != nil {
		t.Fatalf("decode captured spec: %v", err)
	}
	if spec.AgentInstanceID != "agent-custom" {
		t.Fatalf("agent_instance_id = %q, want agent-custom (explicit must win)", spec.AgentInstanceID)
	}
}

func TestMCPFollowupTaskRequiresInstanceID(t *testing.T) {
	h := &mcpHandler{}
	res, err := h.followupTask(context.Background(), json.RawMessage(`{"agent":"builtin/codex","task":"more"}`))
	if err != nil {
		t.Fatalf("followupTask: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "agent_instance_id is required") {
		t.Fatalf("followupTask without instance id = %+v, want agent_instance_id required", res)
	}
}

func TestMCPFollowupTaskSubmitsPersistentWithInstanceID(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent_instance_id":"agent-1","task":"next turn"}`)
	res, err := h.followupTask(context.Background(), args)
	if err != nil {
		t.Fatalf("followupTask: %v", err)
	}
	if res.IsError {
		t.Fatalf("followupTask error: %s", res.Content[0].Text)
	}
	var spec task.Spec
	if err := json.Unmarshal(cap.specJSON, &spec); err != nil {
		t.Fatalf("decode captured spec: %v", err)
	}
	if spec.Mode != task.ModePersistent {
		t.Fatalf("mode = %q, want persistent", spec.Mode)
	}
	if spec.AgentInstanceID != "agent-1" {
		t.Fatalf("agent_instance_id = %q, want agent-1", spec.AgentInstanceID)
	}
	if spec.Task != "next turn" {
		t.Fatalf("task = %q, want next turn", spec.Task)
	}
}

func TestMCPSteerAgentRoutesToClient(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	steer := func(args string) map[string]any {
		t.Helper()
		res, err := h.handle(context.Background(), "amux.instance_steer", json.RawMessage(args))
		if err != nil || res.IsError {
			t.Fatalf("instance_steer = %+v, %v", res, err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		return out
	}

	cap.sendMsgResp = &pb.SendAgentMessageResponse{Ok: true, Delivery: "steered"}
	out := steer(`{"agent_instance_id":"agent-1","text":"use dry-run"}`)
	if req := cap.sendMsgReq; req == nil || req.Id != "agent-1" || req.Text != "use dry-run" || !req.Steer || req.Source != "coordinator" {
		t.Fatalf("sendMsgReq = %+v, want a coordinator steer for agent-1", req)
	}
	if out["delivery"] != "steered" || out["hint"] != nil {
		t.Fatalf("steered result = %+v", out)
	}

	cap.sendMsgResp = &pb.SendAgentMessageResponse{Ok: true, Delivery: "mailbox", PendingMessages: 2, SteerFallbackReason: "no active turn"}
	out = steer(`{"agent_instance_id":"agent-1","text":"use dry-run"}`)
	if out["delivery"] != "mailbox" || out["fallback_reason"] != "no active turn" || out["pending_messages"] != float64(2) {
		t.Fatalf("mailbox result = %+v", out)
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "instance_followup") {
		t.Fatalf("mailbox hint = %q, want instance_followup guidance", hint)
	}

	res, err := h.handle(context.Background(), "amux.instance_steer", json.RawMessage(`{"agent_instance_id":"agent-1"}`))
	if err != nil || !res.IsError {
		t.Fatalf("missing text = %+v, %v; want a tool error", res, err)
	}
}

func TestMCPSendMessageRoutesToClient(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent_instance_id":"agent-1","text":"hello","source":"test"}`)
	res, err := h.sendMessage(context.Background(), args)
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if res.IsError {
		t.Fatalf("sendMessage error: %s", res.Content[0].Text)
	}
	if cap.sendMsgReq == nil || cap.sendMsgReq.Id != "agent-1" || cap.sendMsgReq.Text != "hello" {
		t.Fatalf("sendMsgReq = %+v, want id=agent-1 text=hello", cap.sendMsgReq)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out["queued"] != true {
		t.Fatalf("send_message result = %+v, want queued=true", out)
	}
	if got, ok := out["pending_messages"].(float64); !ok || got != 1 {
		t.Fatalf("pending_messages = %+v, want 1", out["pending_messages"])
	}
}

func TestMCPSendMessageRequiresText(t *testing.T) {
	h := &mcpHandler{}
	res, err := h.sendMessage(context.Background(), json.RawMessage(`{"agent_instance_id":"agent-1"}`))
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "text is required") {
		t.Fatalf("sendMessage without text = %+v, want text required", res)
	}
}

func TestMCPListAgentsRoutesToClient(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"status":"idle"}`)
	res, err := h.listAgents(context.Background(), args)
	if err != nil {
		t.Fatalf("listAgents: %v", err)
	}
	if res.IsError {
		t.Fatalf("listAgents error: %s", res.Content[0].Text)
	}
	if cap.listInstancesReq == nil || cap.listInstancesReq.Status != "idle" {
		t.Fatalf("listInstancesReq = %+v, want status=idle", cap.listInstancesReq)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	insts, ok := out["instances"].([]any)
	if !ok || len(insts) != 1 {
		t.Fatalf("instances = %+v, want one entry", out["instances"])
	}
	if insts[0].(map[string]any)["permission_policy"] != "deny-all" {
		t.Fatalf("instance lost permission policy: %+v", insts[0])
	}
}

func TestMCPInterruptAgentRoutesToClient(t *testing.T) {
	cap := withTaskClient(t)
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent_instance_id":"agent-1"}`)
	res, err := h.interruptAgent(context.Background(), args)
	if err != nil {
		t.Fatalf("interruptAgent: %v", err)
	}
	if res.IsError {
		t.Fatalf("interruptAgent error: %s", res.Content[0].Text)
	}
	if cap.interruptReq == nil || cap.interruptReq.Id != "agent-1" {
		t.Fatalf("interruptReq = %+v, want id=agent-1", cap.interruptReq)
	}
}

func TestMCPWaitAgentRoutesToClient(t *testing.T) {
	cap := withTaskClient(t)
	// Even if an older or alternate server reports activity while the turn is
	// still active, the join contract has only two outcomes: completed or timed
	// out. Activity is compatibility metadata, not a third terminal outcome.
	cap.waitResp = &pb.WaitAgentInstanceResponse{Activity: true, State: "running"}
	h := &mcpHandler{}
	args := json.RawMessage(`{"agent_instance_id":"agent-1","timeout_seconds":5}`)
	res, err := h.waitAgent(context.Background(), args)
	if err != nil {
		t.Fatalf("waitAgent: %v", err)
	}
	if res.IsError {
		t.Fatalf("waitAgent error: %s", res.Content[0].Text)
	}
	if cap.waitReq == nil || cap.waitReq.Id != "agent-1" || cap.waitReq.TimeoutSeconds != 5 {
		t.Fatalf("waitReq = %+v, want id=agent-1 timeout=5", cap.waitReq)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode waitAgent response: %v", err)
	}
	if got["completed"] != false || got["timed_out"] != true {
		t.Fatalf("waitAgent response = %+v, want completed=false timed_out=true", got)
	}
	if got["activity"] != true {
		t.Fatalf("waitAgent response = %+v, want compatibility activity=true", got)
	}
}

func TestMCPHandleDispatchesAgentLevelTools(t *testing.T) {
	withTaskClient(t)
	h := &mcpHandler{}
	for _, tc := range []struct {
		name string
		args string
	}{
		{"amux.instance_spawn", `{"agent":"builtin/codex","task":"x"}`},
		{"amux.instance_send", `{"agent_instance_id":"agent-1","text":"x"}`},
		{"amux.instance_steer", `{"agent_instance_id":"agent-1","text":"x"}`},
		{"amux.instance_followup", `{"agent_instance_id":"agent-1","task":"x"}`},
		{"amux.instance_wait", `{"agent_instance_id":"agent-1"}`},
		{"amux.instance_list", `{}`},
		{"amux.instance_interrupt", `{"agent_instance_id":"agent-1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := h.handle(context.Background(), tc.name, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("handle %s: %v", tc.name, err)
			}
			if res.IsError {
				t.Fatalf("handle %s error: %s", tc.name, res.Content[0].Text)
			}
		})
	}
}

func TestMCPHandleDispatchesAgentPrimaryNames(t *testing.T) {
	withTaskClient(t)
	useLocalAgentProfileClient(t)
	h := &mcpHandler{}
	for _, name := range []string{
		"amux.profile_list",
	} {
		t.Run(name, func(t *testing.T) {
			res, err := h.handle(context.Background(), name, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("handle %s: %v", name, err)
			}
			if res.IsError {
				t.Fatalf("handle %s error: %s", name, res.Content[0].Text)
			}
			if strings.Contains(res.Content[0].Text, "unknown tool") {
				t.Fatalf("handle %s routed to unknown tool: %s", name, res.Content[0].Text)
			}
		})
	}
}

func TestMCPHandleRejectsUnlistedBareToolNames(t *testing.T) {
	h := &mcpHandler{}
	for _, name := range []string{
		"list_agent_profiles", "get_agent_profile",
		"submit_agent", "run_agent", "get_agent", "stream_agent", "cancel_agent", "respond_agent_permission",
		"create_schedule", "list_schedules", "get_schedule", "delete_schedule", "update_schedule", "set_schedule_enabled",
		"spawn_agent", "send_message", "followup_task", "wait_agent", "list_agents", "interrupt_agent",
		"collaborator_list", "delegation_create", "delegation_get", "delegation_wait", "delegation_intervene", "delegation_cancel",
	} {
		t.Run(name, func(t *testing.T) {
			res, err := h.handle(context.Background(), name, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("handle %s: %v", name, err)
			}
			if !res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "unknown tool") {
				t.Fatalf("handle %s = %#v, want unknown tool error", name, res)
			}
		})
	}
}

func TestMCPHandleRejectsRemovedToolNames(t *testing.T) {
	// Only names advertised by tools/list are callable. Retired names must stay
	// rejected instead of silently preserving a second MCP vocabulary.
	h := &mcpHandler{}
	assertRejected := func(t *testing.T, name string) {
		t.Helper()
		res, err := h.handle(context.Background(), name, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("handle %s: %v", name, err)
		}
		if !res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "unknown tool") {
			t.Fatalf("handle %s = %#v, want unknown tool error", name, res)
		}
	}
	for name := range retiredMCPToolRenames {
		t.Run(name, func(t *testing.T) { assertRejected(t, name) })
	}
	for _, name := range []string{
		"amux.list_subagents", "list_subagents",
		"amux.submit_subtask", "submit_subtask",
		"amux.run_subtask", "run_subtask",
		"amux.get_subtask", "get_subtask",
		"amux.stream_subtask", "stream_subtask",
		"amux.cancel_subtask", "cancel_subtask",
		"amux.respond_permission", "respond_permission",
		"amux.get_agent_profile",
		"amux.spawn_agent", "amux.send_message", "amux.followup_task",
		"amux.wait_agent", "amux.list_agents", "amux.interrupt_agent",
		"amux.create_schedule", "amux.list_schedules", "amux.get_schedule",
		"amux.update_schedule", "amux.delete_schedule", "amux.set_schedule_enabled",
	} {
		t.Run(name, func(t *testing.T) { assertRejected(t, name) })
	}
}

func TestMCPToolSchemasOmitUnsupportedRepoFields(t *testing.T) {
	for _, toolName := range []string{"amux.task_submit", "amux.schedule_create"} {
		t.Run(toolName, func(t *testing.T) {
			tool := findMCPTool(t, amuxTools(), toolName)
			var raw map[string]any
			if err := json.Unmarshal(tool.InputSchema, &raw); err != nil {
				t.Fatalf("schema parse: %v", err)
			}
			schemaJSON := string(tool.InputSchema)
			for _, unsupported := range []string{"remote", "patch_ref", "subdir"} {
				if strings.Contains(schemaJSON, `"`+unsupported+`"`) {
					t.Fatalf("%s schema advertises unsupported repo field %q: %s", toolName, unsupported, schemaJSON)
				}
			}
			for _, supported := range []string{"kind", "cache_key", "commit", "local_dir"} {
				if !strings.Contains(schemaJSON, `"`+supported+`"`) {
					t.Fatalf("%s schema missing supported field %q: %s", toolName, supported, schemaJSON)
				}
			}
		})
	}
}

func TestMCPTaskSchemasDocumentWholeTaskTimeout(t *testing.T) {
	for _, toolName := range []string{"amux.task_submit", "amux.task_run", "amux.instance_spawn", "amux.instance_followup"} {
		t.Run(toolName, func(t *testing.T) {
			tool := findMCPTool(t, amuxTools(), toolName)
			var schema struct {
				Properties map[string]struct {
					Minimum     *float64 `json:"minimum"`
					Description string   `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("schema parse: %v", err)
			}
			timeout := schema.Properties["timeout_seconds"]
			if timeout.Minimum == nil || *timeout.Minimum != 0 {
				t.Fatalf("timeout minimum = %v, want 0", timeout.Minimum)
			}
			if !strings.Contains(timeout.Description, "整个 Task") || !strings.Contains(timeout.Description, "30 分钟") {
				t.Fatalf("timeout description = %q, want whole-task and default", timeout.Description)
			}
		})
	}
}

func TestMCPScheduleCreateSchemaDocumentsScheduledSubtaskPolicy(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.schedule_create")
	if !strings.Contains(tool.Description, "session-prompt") || !strings.Contains(tool.Description, "subtask") {
		t.Fatalf("schedule_create description = %q, want session-prompt and subtask modes", tool.Description)
	}
	if !strings.Contains(tool.Description, "escalate-to-main") || !strings.Contains(tool.Description, "不允许") {
		t.Fatalf("schedule_create description = %q, want escalate-to-main not allowed guidance", tool.Description)
	}

	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
			Properties  map[string]struct {
				Description string   `json:"description"`
				Enum        []string `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("create_schedule schema parse: %v", err)
	}
	if schema.Properties["task"].Type != "object" {
		t.Fatalf("task schema type = %q, want object", schema.Properties["task"].Type)
	}
	if got := schema.Properties["task"].Properties["permission_policy"].Enum; !sameStrings(got, []string{"default", "policy-allow", "deny-all"}) {
		t.Fatalf("permission_policy enum = %v, want default/policy-allow/deny-all", got)
	}
	if got := schema.Properties["schedule_type"].Enum; !sameStrings(got, []string{"subtask", "session-prompt"}) {
		t.Fatalf("schedule_type enum = %v, want subtask/session-prompt", got)
	}
	if !strings.Contains(schema.Properties["schedule_type"].Description, "session-prompt") {
		t.Fatalf("schedule_type description = %q, want session-prompt guidance", schema.Properties["schedule_type"].Description)
	}
	sessionProp, ok := schema.Properties["session"]
	if !ok || sessionProp.Type != "string" {
		t.Fatalf("session schema = %+v, want string property", sessionProp)
	}
	if !strings.Contains(sessionProp.Description, "session-ref") || !strings.Contains(sessionProp.Description, "AMUX_SESSION_ID") {
		t.Fatalf("session description = %q, want session-ref and AMUX_SESSION_ID fallback", sessionProp.Description)
	}
	if !containsString(schema.Required, "spec") || containsString(schema.Required, "task") || containsString(schema.Required, "session") {
		t.Fatalf("required = %v, want only spec (session optional with AMUX_SESSION_ID fallback)", schema.Required)
	}
}

func TestNormalizeMCPScheduleTypeDefaultsToSubtask(t *testing.T) {
	got, err := normalizeMCPScheduleType("")
	if err != nil || got != schedule.ScheduleTypeSubtask {
		t.Fatalf("got=%q err=%v, want subtask", got, err)
	}
	got, err = normalizeMCPScheduleType("session-prompt")
	if err != nil || got != schedule.ScheduleTypeSessionPrompt {
		t.Fatalf("got=%q err=%v, want session-prompt", got, err)
	}
	if _, err := normalizeMCPScheduleType("bogus"); err == nil {
		t.Fatal("bogus schedule_type accepted")
	}
}

func TestMCPScheduleCreateSessionPromptRequiresSessionRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	t.Setenv("AMUX_SESSION_ID", "")
	res, err := (&mcpHandler{}).createSchedule(context.Background(), json.RawMessage(`{
		"spec": "every 30m",
		"schedule_type": "session-prompt",
		"prompt": "status check"
	}`))
	if err != nil {
		t.Fatalf("createSchedule: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("result = %+v, want session-ref parameter error", res)
	}
	msg := res.Content[0].Text
	if !strings.Contains(msg, "session") || strings.Contains(msg, "hub is not reachable") {
		t.Fatalf("result = %+v, want session parameter error before hub dial", res)
	}
}

func TestMCPSessionPromptRefPrefersExplicitSession(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "env-session")
	got, err := mcpSessionPromptRef(mcpCreateScheduleArgs{Session: " local/main "})
	if err != nil || got != "local/main" {
		t.Fatalf("got=%q err=%v, want local/main", got, err)
	}
}

func TestMCPSessionPromptRefFallsBackToEnv(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "  env-session  ")
	got, err := mcpSessionPromptRef(mcpCreateScheduleArgs{})
	if err != nil || got != "env-session" {
		t.Fatalf("got=%q err=%v, want env-session", got, err)
	}
}

func TestMCPScheduleCreateSessionPromptRequiresPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	t.Setenv("AMUX_SESSION_ID", "")
	res, err := (&mcpHandler{}).createSchedule(context.Background(), json.RawMessage(`{
		"spec": "every 30m",
		"schedule_type": "session-prompt",
		"session": "local/main"
	}`))
	if err != nil {
		t.Fatalf("createSchedule: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "prompt") {
		t.Fatalf("result = %+v, want prompt error before hub dial", res)
	}
	if strings.Contains(res.Content[0].Text, "hub is not reachable") {
		t.Fatalf("result = %+v, dialed hub before prompt validation", res)
	}
}

func TestMCPScheduleCreateRejectsUnknownScheduleType(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	res, err := (&mcpHandler{}).createSchedule(context.Background(), json.RawMessage(`{
		"spec": "every 30m",
		"schedule_type": "bogus",
		"prompt": "x"
	}`))
	if err != nil {
		t.Fatalf("createSchedule: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "schedule_type") {
		t.Fatalf("result = %+v, want schedule_type error", res)
	}
}

func TestMCPSetScheduleEnabledRequiresEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	res, err := (&mcpHandler{}).setScheduleEnabled(context.Background(), json.RawMessage(`{"schedule_id":1}`))
	if err != nil {
		t.Fatalf("setScheduleEnabled: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "enabled is required") {
		t.Fatalf("setScheduleEnabled result = %+v, want enabled is required error", res)
	}
}

func TestMCPScheduleViewOmitsRawTaskSpecJsonAndDecodesTask(t *testing.T) {
	specJSON := []byte(`{"agent":"builtin/codex","task":"review","permission_policy":"deny-all","trusted_mode_ack":true}`)
	item := &pb.ScheduleItem{
		Id:           42,
		Name:         "review schedule",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         schedule.KindInterval,
		Enabled:      true,
		NextRunAt:    1700000000,
		LastRunAt:    1700000300,
		LastStatus:   "ok:task:task-1",
		TaskSpecJson: specJSON,
		Runs: []*pb.ScheduleRun{{
			Id:         7,
			ScheduleId: 42,
			FiredAt:    1700000300,
			TaskId:     "task-1",
			Status:     "ok",
		}},
	}

	view := scheduleItemMCPView(item)
	if _, ok := view["task_spec_json"]; ok {
		t.Fatalf("schedule view leaked raw task_spec_json: %#v", view)
	}
	if view["id"] != int64(42) || view["name"] != "review schedule" || view["schedule_type"] != schedule.ScheduleTypeSubtask {
		t.Fatalf("basic view fields = %#v", view)
	}
	taskView, ok := view["task"].(map[string]any)
	if !ok {
		t.Fatalf("task view = %#v, want map", view["task"])
	}
	if taskView["agent"] != "builtin/codex" || taskView["task"] != "review" || taskView["permission_policy"] != task.PolicyDenyAll {
		t.Fatalf("decoded task view = %#v", taskView)
	}
	if _, ok := taskView["trusted_mode_ack"]; ok {
		t.Fatalf("decoded task view retained legacy trusted_mode_ack: %#v", taskView)
	}
	runs, ok := view["runs"].([]map[string]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("runs view = %#v, want one run", view["runs"])
	}
	if runs[0]["fired_at"] != int64(1700000300) || runs[0]["task_id"] != "task-1" || runs[0]["status"] != "ok" {
		t.Fatalf("run view = %#v", runs[0])
	}

	wrapped, err := listSchedulesMCPView([]*pb.ScheduleItem{item}, "")
	if err != nil {
		t.Fatalf("listSchedulesMCPView: %v", err)
	}
	if wrapped["schema_version"] != "mcp.list_schedules.v2" || wrapped["page_count"] != 1 || wrapped["total_count"] != 1 || wrapped["has_more"] != false {
		t.Fatalf("list schedules metadata = %#v", wrapped)
	}
	schedules, ok := wrapped["schedules"].([]map[string]any)
	if !ok || len(schedules) != 1 {
		t.Fatalf("list schedules wrapper = %#v", wrapped)
	}
	row := schedules[0]
	// The list view must stay compact: no full task prompt, no run history,
	// just triage fields plus the owning agent ref.
	if row["id"] != int64(42) || row["name"] != "review schedule" || row["spec"] != "every 30m" || row["enabled"] != true {
		t.Fatalf("list summary fields = %#v", row)
	}
	if row["agent"] != "builtin/codex" {
		t.Fatalf("list summary should surface agent ref, got = %#v", row)
	}
	if _, hasTask := row["task"]; hasTask {
		t.Fatalf("list summary must not embed full task prompt: %#v", row)
	}
	if _, hasRuns := row["runs"]; hasRuns {
		t.Fatalf("list summary must not embed run history: %#v", row)
	}
	gotJSON := toJSON(wrapped)
	if strings.Contains(gotJSON, "task_spec_json") {
		t.Fatalf("MCP schedule JSON leaked raw task spec bytes: %s", gotJSON)
	}
	if strings.Contains(gotJSON, `"task":"review"`) {
		t.Fatalf("MCP schedule list JSON leaked task prompt, should stay compact: %s", gotJSON)
	}
}

func TestMCPScheduleListPagesWithoutLosingSchedules(t *testing.T) {
	items := make([]*pb.ScheduleItem, 0, 23)
	for i := 0; i < 23; i++ {
		items = append(items, &pb.ScheduleItem{
			Id:      int64(i + 1),
			Name:    fmt.Sprintf("schedule-%02d", i+1),
			Spec:    "every 1h",
			Kind:    schedule.KindInterval,
			Enabled: true,
		})
	}
	var cursor string
	var ids []int64
	for pageNumber := 0; ; pageNumber++ {
		view, err := listSchedulesMCPView(items, cursor)
		if err != nil {
			t.Fatalf("schedule page %d: %v", pageNumber, err)
		}
		if view["schema_version"] != "mcp.list_schedules.v2" || view["total_count"] != 23 {
			t.Fatalf("schedule page %d metadata = %#v", pageNumber, view)
		}
		page := view["schedules"].([]map[string]any)
		if len(page) > mcpScheduleListPageSize {
			t.Fatalf("schedule page %d count = %d, want <= %d", pageNumber, len(page), mcpScheduleListPageSize)
		}
		for _, row := range page {
			ids = append(ids, row["id"].(int64))
		}
		hasMore := view["has_more"].(bool)
		if !hasMore {
			if _, ok := view["next_cursor"]; ok {
				t.Fatalf("final schedule page must not include next_cursor: %#v", view)
			}
			break
		}
		nextCursor, ok := view["next_cursor"].(string)
		if !ok || nextCursor == "" {
			t.Fatalf("schedule page %d missing next_cursor: %#v", pageNumber, view)
		}
		if !strings.Contains(view["pagination_instruction"].(string), "has_more=false") {
			t.Fatalf("schedule page %d missing complete-read instruction: %#v", pageNumber, view)
		}
		cursor = nextCursor
	}
	if len(ids) != 23 {
		t.Fatalf("paged schedule count = %d, want 23: %v", len(ids), ids)
	}
	for i, id := range ids {
		if id != int64(i+1) {
			t.Fatalf("schedule order at %d = %d, want %d", i, id, i+1)
		}
	}
}

func TestMCPScheduleListRejectsInvalidAndPastEndCursors(t *testing.T) {
	items := []*pb.ScheduleItem{{Id: 1}}
	for _, tc := range []struct {
		name   string
		cursor string
		want   string
	}{
		{name: "invalid", cursor: "not-a-cursor", want: "invalid cursor"},
		{name: "negative", cursor: "-1", want: "invalid cursor"},
		{name: "past end", cursor: "2", want: "past the end of the schedule list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := listSchedulesMCPView(items, tc.cursor)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("cursor %q error = %v, want %q", tc.cursor, err, tc.want)
			}
		})
	}
}

func TestMCPScheduleViewIncludesSessionPromptFields(t *testing.T) {
	view := scheduleItemMCPView(&pb.ScheduleItem{
		Id:           12,
		Name:         "standup",
		ScheduleType: schedule.ScheduleTypeSessionPrompt,
		TargetId:     "target-1",
		TargetName:   "local",
		SessionName:  "work",
		Spec:         "daily 09:00",
		Kind:         schedule.KindDaily,
		RunMode:      schedule.RunModeReuseSession,
		Prompt:       "start standup",
		Enabled:      true,
	})
	if _, ok := view["task_spec_json"]; ok {
		t.Fatalf("session prompt view leaked task_spec_json: %#v", view)
	}
	if view["target_id"] != "target-1" || view["target_name"] != "local" || view["session_name"] != "work" || view["prompt"] != "start standup" || view["run_mode"] != schedule.RunModeReuseSession {
		t.Fatalf("session prompt fields = %#v", view)
	}
}

func TestMCPCommandRejectsWhenDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	writeUserConfig(t, home, `[mcp]
enabled = false
`)

	cmd := newMcpCmd()
	cmd.SetArgs(nil)
	cmd.SetIn(strings.NewReader(""))
	var out strings.Builder
	cmd.SetOut(&out)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "MCP is disabled") {
		t.Fatalf("mcp disabled error = %v, want clear disabled error", err)
	}
}

func TestMCPHelpDescribesOnDemandBackend(t *testing.T) {
	cmd := newMcpCmd()
	if !strings.Contains(cmd.Long, "stdio") || !strings.Contains(cmd.Long, "uses the hub") || !strings.Contains(cmd.Long, "CoordinationService") || !strings.Contains(cmd.Long, "task control plane") {
		t.Fatalf("mcp help = %q, want stdio/hub coordination and task backend explanation", cmd.Long)
	}
	if strings.Contains(cmd.Long, "local runtime") || strings.Contains(cmd.Long, "Phase 1") {
		t.Fatalf("mcp help should not mention stale local fallback wording: %q", cmd.Long)
	}
	if strings.Contains(strings.ToLower(cmd.Long), "legacy tool alias") {
		t.Fatalf("mcp help should not claim legacy tool aliases remain callable: %q", cmd.Long)
	}
	for _, removedName := range []string{"submit_subtask", "stream_subtask", "run_subtask", "get_subtask", "cancel_subtask", "respond_permission", "list_subagents"} {
		// Match removed names as whole tool tokens so a canonical name that
		// merely contains one as a substring (e.g. task_respond_permission)
		// does not trip the guard.
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(removedName) + `\b`).MatchString(cmd.Long) {
			t.Fatalf("mcp help should not advertise removed tool name %q: %q", removedName, cmd.Long)
		}
	}
}

type mcpAgentProfilesView struct {
	SchemaVersion      string                    `json:"schema_version"`
	Profiles           []mcpProfileView          `json:"profiles"`
	CapabilitySets     map[string]map[string]any `json:"capability_sets"`
	ProfilesScope      string                    `json:"profiles_scope"`
	NextCursor         string                    `json:"next_cursor"`
	HasMore            bool                      `json:"has_more"`
	TotalCount         int                       `json:"total_count"`
	SelectionGuidance  string                    `json:"selection_guidance"`
	SubmissionContract mcpSubmissionContract     `json:"submission_contract"`
}

func writeUserConfig(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

type mcpSubmissionContract struct {
	PreferredValue string `json:"preferred_value"`
	AgentField     string `json:"agent_field"`
}

type mcpProfileView struct {
	Ref                 string         `json:"ref"`
	PinnedRef           string         `json:"pinned_ref"`
	AgentType           string         `json:"agent_type"`
	Source              string         `json:"source"`
	Writable            bool           `json:"writable"`
	Description         string         `json:"description"`
	DefaultModelRef     string         `json:"default_model_ref"`
	SupportedModels     []string       `json:"supported_models"`
	Skills              []mcpSkillView `json:"skills"`
	Capabilities        map[string]any `json:"capabilities"`
	ProfileCapabilities map[string]any `json:"profile_capabilities"`
	Submission          struct {
		Agent       string `json:"agent"`
		PinnedAgent string `json:"pinned_agent"`
	} `json:"submission"`
	Placement struct {
		Requires struct {
			RuntimeTools []string `json:"runtime_tools"`
		} `json:"requires"`
	} `json:"placement"`
}

type mcpSkillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func decodeMCPListAgentProfiles(t *testing.T, text string) mcpAgentProfilesView {
	t.Helper()
	var view mcpAgentProfilesView
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatalf("decode list_agent_profiles: %v\n%s", err, text)
	}
	return view
}

func assertMCPListAgentProfilesHasNoRawAgents(t *testing.T, text string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("decode list_agent_profiles raw: %v\n%s", err, text)
	}
	if _, ok := raw["agents"]; ok {
		t.Fatalf("list_agent_profiles returned top-level agents field; raw agent types must not be discovery choices: %s", text)
	}
}

func findMCPProfile(t *testing.T, profiles []mcpProfileView, ref string) mcpProfileView {
	t.Helper()
	for _, profile := range profiles {
		if profile.Ref == ref {
			return profile
		}
	}
	t.Fatalf("missing profile %s in %+v", ref, profiles)
	return mcpProfileView{}
}

func findMCPSkill(skills []mcpSkillView, name string) (mcpSkillView, bool) {
	for _, skill := range skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return mcpSkillView{}, false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func findMCPTool(t *testing.T, tools []mcp.Tool, name string) mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %s", name)
	return mcp.Tool{}
}

func mustMCPScheduleTaskSpecJson(t *testing.T, spec task.Spec) []byte {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal task spec: %v", err)
	}
	return b
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, value := range got {
		seen[value]++
	}
	for _, value := range want {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

func TestFillParentSessionFromEnv(t *testing.T) {
	t.Run("fills from env when not set", func(t *testing.T) {
		t.Setenv("AMUX_SESSION_ID", "sess-abc123")
		spec := task.Spec{Agent: "builtin/codex", Task: "do work"}
		fillParentSessionFromEnv(&spec)
		if spec.ParentSessionID != "sess-abc123" {
			t.Fatalf("ParentSessionID = %q, want sess-abc123", spec.ParentSessionID)
		}
	})

	t.Run("explicit value is not overwritten", func(t *testing.T) {
		t.Setenv("AMUX_SESSION_ID", "sess-from-env")
		spec := task.Spec{Agent: "builtin/codex", Task: "do work", ParentSessionID: "sess-explicit"}
		fillParentSessionFromEnv(&spec)
		if spec.ParentSessionID != "sess-explicit" {
			t.Fatalf("ParentSessionID = %q, want sess-explicit (explicit must win)", spec.ParentSessionID)
		}
	})

	t.Run("no env var leaves empty", func(t *testing.T) {
		t.Setenv("AMUX_SESSION_ID", "")
		spec := task.Spec{Agent: "builtin/codex", Task: "do work"}
		fillParentSessionFromEnv(&spec)
		if spec.ParentSessionID != "" {
			t.Fatalf("ParentSessionID = %q, want empty when env unset", spec.ParentSessionID)
		}
	})

	t.Run("trims whitespace from env", func(t *testing.T) {
		t.Setenv("AMUX_SESSION_ID", "  sess-trimmed  ")
		spec := task.Spec{Agent: "builtin/codex", Task: "do work"}
		fillParentSessionFromEnv(&spec)
		if spec.ParentSessionID != "sess-trimmed" {
			t.Fatalf("ParentSessionID = %q, want sess-trimmed", spec.ParentSessionID)
		}
	})
}
