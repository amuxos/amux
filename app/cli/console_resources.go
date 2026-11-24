package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/gateway"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The browser is an operator surface, not an authority to select an actor.
// Requests are deliberately narrow DTOs; identity, credentials and frozen launch
// snapshots can only be supplied by the existing authenticated Hub clients.
type consoleResourceError struct {
	code  string
	field string
	cause error
}

func (e *consoleResourceError) Error() string {
	if e.field != "" {
		return e.code + ": " + e.field
	}
	return e.code
}
func (e *consoleResourceError) Unwrap() error             { return e.cause }
func (e *consoleResourceError) ConsoleErrorCode() string  { return e.code }
func (e *consoleResourceError) ConsoleErrorField() string { return e.field }
func consoleInvalid(code, field string) error {
	return &consoleResourceError{code: code, field: field, cause: webconsole.ErrInvalid}
}
func consoleMissing() error {
	return &consoleResourceError{code: "not_found", cause: webconsole.ErrNotFound}
}
func consoleOperationError(err error) error {
	if err == nil {
		return nil
	}
	var resourceErr *consoleResourceError
	if errors.As(err, &resourceErr) {
		return err
	}
	if errors.Is(err, webconsole.ErrConflict) {
		return &consoleResourceError{code: "version_conflict", cause: err}
	}
	var fieldErr *clientop.FieldError
	if errors.As(err, &fieldErr) {
		return consoleInvalid("invalid_field", string(fieldErr.Field))
	}
	code := "operation_failed"
	cause := err
	switch status.Code(err) {
	case codes.NotFound:
		code = "not_found"
		cause = errors.Join(webconsole.ErrNotFound, err)
	case codes.InvalidArgument:
		code = "invalid_field"
		cause = errors.Join(webconsole.ErrInvalid, err)
	case codes.PermissionDenied, codes.Unauthenticated:
		code = "permission_denied"
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		code = "operation_conflict"
		cause = errors.Join(webconsole.ErrConflict, err)
	}
	// Backend diagnostics can contain env values and launch arguments. Preserve
	// their identity for callers/logging, never render them into browser errors.
	return &consoleResourceError{code: code, cause: cause}
}
func consoleDecode(data json.RawMessage, value any) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || raw[0] != '{' {
		return consoleInvalid("invalid_data", "")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return consoleInvalid("invalid_data", "")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return consoleInvalid("invalid_data", "")
	}
	return nil
}
func consoleRequired(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return consoleInvalid("field_required", field)
	}
	return nil
}
func consoleActions(resource string) []string {
	switch resource {
	case "sessions", "session":
		return []string{"create", "submit", "interrupt", "restart", "archive", "sleep", "resume", "queue-list", "queue-clear", "queue-cancel", "respond-permission"}
	case "tasks", "task":
		return []string{"cancel", "respond"}
	case "instances", "instance":
		return []string{"spawn", "followup", "send", "steer", "interrupt"}
	case "schedules", "schedule":
		return []string{"create", "update", "enable", "disable", "delete"}
	case "packs":
		return []string{"install", "sync", "rollback", "remove"}
	case "credentials":
		return []string{"set_env", "unset_env", "set_file", "unset_file", "delete"}
	default:
		return []string{}
	}
}
func consoleWithActions(resource string, value map[string]any) map[string]any {
	value["actions"] = consoleActions(resource)
	return value
}
func consoleHasAction(resource, action string) bool {
	for _, candidate := range consoleActions(resource) {
		if action == candidate {
			return true
		}
	}
	return false
}

