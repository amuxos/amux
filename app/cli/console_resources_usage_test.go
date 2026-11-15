package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// consoleUsageHub adds schedules and several experts to the shared Hub fake.
type consoleUsageHub struct {
	*consoleResourceHub
	profiles          []agentprofile.Profile
	schedules         []*pb.ScheduleItem
	schedulesErr      error
	referencesPayload string
	referencesErr     error
}

func (h *consoleUsageHub) ConsoleManagement(ctx context.Context, req *pb.ConsoleManagementRequest, opts ...grpc.CallOption) (*pb.ConsoleManagementResponse, error) {
	if req.GetResource() == "bots" && req.GetAction() == "references" {
		if h.referencesErr != nil {
			return nil, h.referencesErr
		}
		return &pb.ConsoleManagementResponse{Status: "success", PayloadJson: []byte(h.referencesPayload)}, nil
	}
	return h.consoleResourceHub.ConsoleManagement(ctx, req, opts...)
}

func (h *consoleUsageHub) ListAgentProfiles(context.Context, *pb.ListAgentProfilesRequest, ...grpc.CallOption) (*pb.ListAgentProfilesResponse, error) {
	infos := make([]*pb.AgentProfileInfo, 0, len(h.profiles))
	for _, profile := range h.profiles {
		infos = append(infos, &pb.AgentProfileInfo{Profile: agentprofile.ToPBProfileSpec(profile)})
	}
	return &pb.ListAgentProfilesResponse{Profiles: infos}, nil
}

func (h *consoleUsageHub) ListSchedules(context.Context, *pb.ListSchedulesRequest, ...grpc.CallOption) (*pb.ListSchedulesResponse, error) {
	if h.schedulesErr != nil {
		return nil, h.schedulesErr
	}
	return &pb.ListSchedulesResponse{Schedules: h.schedules}, nil
}

type consoleUsageTasks struct {
	pb.AmuxTaskClient
	instances []*pb.AgentInstance
	err       error
	block     bool
}

func (f *consoleUsageTasks) ListAgentInstances(ctx context.Context, _ *pb.ListAgentInstancesRequest, _ ...grpc.CallOption) (*pb.ListAgentInstancesResponse, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &pb.ListAgentInstancesResponse{Instances: f.instances}, nil
}

func consoleTestExpert(namespace, name string) agentprofile.Profile {
	profile, _ := agentprofile.ResolveBuiltin("builtin/codex")
	profile.Namespace, profile.Name = namespace, name
	return profile
}

const consolePin = "@0123456789abcdef0123"

// consoleUsageFixture is a small installation: two experts in the "team" Pack
// referenced by Sessions, a Bot, Schedules and a persistent instance.
func consoleUsageFixture(t *testing.T) (*consoleBackend, *consoleUsageHub, *consoleUsageTasks) {
	t.Helper()
	consoleTestOperator(t)
	base := consoleTestHub()
	base.botsPayload = `{"status":"success","bots":[{"profile":"default","id":"bot-1","name":"Helper","status":"running","settings":{"default_agent":"team/reviewer` + consolePin + `"}},{"profile":"second","id":"bot-2","name":"Other","status":"running","settings":{"default_agent":"builtin/codex"}}]}`
	base.targets = []*pb.Target{{Id: "runtime-a", Name: "worker", Sessions: []*pb.Session{
		{Id: "s1", Name: "review-1", State: "running", AgentProfile: "team/reviewer" + consolePin, LastActivityAt: 30},
		{Id: "s2", Name: "review-2", State: "sleeping", AgentProfile: "team/reviewer", LastActivityAt: 20},
		{Id: "s3", Name: "review-old", State: "archived", AgentProfile: "team/reviewer", LastActivityAt: 10},
		{Id: "s4", Name: "other-1", State: "running", AgentProfile: "team/other", LastActivityAt: 5},
		{Id: "s5", Name: "plain", State: "running"},
	}}}
	hub := &consoleUsageHub{consoleResourceHub: base,
		referencesPayload: `{"status":"success","references":[{"bridge_id":"bot-1","agent":"team/reviewer` + consolePin + `"},{"bridge_id":"bot-2","agent":"builtin/codex"}]}`,
		profiles:          []agentprofile.Profile{consoleTestExpert("team", "reviewer"), consoleTestExpert("team", "other"), consoleTestExpert("solo", "helper")},
		schedules: []*pb.ScheduleItem{
			{Id: 1, Name: "nightly review", ScheduleType: "subtask", Enabled: true, TaskSpecJson: []byte(`{"agent":"team/reviewer","task":"review"}`)},
			{Id: 2, Name: "paused other", ScheduleType: "subtask", Enabled: false, TaskSpecJson: []byte(`{"agent":"team/other","task":"x"}`)},
			{Id: 3, Name: "poke a session", ScheduleType: "session-prompt", Enabled: true},
			nil,
		}}
	tasks := &consoleUsageTasks{instances: []*pb.AgentInstance{
		{Id: "inst-1", Name: "reviewer-agent", Status: "active", AgentProfile: "team/reviewer" + consolePin, TargetId: "runtime-a"},
		{Id: "inst-2", Name: "gone", Status: "archived", AgentProfile: "team/reviewer"},
	}}
	return &consoleBackend{hub: hub, tasks: tasks}, hub, tasks
}

