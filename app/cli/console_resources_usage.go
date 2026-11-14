package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// consoleUsageListLimit bounds the names listed per source and expert.
const consoleUsageListLimit = 20

// consoleUsageTimeout bounds each source so a slow service only drops its own
// column. It is a variable only so tests can shorten it.
var consoleUsageTimeout = 5 * time.Second

var consolePinnedRevision = regexp.MustCompile(`@[0-9a-fA-F]{12,}$`)

// consoleProfileKey is an expert reference without the revision pin that a
// Session or persistent instance records when it starts.
func consoleProfileKey(ref string) string {
	return consolePinnedRevision.ReplaceAllString(strings.TrimSpace(ref), "")
}

// consoleUsageCounts says what refers to an expert. Sessions keep their frozen
// launch snapshot, so only Bot and group defaults, Schedules and persistent
// instances start new work from the reference and fail when the expert is gone.
type consoleUsageCounts struct {
	Sessions  int `json:"sessions"`
	Bots      int `json:"bots"`
	Groups    int `json:"groups"`
	Schedules int `json:"schedules"`
	Instances int `json:"instances"`
}

func (c consoleUsageCounts) starting() int { return c.Bots + c.Groups + c.Schedules + c.Instances }
func (c *consoleUsageCounts) add(other consoleUsageCounts) {
	c.Sessions += other.Sessions
	c.Bots += other.Bots
	c.Groups += other.Groups
	c.Schedules += other.Schedules
	c.Instances += other.Instances
}

type consoleUsageItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state,omitempty"`
	Target string `json:"target,omitempty"`
}

type consoleUsageProfile struct {
	counts consoleUsageCounts
	items  map[string][]consoleUsageItem
}

// consoleUsageIndex is what refers to which expert, read at one moment. A source
// that cannot be read is listed in unavailable and never counted as zero use.
type consoleUsageIndex struct {
	byRef       map[string]*consoleUsageProfile
	unavailable []string
}

func (i *consoleUsageIndex) record(kind, ref string, item consoleUsageItem) {
	key := consoleProfileKey(ref)
	if key == "" {
		return
	}
	profile := i.byRef[key]
	if profile == nil {
		profile = &consoleUsageProfile{items: map[string][]consoleUsageItem{}}
		i.byRef[key] = profile
	}
	switch kind {
	case "sessions":
		profile.counts.Sessions++
	case "bots":
		profile.counts.Bots++
	case "groups":
		profile.counts.Groups++
	case "schedules":
		profile.counts.Schedules++
	case "instances":
		profile.counts.Instances++
	}
	if len(profile.items[kind]) < consoleUsageListLimit {
		profile.items[kind] = append(profile.items[kind], item)
	}
}

type consoleUsageRecord struct {
	kind, ref string
	item      consoleUsageItem
}

func (b *consoleBackend) consoleUsageSessions(ctx context.Context) ([]consoleUsageRecord, error) {
	entries, err := b.consoleSessionEntries(ctx)
	if err != nil {
		return nil, err
	}
	// Most recent first, so the bounded list shows what is current.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].GetLastActivityAt() > entries[j].GetLastActivityAt() })
	records := make([]consoleUsageRecord, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.GetState() == "archived" || entry.GetAgentProfile() == "" {
			continue
		}
		records = append(records, consoleUsageRecord{"sessions", entry.GetAgentProfile(), consoleUsageItem{ID: entry.GetId(), Name: entry.GetName(), State: entry.GetState(), Target: entry.TargetID}})
	}
	return records, nil
}