func (b *consoleBackend) snapshotResource(ctx context.Context, resource string, query url.Values) (any, error) {
	switch resource {
	case "overview", "sessions", "session", "tasks", "task", "instances", "instance", "schedules", "schedule", "profiles", "profile", "packs", "usage", "credentials", "catalogs":
	default:
		return nil, errConsoleResourceNotFound
	}
	if consoleHasAgentContext() || !b.managedOperator && actorToken() != "" {
		return nil, consoleInvalid("operator_required", "")
	}
	var value map[string]any
	var err error
	switch resource {
	case "overview":
		value, err = b.consoleOverview(ctx)
	case "sessions", "session":
		value, err = b.consoleSessions(ctx, resource, query)
	case "tasks", "task":
		value, err = b.consoleTasks(ctx, resource, query)
	case "instances", "instance":
		value, err = b.consoleInstances(ctx, resource, query)
	case "schedules", "schedule":
		value, err = b.consoleSchedules(ctx, resource, query)
	case "profiles", "profile":
		value, err = b.consoleProfiles(ctx, resource, query)
	case "packs":
		value, err = b.consolePacksWithExperts(ctx)
	case "usage":
		value, err = b.consoleUsage(ctx, query)
	case "credentials":
		if b.managedOperator {
			value, err = b.consoleOperatorCredentials(ctx)
		} else {
			value, err = consoleCredentials(ctx)
		}
	case "catalogs":
		value, err = b.consoleCatalogs(ctx)
	}
	if err != nil {
		return nil, consoleOperationError(err)
	}
	return consoleWithActions(resource, value), nil
}
func (b *consoleBackend) executeResource(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	switch resource {
	case "sessions", "session", "tasks", "task", "instances", "instance", "schedules", "schedule", "packs", "credentials":
	case "overview", "profiles", "profile", "catalogs":
		return nil, consoleInvalid("unsupported_action", "")
	default:
		return nil, errConsoleResourceNotFound
	}
	if consoleHasAgentContext() || !b.managedOperator && actorToken() != "" {
		return nil, consoleInvalid("operator_required", "")
	}
	if !consoleHasAction(resource, action) {
		return nil, consoleInvalid("unsupported_action", "")
	}
	if err := consoleValidateActionFields(resource, action, data); err != nil {
		return nil, err
	}
	var value any
	var err error
	switch resource {
	case "sessions", "session":
		value, err = b.consoleExecuteSession(ctx, action, data)
	case "tasks", "task":
		value, err = b.consoleExecuteTask(ctx, action, data)
	case "instances", "instance":
		value, err = b.consoleExecuteInstance(ctx, action, data)
	case "schedules", "schedule":
		value, err = b.consoleExecuteSchedule(ctx, action, data)
	case "packs":
		value, err = b.consoleExecutePack(ctx, action, data)
	case "credentials":
		value, err = b.consoleExecuteCredential(ctx, action, data)
	}
	return value, consoleOperationError(err)
}