func TestConsoleUsageCountsWhatStartsWorkFromAnExpert(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{"ref": {"team/reviewer" + consolePin}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	// Pinned and unpinned references are the same expert; archived history is not use.
	for _, expected := range []string{`"ref":"team/reviewer"`, `"counts":{"sessions":2,"bots":1,"groups":0,"schedules":1,"instances":1}`, `"name":"review-1"`, `"name":"Helper"`, `"name":"nightly review"`, `"state":"enabled"`, `"name":"reviewer-agent"`, `"unavailable":[]`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing %s in %s", expected, raw)
		}
	}
	for _, unexpected := range []string{"review-old", "paused other", "poke a session", "gone", "other-1"} {
		if strings.Contains(raw, unexpected) {
			t.Fatalf("%s counted as use: %s", unexpected, raw)
		}
	}

	// A whole Pack namespace sums its experts and lists each one's counts.
	value, err = backend.snapshotResource(context.Background(), "usage", url.Values{"namespace": {"team"}})
	if err != nil {
		t.Fatal(err)
	}
	raw = consoleTestJSON(t, value)
	if !strings.Contains(raw, `"counts":{"sessions":3,"bots":1,"groups":0,"schedules":2,"instances":1}`) || !strings.Contains(raw, `"team/other":{"sessions":1,"bots":0,"groups":0,"schedules":1,"instances":0}`) {
		t.Fatalf("namespace usage = %s", raw)
	}
	// Without a filter every expert's counts are returned.
	value, err = backend.snapshotResource(context.Background(), "usage", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if raw = consoleTestJSON(t, value); !strings.Contains(raw, `"builtin/codex":{"sessions":0,"bots":1,"groups":0,"schedules":0,"instances":0}`) {
		t.Fatalf("all usage = %s", raw)
	}
	// An expert nothing refers to is an empty answer, not an error.
	value, err = backend.snapshotResource(context.Background(), "usage", url.Values{"ref": {"solo/helper"}})
	if err != nil || !strings.Contains(consoleTestJSON(t, value), `"counts":{"sessions":0,"bots":0,"groups":0,"schedules":0,"instances":0}`) {
		t.Fatalf("unused expert = %v %v", value, err)
	}
	_, err = backend.snapshotResource(context.Background(), "usage", url.Values{"namespace": {"../team"}})
	consoleTestCode(t, err, "invalid_field")
}

func TestConsoleUsageIncludesStoppedBotAndGroupDefaults(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	hub.referencesPayload = `{"status":"success","references":[{"bridge_id":"feishu:stopped","agent":"team/reviewer"},{"bridge_id":"feishu:stopped","chat_id":"oc-group","agent":"team/other"}]}`
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{"namespace": {"team"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	for _, expected := range []string{`"bots":1`, `"groups":1`, `"id":"feishu:stopped"`, `"id":"oc-group"`, `"team/other"`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing %s from %s", expected, raw)
		}
	}
	consoleRemoveRegistry(t)
	hub.schedules = nil
	backend.tasks = &consoleUsageTasks{}
	_, err = backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"team","confirm":true}`))
	consoleTestCode(t, err, "pack_in_use")
}

func TestConsoleUsageUnknownWhenBridgeReferencesCannotBeTrusted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		err     error
	}{
		{"bridge unavailable", "", status.Error(codes.Unavailable, "console_bridge_references_unavailable")},
		{"older hub without references", "", status.Error(codes.InvalidArgument, "console_unknown_action")},
		{"missing references array", `{"status":"success"}`, nil},
		{"null references array", `{"status":"success","references":null}`, nil},
		{"reference without bridge identity", `{"status":"success","references":[{"agent":"team/reviewer"}]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, hub, _ := consoleUsageFixture(t)
			hub.referencesPayload, hub.referencesErr = tc.payload, tc.err
			consoleRemoveRegistry(t)
			value, err := backend.snapshotResource(context.Background(), "usage", url.Values{"ref": {"team/reviewer"}})
			if err != nil {
				t.Fatal(err)
			}
			if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"unavailable":["bots","groups"]`) {
				t.Fatalf("unknown references treated as empty: %s", raw)
			}
			_, err = backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
			consoleTestCode(t, err, "usage_unavailable")
		})
	}
}

func TestConsoleUsageMalformedScheduleDoesNotLookUnused(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	hub.schedules = []*pb.ScheduleItem{{Id: 9, ScheduleType: "subtask", TaskSpecJson: []byte(`{"agent":`)}}
	consoleRemoveRegistry(t)
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"unavailable":["schedules"]`) {
		t.Fatalf("malformed schedule treated as no usage: %s", raw)
	}
	_, err = backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	consoleTestCode(t, err, "usage_unavailable")
}

func TestConsoleUsageListsAreBoundedButCountsAreExact(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	sessions := make([]*pb.Session, 0, consoleUsageListLimit+5)
	for i := 0; i < consoleUsageListLimit+5; i++ {
		sessions = append(sessions, &pb.Session{Id: fmt.Sprintf("bulk-%d", i), Name: fmt.Sprintf("bulk-%d", i), State: "sleeping", AgentProfile: "team/bulk", LastActivityAt: int64(i)})
	}
	hub.targets = []*pb.Target{{Id: "runtime-a", Name: "worker", Sessions: sessions}}
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{"ref": {"team/bulk"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, fmt.Sprintf(`"sessions":%d`, consoleUsageListLimit+5)) || strings.Count(raw, `"id":"bulk-`) != consoleUsageListLimit {
		t.Fatalf("bounded lists = %s", raw)
	}
	// The newest Sessions are the ones listed.
	if !strings.Contains(raw, fmt.Sprintf(`"name":"bulk-%d"`, consoleUsageListLimit+4)) || strings.Contains(raw, `"name":"bulk-0"`) {
		t.Fatalf("not the most recent: %s", raw)
	}
}

func TestConsoleUsageDegradesOneSourceAtATimeWithoutLeaking(t *testing.T) {
	backend, hub, tasks := consoleUsageFixture(t)
	hub.referencesErr = errors.New("bridge references unavailable")
	hub.schedulesErr = errors.New("api_key=schedule-secret")
	tasks.err = errors.New("TOKEN=instance-secret")
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{"ref": {"team/reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, `"unavailable":["bots","groups","schedules","instances"]`) || !strings.Contains(raw, `"sessions":2`) {
		t.Fatalf("degraded usage = %s", raw)
	}
	if strings.Contains(raw, "secret") {
		t.Fatalf("backend diagnostics leaked: %s", raw)
	}
}

func TestConsoleUsageDoesNotWaitForeverOnASlowSource(t *testing.T) {
	backend, _, tasks := consoleUsageFixture(t)
	tasks.block = true
	previous := consoleUsageTimeout
	consoleUsageTimeout = 20 * time.Millisecond
	t.Cleanup(func() { consoleUsageTimeout = previous })
	started := time.Now()
	value, err := backend.snapshotResource(context.Background(), "usage", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("usage waited %s for a stalled source", elapsed)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"unavailable":["instances"]`) {
		t.Fatalf("stalled source = %s", raw)
	}
}

func TestConsolePacksReportHowManyExpertsEachProvides(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team": {Namespace: "team", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/team/repo.git", CurrentRevision: "abc"},
		"solo": {Namespace: "solo", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/solo/repo.git", CurrentRevision: "def"},
		"idle": {Namespace: "idle", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/idle/repo.git", CurrentRevision: "123"},
	}}
	if err := agentprofile.SavePackRegistry(defaultAgentPackRegistryPath(), registry); err != nil {
		t.Fatal(err)
	}
	value, err := backend.snapshotResource(context.Background(), "packs", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, view := range value.(map[string]any)["packs"].([]map[string]any) {
		counts[view["namespace"].(string)] = view["profile_count"].(int)
	}
	if counts["team"] != 2 || counts["solo"] != 1 || counts["idle"] != 0 {
		t.Fatalf("expert counts = %v", counts)
	}
	// Packs still list when the Hub cannot enumerate experts; the count is simply absent.
	value, err = (&consoleBackend{}).snapshotResource(context.Background(), "packs", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); strings.Contains(raw, "profile_count") || !strings.Contains(raw, `"namespace":"team"`) {
		t.Fatalf("packs without a Hub = %s", raw)
	}
}

func TestConsoleProfilesCarryUsageOnRequestAndPackProvenance(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	hub.profile = consoleTestExpert("team", "reviewer")
	registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team": {Namespace: "team", SourceKind: agentprofile.PackSourceGit, GitURL: "https://user:token@example.com/team/repo.git", CurrentRevision: "abc", AutoSync: true, SyncInterval: "30s", Versions: []agentprofile.PackVersion{{Revision: "abc"}}},
	}}
	if err := agentprofile.SavePackRegistry(defaultAgentPackRegistryPath(), registry); err != nil {
		t.Fatal(err)
	}
	value, err := backend.snapshotResource(context.Background(), "profiles", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); strings.Contains(raw, `"usage"`) {
		t.Fatalf("usage was computed for a plain list: %s", raw)
	}
	value, err = backend.snapshotResource(context.Background(), "profiles", url.Values{"usage": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"usage":{`) || !strings.Contains(raw, `"team/reviewer":{"sessions":2,"bots":1,"groups":0,"schedules":1,"instances":1}`) || !strings.Contains(raw, `"usage_unavailable":[]`) {
		t.Fatalf("profiles with usage = %s", raw)
	}
	value, err = backend.snapshotResource(context.Background(), "profile", url.Values{"ref": {"team/reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, `"pack":{`) || !strings.Contains(raw, `"namespace":"team"`) || !strings.Contains(raw, `"current_revision":"abc"`) || !strings.Contains(raw, `"source_host":"example.com"`) {
		t.Fatalf("pack provenance = %s", raw)
	}
	if strings.Contains(raw, "token") || strings.Contains(raw, "user:") {
		t.Fatalf("raw source URL leaked: %s", raw)
	}
	// The detail page asks for the usage of this one expert.
	value, err = backend.snapshotResource(context.Background(), "profile", url.Values{"ref": {"team/reviewer"}, "usage": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"usage":{`) || !strings.Contains(raw, `"counts":{"sessions":2,"bots":1,"groups":0,"schedules":1,"instances":1}`) || !strings.Contains(raw, `"name":"Helper"`) || !strings.Contains(raw, `"unavailable":[]`) {
		t.Fatalf("profile with usage = %s", raw)
	}
	hub.profile, _ = agentprofile.ResolveBuiltin("builtin/codex")
	value, err = backend.snapshotResource(context.Background(), "profile", url.Values{"ref": {"builtin/codex"}})
	if err != nil || !strings.Contains(consoleTestJSON(t, value), `"source_kind":"builtin"`) {
		t.Fatalf("builtin provenance = %v %v", value, err)
	}
}

// removeFakes records the Pack removal the console asked for.
type removeCall struct {
	path, namespace string
	opts            agentprofile.RemovePackOptions
}

func consoleFakeRemove(t *testing.T, result agentprofile.RemovePackResult, err error) *[]removeCall {
	t.Helper()
	calls := &[]removeCall{}
	previous := consoleRemovePack
	t.Cleanup(func() { consoleRemovePack = previous })
	consoleRemovePack = func(_ context.Context, path, namespace string, opts agentprofile.RemovePackOptions, guard func(agentprofile.Pack) error) (agentprofile.RemovePackResult, error) {
		registry, loadErr := agentprofile.LoadPackRegistry(path)
		if loadErr != nil {
			return agentprofile.RemovePackResult{}, loadErr
		}
		if current, ok := registry.Packs[namespace]; ok && guard != nil {
			if guardErr := guard(current); guardErr != nil {
				return agentprofile.RemovePackResult{}, guardErr
			}
		}
		*calls = append(*calls, removeCall{path, namespace, opts})
		return result, err
	}
	return calls
}

func consoleRemoveRegistry(t *testing.T) {
	t.Helper()
	registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team":    {Namespace: "team", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/team/repo.git", CurrentRevision: "abc"},
		"solo":    {Namespace: "solo", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/solo/repo.git", CurrentRevision: "def"},
		"managed": {Namespace: "managed", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/m/repo.git", ManagedBy: "amux"},
		"temp":    {Namespace: "temp", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/t/repo.git", Temporary: true},
		"local1":  {Namespace: "local1", SourceKind: agentprofile.PackSourceGit, GitURL: "/srv/local/repo"},
	}}
	if err := agentprofile.SavePackRegistry(defaultAgentPackRegistryPath(), registry); err != nil {
		t.Fatal(err)
	}
}

func TestConsolePackRemoveRequiresConfirmationAndStaysInsideTheContract(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	calls := consoleFakeRemove(t, agentprofile.RemovePackResult{FilesRemoved: true, RemovedPath: "/private/host/path"}, nil)
	ctx := context.Background()
	_, err := backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"solo"}`))
	consoleTestCode(t, err, "confirmation_required")
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"managed","confirm":true}`))
	consoleTestCode(t, err, "managed_pack_read_only")
	// Temporary Packs can be removed, never synced or rolled back.
	_, err = backend.executeResource(ctx, "packs", "sync", json.RawMessage(`{"namespace":"temp"}`))
	consoleTestCode(t, err, "managed_pack_read_only")
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true,"keep_worktree":true}`))
	consoleTestCode(t, err, "invalid_field")
	for _, namespace := range []string{"builtin", "local", "../x"} {
		_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"`+namespace+`","confirm":true}`))
		consoleTestCode(t, err, "invalid_field")
	}
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"missing","confirm":true}`))
	consoleTestCode(t, err, "not_found")
	for _, body := range []string{`{"namespace":"solo","confirm":true,"path":"/etc"}`, `{"namespace":"solo","confirm":"yes"}`, `[]`} {
		if _, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a rejected request removed a Pack: %+v", *calls)
	}
	// Nothing starts work from "solo": removal goes through and never reveals the host path.
	value, err := backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, `"removed":{"files_removed":true,"namespace":"solo"}`) || strings.Contains(raw, "/private/host/path") {
		t.Fatalf("remove response = %s", raw)
	}
	if len(*calls) != 1 || (*calls)[0].path != defaultAgentPackRegistryPath() || (*calls)[0].namespace != "solo" || (*calls)[0].opts.KeepFiles {
		t.Fatalf("remove calls = %+v", *calls)
	}
	// A Pack registered from a local path has no git command involved in removal.
	if _, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"local1","confirm":true,"keep_files":true}`)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || !(*calls)[1].opts.KeepFiles {
		t.Fatalf("keep_files not forwarded: %+v", *calls)
	}
}

func TestConsolePackRemoveIsBlockedWhileSomethingStartsWorkFromIt(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	calls := consoleFakeRemove(t, agentprofile.RemovePackResult{}, nil)
	ctx := context.Background()
	// "team" experts are a Bot default, a Schedule and a persistent instance.
	_, err := backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"team","confirm":true}`))
	consoleTestCode(t, err, "pack_in_use")
	if len(*calls) != 0 {
		t.Fatal("removed a Pack that is in use")
	}
	if _, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"team","confirm":true,"force":true}`)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].namespace != "team" {
		t.Fatalf("forced removal calls = %+v", *calls)
	}

	// Sessions keep their frozen launch snapshot: they never block a removal.
	*calls = nil
	hub.referencesPayload, hub.schedules = `{"status":"success","references":[]}`, nil
	backend.tasks = &consoleUsageTasks{}
	if _, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"team","confirm":true}`)); err != nil {
		t.Fatalf("sessions blocked removal: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}

	// Unreadable usage blocks too; a known absence of a bridge returns an empty list.
	*calls = nil
	hub.schedulesErr = errors.New("schedule service down")
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	consoleTestCode(t, err, "usage_unavailable")
	if len(*calls) != 0 {
		t.Fatal("removed a Pack with unknown use")
	}
	if _, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true,"force":true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestConsolePackRemoveRechecksAfterAcquiringLock(t *testing.T) {
	backend, hub, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	hub.referencesPayload = `{"status":"success","references":[]}`
	hub.schedules = nil
	backend.tasks = &consoleUsageTasks{}
	old := consoleRemovePack
	t.Cleanup(func() { consoleRemovePack = old })
	consoleRemovePack = func(_ context.Context, _ string, _ string, _ agentprofile.RemovePackOptions, guard func(agentprofile.Pack) error) (agentprofile.RemovePackResult, error) {
		// A new group reference appears after the first guard, but before the
		// registry lock is acquired and the locked guard executes.
		hub.referencesPayload = `{"status":"success","references":[{"bridge_id":"feishu:bot","chat_id":"oc-group","agent":"team/reviewer"}]}`
		return agentprofile.RemovePackResult{}, guard(agentprofile.Pack{Namespace: "team"})
	}
	_, err := backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"team","confirm":true}`))
	consoleTestCode(t, err, "pack_in_use")
}

func TestConsolePackRemoveReportsPartialFileCleanup(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	consoleFakeRemove(t, agentprofile.RemovePackResult{RemovedPath: "/private/host/path"}, errors.New("permission denied: /private/host/path"))
	value, err := backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"warning_code":"pack_files_not_removed"`) || strings.Contains(raw, "/private/host/path") {
		t.Fatalf("partial cleanup response = %s", raw)
	}
}