// consoleUsageBridge reads stored Bot and group default experts through the Hub.
// A stopped Bridge can leave references behind; an unavailable reference source
// is unknown, never evidence that nothing refers to the Pack.
func (b *consoleBackend) consoleUsageBridge(ctx context.Context) ([]consoleUsageRecord, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	raw, err := b.bridgeManagement(ctx, "bots", "references", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	data, ok := raw.(json.RawMessage)
	if !ok {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	var references struct {
		References []struct {
			BridgeID string `json:"bridge_id"`
			ChatID   string `json:"chat_id"`
			Agent    string `json:"agent"`
		} `json:"references"`
	}
	if err := json.Unmarshal(data, &references); err != nil || references.References == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	// Names are a courtesy: a missing name never makes the references unknown.
	botNames, botStates, groupNames := map[string]string{}, map[string]string{}, map[string]string{}
	if listed, listErr := b.bridgeManagement(ctx, "bots", "list", json.RawMessage(`{}`)); listErr == nil {
		var bots struct {
			Bots []struct{ ID, Name, Status string } `json:"bots"`
		}
		if data, ok := listed.(json.RawMessage); ok && json.Unmarshal(data, &bots) == nil {
			for _, bot := range bots.Bots {
				botNames[bot.ID], botStates[bot.ID] = bot.Name, bot.Status
			}
		}
	}
	if listed, listErr := b.bridgeManagement(ctx, "groups", "list", json.RawMessage(`{}`)); listErr == nil {
		var groups struct {
			Groups []struct {
				ChatID string `json:"chat_id"`
				Title  string `json:"title"`
				Name   string `json:"name"`
			} `json:"groups"`
		}
		if data, ok := listed.(json.RawMessage); ok && json.Unmarshal(data, &groups) == nil {
			for _, group := range groups.Groups {
				groupNames[group.ChatID] = firstNonEmpty(group.Title, group.Name)
			}
		}
	}
	records := make([]consoleUsageRecord, 0, len(references.References))
	for _, reference := range references.References {
		if strings.TrimSpace(reference.Agent) == "" {
			continue
		}
		if reference.BridgeID == "" {
			return nil, consoleInvalid("backend_response_invalid", "")
		}
		if reference.ChatID == "" {
			records = append(records, consoleUsageRecord{"bots", reference.Agent, consoleUsageItem{ID: reference.BridgeID, Name: firstNonEmpty(botNames[reference.BridgeID], reference.BridgeID), State: botStates[reference.BridgeID]}})
			continue
		}
		records = append(records, consoleUsageRecord{"groups", reference.Agent, consoleUsageItem{ID: reference.ChatID, Name: firstNonEmpty(groupNames[reference.ChatID], reference.ChatID)}})
	}
	return records, nil
}

func (b *consoleBackend) consoleUsageSchedules(ctx context.Context) ([]consoleUsageRecord, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	resp, err := b.hub.ListSchedules(ctx, &pb.ListSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	records := []consoleUsageRecord{}
	for _, item := range resp.GetSchedules() {
		// Only schedules that start a task by expert reference carry a Spec; a
		// session-prompt schedule drives an existing Session instead.
		if item == nil || len(item.GetTaskSpecJson()) == 0 {
			continue
		}
		var spec task.Spec
		if err := json.Unmarshal(item.GetTaskSpecJson(), &spec); err != nil {
			// A malformed task may still reference an expert. Never treat a
			// partially readable schedule store as proof of no references.
			return nil, consoleInvalid("backend_response_invalid", "")
		}
		if strings.TrimSpace(spec.Agent) == "" {
			continue
		}
		state := "disabled"
		if item.GetEnabled() {
			state = "enabled"
		}
		name := item.GetName()
		if name == "" {
			name = "#" + strconv.FormatInt(item.GetId(), 10)
		}
		records = append(records, consoleUsageRecord{"schedules", spec.Agent, consoleUsageItem{ID: strconv.FormatInt(item.GetId(), 10), Name: name, State: state}})
	}
	return records, nil
}

func (b *consoleBackend) consoleUsageInstances(ctx context.Context) ([]consoleUsageRecord, error) {
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	resp, err := b.tasks.ListAgentInstances(ctx, &pb.ListAgentInstancesRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	records := []consoleUsageRecord{}
	for _, instance := range resp.GetInstances() {
		if instance == nil || instance.GetStatus() == "archived" || instance.GetAgentProfile() == "" {
			continue
		}
		name := instance.GetName()
		if name == "" {
			name = instance.GetId()
		}
		records = append(records, consoleUsageRecord{"instances", instance.GetAgentProfile(), consoleUsageItem{ID: instance.GetId(), Name: name, State: instance.GetStatus(), Target: instance.GetTargetId()}})
	}
	return records, nil
}

// consoleUsageIndex reads the four sources in parallel. Each has its own
// deadline and failure: an unavailable source is reported, never counted as zero.
func (b *consoleBackend) consoleUsageIndex(ctx context.Context) *consoleUsageIndex {
	sources := []struct {
		kinds []string // what an unreadable source leaves unknown
		read  func(context.Context) ([]consoleUsageRecord, error)
	}{
		{[]string{"sessions"}, b.consoleUsageSessions},
		{[]string{"bots", "groups"}, b.consoleUsageBridge},
		{[]string{"schedules"}, b.consoleUsageSchedules},
		{[]string{"instances"}, b.consoleUsageInstances},
	}
	records := make([][]consoleUsageRecord, len(sources))
	failed := make([]bool, len(sources))
	var wg sync.WaitGroup
	for n, source := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sourceCtx, cancel := context.WithTimeout(ctx, consoleUsageTimeout)
			defer cancel()
			read, err := source.read(sourceCtx)
			records[n], failed[n] = read, err != nil
		}()
	}
	wg.Wait()
	index := &consoleUsageIndex{byRef: map[string]*consoleUsageProfile{}}
	for i, source := range sources {
		if failed[i] {
			index.unavailable = append(index.unavailable, source.kinds...)
			continue
		}
		for _, record := range records[i] {
			index.record(record.kind, record.ref, record.item)
		}
	}
	return index
}

// forNamespace sums the expert references of one Pack namespace.
func (i *consoleUsageIndex) forNamespace(namespace string) (consoleUsageCounts, map[string]consoleUsageCounts, map[string][]consoleUsageItem) {
	total := consoleUsageCounts{}
	refs := map[string]consoleUsageCounts{}
	items := map[string][]consoleUsageItem{}
	prefix := namespace + "/"
	keys := make([]string, 0, len(i.byRef))
	for key := range i.byRef {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		profile := i.byRef[key]
		total.add(profile.counts)
		refs[key] = profile.counts
		for kind, list := range profile.items {
			for _, item := range list {
				if len(items[kind]) < consoleUsageListLimit {
					items[kind] = append(items[kind], item)
				}
			}
		}
	}
	return total, refs, items
}

func (i *consoleUsageIndex) counts() map[string]consoleUsageCounts {
	out := make(map[string]consoleUsageCounts, len(i.byRef))
	for key, profile := range i.byRef {
		out[key] = profile.counts
	}
	return out
}

// forRef is what refers to one expert: its counts and the bounded lists behind them.
func (i *consoleUsageIndex) forRef(ref string) map[string]any {
	key := consoleProfileKey(ref)
	counts, items := consoleUsageCounts{}, map[string][]consoleUsageItem{}
	if profile := i.byRef[key]; profile != nil {
		counts, items = profile.counts, profile.items
	}
	return map[string]any{"ref": key, "counts": counts, "items": consoleNonNilItems(items)}
}

func consoleNonNilItems(items map[string][]consoleUsageItem) map[string][]consoleUsageItem {
	out := map[string][]consoleUsageItem{"sessions": {}, "bots": {}, "groups": {}, "schedules": {}, "instances": {}}
	for kind, list := range items {
		out[kind] = list
	}
	return out
}

// consoleUsage answers GET usage: for one expert (ref), for every expert of a
// Pack namespace (namespace), or counts for all experts when neither is given.
func (b *consoleBackend) consoleUsage(ctx context.Context, query url.Values) (map[string]any, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	index := b.consoleUsageIndex(ctx)
	value := map[string]any{"unavailable": nonNilStrings(index.unavailable)}
	switch {
	case consoleQueryFirst(query, "ref") != "":
		for key, item := range index.forRef(consoleQueryFirst(query, "ref")) {
			value[key] = item
		}
	case consoleQueryFirst(query, "namespace") != "":
		namespace := consoleQueryFirst(query, "namespace")
		if err := consoleValidatePackNamespace(namespace); err != nil {
			return nil, err
		}
		counts, refs, items := index.forNamespace(namespace)
		experts := b.consolePackExperts(ctx, namespace, refs)
		for _, ref := range experts {
			if _, used := refs[ref]; !used {
				refs[ref] = consoleUsageCounts{}
			}
		}
		value["namespace"], value["counts"], value["refs"], value["items"], value["experts"] = namespace, counts, refs, consoleNonNilItems(items), experts
	default:
		value["usage"] = index.counts()
	}
	return value, nil
}