// Only explicitly selected Session fields are copied. Env, native arguments,
// execution identity, launch IDs and future protocol fields are not browser data.
func consoleSession(s *pb.Session) *pb.Session {
	if s == nil {
		return nil
	}
	return &pb.Session{Id: s.Id, Name: s.Name, AgentType: s.AgentType, State: s.State,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Acp: s.Acp, Interrupt: s.Interrupt,
		Cwd: s.Cwd, Protocol: s.Protocol, WorkspaceSource: s.WorkspaceSource, WorkspaceScope: s.WorkspaceScope,
		AgentProfile: s.AgentProfile, IdleTimeoutSeconds: s.IdleTimeoutSeconds, LastActivityAt: s.LastActivityAt,
		LastSleepReason: s.LastSleepReason, AutoSleepEligible: s.AutoSleepEligible, OwnerTaskId: s.OwnerTaskId,
		AgentInstanceId: s.AgentInstanceId, EffectiveModel: s.EffectiveModel, CwdSwitch: s.CwdSwitch,
		InputModalities: append([]string(nil), s.InputModalities...), InputCapabilitySource: s.InputCapabilitySource, GoalSupported: s.GoalSupported}
}
func (b *consoleBackend) consoleSessionEntries(ctx context.Context) ([]*gateway.SessionEntry, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	resp, err := clientop.SessionList(ctx, b.hub)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	entries := make([]*gateway.SessionEntry, 0)
	for _, target := range resp.GetTargets() {
		if target == nil {
			continue
		}
		for _, session := range target.GetSessions() {
			if session != nil {
				entries = append(entries, &gateway.SessionEntry{Session: consoleSession(session), TargetID: target.GetId(), TargetName: target.GetName()})
			}
		}
	}
	return entries, nil
}
func consoleQueryFirst(query url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
func consoleFindSession(entries []*gateway.SessionEntry, name, target string) (*gateway.SessionEntry, error) {
	if err := consoleRequired(name, "name"); err != nil {
		return nil, err
	}
	var match *gateway.SessionEntry
	for _, entry := range entries {
		if target != "" && entry.TargetID != target && entry.TargetName != target {
			continue
		}
		if entry.GetName() != name && entry.GetId() != name {
			continue
		}
		if match != nil {
			return nil, &consoleResourceError{code: "ambiguous_session", cause: webconsole.ErrConflict}
		}
		match = entry
	}
	if match == nil {
		return nil, consoleMissing()
	}
	return match, nil
}
func (b *consoleBackend) consoleSessions(ctx context.Context, resource string, query url.Values) (map[string]any, error) {
	entries, err := b.consoleSessionEntries(ctx)
	if err != nil {
		return nil, err
	}
	if resource != "session" {
		sessions := b.consoleEnrichActivity(ctx, entries)
		known := 0
		for _, session := range sessions {
			if session.ActivityStatus == "known" {
				known++
			}
		}
		value := map[string]any{"sessions": sessions, "activity_coverage": map[string]int{"known": known, "unknown": len(sessions) - known}}
		if resource == "overview" {
			counts := map[string]int{"total": len(entries), "running": 0, "sleeping": 0, "archived": 0}
			for _, entry := range entries {
				counts[entry.GetState()]++
			}
			value["counts"] = counts
		}
		return value, nil
	}
	entry, err := consoleFindSession(entries, consoleQueryFirst(query, "name", "session", "id"), consoleQueryFirst(query, "target", "target_id"))
	if err != nil {
		return nil, err
	}
	// The canonical replay is the slow read; run it beside the queue lookup so
	// the detail page waits for the slower of the two, not their sum.
	var facts *consoleActivityFacts
	var replay sync.WaitGroup
	// activity=0 asks for the lifecycle only: an open page polls it cheaply.
	if entry.GetState() != "archived" && b.hub != nil && consoleQueryFirst(query, "activity") != "0" {
		replay.Add(1)
		go func() {
			defer replay.Done()
			facts = b.consoleReadActivity(ctx, entry)
		}()
	}
	queue, err := clientop.SessionQueueList(ctx, b.hub, clientop.SessionRef{Name: entry.GetName(), Target: entry.TargetID})
	replay.Wait()
	if err != nil {
		return nil, err
	}
	if queue == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	items := queue.GetItems()
	if items == nil {
		items = []*pb.QueueItem{}
	}
	value := map[string]any{"session": entry, "queue": items, "activity_status": "unknown"}
	// Only a complete canonical replay establishes the current Turn; otherwise
	// the browser keeps lifecycle-only controls and labels the Turn unknown.
	if facts != nil {
		value["activity"] = facts
		value["activity_status"] = "known"
	}
	return value, nil
}
func consoleProfileView(profile agentprofile.Profile) map[string]any {
	view := profileCLIView(profile)
	// Profile env values, native launch commands and MCP config can hold secrets.
	for _, key := range []string{"default_env", "default_args", "mcp_config", "env_refs"} {
		delete(view, key)
	}
	return view
}
func (b *consoleBackend) consoleProfiles(ctx context.Context, resource string, query url.Values) (map[string]any, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if resource == "profile" {
		ref := consoleQueryFirst(query, "ref", "profile")
		if err := consoleRequired(ref, "ref"); err != nil {
			return nil, err
		}
		profile, err := resolveHubRunProfile(ctx, b.hub, ref)
		if err != nil {
			return nil, err
		}
		value := map[string]any{"profile": consoleProfileView(profile)}
		if pack := consoleProfilePack(profile.Ref()); pack != nil {
			value["pack"] = pack
		}
		if consoleQueryFirst(query, "usage") == "1" {
			index := b.consoleUsageIndex(ctx)
			usage := index.forRef(profile.Ref())
			usage["unavailable"] = nonNilStrings(index.unavailable)
			value["usage"] = usage
		}
		return value, nil
	}
	resp, err := b.hub.ListAgentProfiles(ctx, &pb.ListAgentProfilesRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	profiles, err := discoveredAgentProfilesFromPB(resp.GetProfiles())
	if err != nil {
		return nil, err
	}
	views := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, consoleProfileView(profile.Profile))
	}
	value := map[string]any{"profiles": views}
	if resp.GetProfilesError() != "" {
		value["partial"] = true
		value["warning_code"] = "profile_discovery_incomplete"
	}
	if consoleQueryFirst(query, "usage") == "1" {
		index := b.consoleUsageIndex(ctx)
		value["usage"] = index.counts()
		value["usage_unavailable"] = nonNilStrings(index.unavailable)
	}
	return value, nil
}