func TestConsolePackRemoveRespectsContextBeforeDeleting(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	old := consoleRemovePack
	t.Cleanup(func() { consoleRemovePack = old })
	consoleRemovePack = func(context.Context, string, string, agentprofile.RemovePackOptions, func(agentprofile.Pack) error) (agentprofile.RemovePackResult, error) {
		return agentprofile.RemovePackResult{}, context.DeadlineExceeded
	}
	_, err := backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	consoleTestCode(t, err, "pack_busy")
}

func TestConsolePackRemoveReportsFailureWithoutHostDetails(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	consoleFakeRemove(t, agentprofile.RemovePackResult{}, errors.New("remove materialized agent pack files /private/host/path: permission denied"))
	_, err := backend.executeResource(context.Background(), "packs", "remove", json.RawMessage(`{"namespace":"solo","confirm":true}`))
	consoleTestCode(t, err, "operation_failed")
	if strings.Contains(err.Error(), "/private/host/path") {
		t.Fatalf("host path leaked: %v", err)
	}
}

func TestConsoleProfileKeyIgnoresRevisionPins(t *testing.T) {
	for ref, want := range map[string]string{
		"team/reviewer" + consolePin: "team/reviewer",
		" team/reviewer ":            "team/reviewer",
		"team/reviewer":              "team/reviewer",
		"team/reviewer@v2":           "team/reviewer@v2",
		"":                           "",
	} {
		if got := consoleProfileKey(ref); got != want {
			t.Fatalf("consoleProfileKey(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestConsoleRemovesTemporaryDevelopmentPacks(t *testing.T) {
	backend, _, _ := consoleUsageFixture(t)
	consoleRemoveRegistry(t)
	calls := consoleFakeRemove(t, agentprofile.RemovePackResult{FilesRemoved: true, RemovedPath: "/private/host/path"}, nil)
	ctx := context.Background()
	// Without a development lease a temporary Pack is unregistered like any other.
	value, err := backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"temp","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"namespace":"temp"`) || len(*calls) != 1 {
		t.Fatalf("remove = %s calls=%+v", raw, *calls)
	}
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"temp","confirm":true,"keep_worktree":true}`))
	consoleTestCode(t, err, "invalid_field")

	// With a lease, removal cleans the development worktree up too.
	consoleRemoveRegistry(t)
	leasePath := agentprofile.DevelopmentLeaseRegistryPath(defaultAgentPackRegistryPath())
	if err := os.WriteFile(leasePath, []byte(`{"leases":{"temp":{"namespace":"temp","branch":"dev/x","worktree_path":"/private/worktree"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err := backend.snapshotResource(ctx, "packs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, listed); !strings.Contains(raw, `"branch":"dev/x"`) || strings.Contains(raw, "/private/worktree") || !strings.Contains(raw, `"removable":true`) {
		t.Fatalf("packs = %s", raw)
	}
	cleanup, release := consoleCleanupDevelopmentPack, consoleReleaseDevelopmentPack
	t.Cleanup(func() { consoleCleanupDevelopmentPack, consoleReleaseDevelopmentPack = cleanup, release })
	var cleaned, released []string
	cleanupStatus := agentprofile.DevelopmentLeaseRetained
	consoleCleanupDevelopmentPack = func(_ context.Context, _ string, namespace string, guard func(agentprofile.Pack) error) (agentprofile.DevelopmentLeaseCleanupResult, error) {
		if err := guard(agentprofile.Pack{Namespace: namespace, Temporary: true}); err != nil {
			return agentprofile.DevelopmentLeaseCleanupResult{}, err
		}
		cleaned = append(cleaned, namespace)
		return agentprofile.DevelopmentLeaseCleanupResult{Status: cleanupStatus, Reason: "development worktree has uncommitted changes in /private/worktree"}, nil
	}
	consoleReleaseDevelopmentPack = func(_ context.Context, _ string, namespace string, guard func(agentprofile.Pack) error) (agentprofile.RemovePackResult, error) {
		if err := guard(agentprofile.Pack{Namespace: namespace, Temporary: true}); err != nil {
			return agentprofile.RemovePackResult{}, err
		}
		released = append(released, namespace)
		return agentprofile.RemovePackResult{FilesRemoved: true}, nil
	}
	_, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"temp","confirm":true}`))
	consoleTestCode(t, err, "development_worktree_retained")
	if strings.Contains(err.Error(), "/private") || !strings.Contains(err.Error(), "worktree_has_changes") {
		t.Fatalf("retained error = %v", err)
	}
	value, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"temp","confirm":true,"keep_worktree":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"worktree_kept":true`) {
		t.Fatalf("release = %s", raw)
	}
	cleanupStatus = agentprofile.DevelopmentLeaseCleaned
	value, err = backend.executeResource(ctx, "packs", "remove", json.RawMessage(`{"namespace":"temp","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); !strings.Contains(raw, `"worktree_removed":true`) {
		t.Fatalf("cleanup = %s", raw)
	}
	if strings.Join(cleaned, ",") != "temp,temp" || strings.Join(released, ",") != "temp" || len(*calls) != 1 {
		t.Fatalf("cleaned=%v released=%v removed=%+v", cleaned, released, *calls)
	}
}