// consoleProfilePack describes the Pack that provides an expert: its source, the
// current revision and the last sync, never raw URLs or file locations.
func consoleProfilePack(ref string) map[string]any {
	namespace, _, found := strings.Cut(ref, "/")
	if !found || namespace == "" {
		return nil
	}
	if namespace == agentprofile.BuiltinNamespace {
		return map[string]any{"namespace": namespace, "source_kind": "builtin"}
	}
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		return nil
	}
	pack, ok := registry.Packs[namespace]
	if !ok {
		return nil
	}
	return consolePackView(pack)
}
func consoleCredentials(ctx context.Context) (map[string]any, error) {
	profiles, err := listCredentialMetadata(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, map[string]any{"profile": profile.Profile, "kind": profile.Kind, "env_names": nonNilStrings(profile.EnvNames), "file_names": nonNilStrings(profile.FileNames)})
	}
	return map[string]any{"profiles": views}, nil
}
func (b *consoleBackend) consoleCatalogs(ctx context.Context) (map[string]any, error) {
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		return nil, err
	}
	runtimes := make([]map[string]any, 0, len(catalog))
	for _, runtime := range catalog {
		models := make([]map[string]any, 0, len(runtime.Models))
		for _, model := range runtime.Models {
			models = append(models, map[string]any{"ref": model.Ref, "display_name": model.DisplayName, "supported_reasoning_efforts": nonNilStrings(model.SupportedReasoningEfforts), "default_reasoning_effort": model.DefaultReasoningEffort})
		}
		runtimes = append(runtimes, map[string]any{"name": runtime.Ref, "ref": runtime.Ref, "display_name": runtime.DisplayName, "agent_type": runtime.AgentType, "models": models})
	}
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	resp, err := b.hub.ListTargets(ctx, &pb.ListTargetsRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	targets := make([]map[string]string, 0, len(resp.GetTargets()))
	for _, target := range resp.GetTargets() {
		if target != nil {
			targets = append(targets, map[string]string{"id": target.GetId(), "name": target.GetName()})
		}
	}
	return map[string]any{"runtimes": runtimes, "targets": targets}, nil
}
func consoleSchedule(item *pb.ScheduleItem) *pb.ScheduleItem {
	if item == nil {
		return nil
	}
	// Freeze the shape and omit the stored task spec altogether: it includes
	// provider launch snapshots and can contain credentials/native args.
	out := &pb.ScheduleItem{Id: item.Id, Name: item.Name, ScheduleType: item.ScheduleType, TargetId: item.TargetId, TargetName: item.TargetName, SessionName: item.SessionName, Spec: item.Spec, Kind: item.Kind, Timezone: item.Timezone, RunMode: item.RunMode, Prompt: item.Prompt, Enabled: item.Enabled, NextRunAt: item.NextRunAt, LastRunAt: item.LastRunAt, LastStatus: item.LastStatus}
	out.Runs = make([]*pb.ScheduleRun, 0, len(item.Runs))
	for _, run := range item.Runs {
		if run != nil {
			out.Runs = append(out.Runs, proto.Clone(run).(*pb.ScheduleRun))
		}
	}
	return out
}
func consoleScheduleID(query url.Values) (int64, error) {
	id, err := strconv.ParseInt(consoleQueryFirst(query, "schedule_id", "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, consoleInvalid("invalid_field", "schedule_id")
	}
	return id, nil
}
func (b *consoleBackend) consoleSchedules(ctx context.Context, resource string, query url.Values) (map[string]any, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if resource == "schedule" {
		id, err := consoleScheduleID(query)
		if err != nil {
			return nil, err
		}
		item, err := clientop.ScheduleGet(ctx, b.hub, clientop.ScheduleIDRequest{ID: id, IncludeRuns: true})
		if err != nil {
			return nil, err
		}
		task, err := consoleScheduleTask(item)
		if err != nil {
			return nil, err
		}
		return map[string]any{"schedule": consoleSchedule(item), "task": task}, nil
	}
	items, err := clientop.ScheduleList(ctx, b.hub)
	if err != nil {
		return nil, err
	}
	views := make([]*pb.ScheduleItem, 0, len(items))
	for _, item := range items {
		if item != nil {
			views = append(views, consoleSchedule(item))
		}
	}
	return map[string]any{"schedules": views}, nil
}
