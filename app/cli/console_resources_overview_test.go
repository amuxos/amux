package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type consoleOverviewTasks struct {
	pb.AmuxTaskClient
	raw [][]byte
	err error
}

func (f *consoleOverviewTasks) ListSubtasks(context.Context, *pb.ListSubtasksRequest, ...grpc.CallOption) (*pb.ListSubtasksResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &pb.ListSubtasksResponse{TasksJson: f.raw}, nil
}

func consoleOverviewTask(id, status string, requested, finished time.Time, text string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"status":%q,"task":%q,"requested_at":%q,"finished_at":%q,"error":"TOKEN=task-secret","result":{"status":"ok","summary":"done"},"spec":{"env":["TOKEN=spec-secret"]}}`,
		id, status, text, requested.Format(time.RFC3339), finished.Format(time.RFC3339)))
}

func TestConsoleOverviewSummarizesTasksAndBotsWithoutSecrets(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	hub.botsPayload = `{"status":"success","bots":[{"profile":"default","name":"Helper","status":"running"}]}`
	now := time.Now().UTC()
	long := strings.Repeat("步", consoleOverviewTaskTextSize+20)
	tasks := &consoleOverviewTasks{}
	for i := 0; i < consoleOverviewRecentTasks+2; i++ {
		tasks.raw = append(tasks.raw, consoleOverviewTask(fmt.Sprintf("task-%d", i), "succeeded", now.Add(-time.Duration(i)*time.Minute), now.Add(-time.Duration(i)*time.Minute), long))
	}
	tasks.raw = append(tasks.raw,
		consoleOverviewTask("task-running", "running", now.Add(-48*time.Hour), time.Time{}, "run"),
		consoleOverviewTask("task-failed", "timed_out", now.Add(-2*time.Hour), now.Add(-time.Hour), "fail"),
		consoleOverviewTask("task-old", "failed", now.Add(-72*time.Hour), now.Add(-71*time.Hour), "old"),
	)
	backend := &consoleBackend{hub: hub, tasks: tasks}
	value, err := backend.snapshotResource(context.Background(), "overview", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	for _, secret := range []string{"task-secret", "spec-secret", `"spec"`, `"result"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("overview leaked %s: %s", secret, raw)
		}
	}
	recent, ok := value.(map[string]any)["recent_tasks"].([]map[string]any)
	if !ok || len(recent) != consoleOverviewRecentTasks || recent[0]["task_id"] != "task-0" {
		t.Fatalf("recent tasks = %#v", value.(map[string]any)["recent_tasks"])
	}
	if text := recent[0]["task"].(string); len([]rune(text)) != consoleOverviewTaskTextSize+1 || !strings.HasSuffix(text, "…") {
		t.Fatalf("task text not bounded: %d runes", len([]rune(text)))
	}
	counts := value.(map[string]any)["task_counts"].(map[string]int)
	if counts["active"] != 1 || counts["failed_24h"] != 1 || counts["succeeded_24h"] != consoleOverviewRecentTasks+2 {
		t.Fatalf("task counts = %v", counts)
	}
	if !strings.Contains(raw, `"name":"Helper"`) || strings.Contains(raw, "unavailable") {
		t.Fatalf("bots/unavailable = %s", raw)
	}
}

func TestConsoleOverviewDegradesWithoutTasksOrBridge(t *testing.T) {
	consoleTestOperator(t)
	backend := &consoleBackend{hub: consoleTestHub(), tasks: &consoleOverviewTasks{err: fmt.Errorf("private task index failure")}}
	value, err := backend.snapshotResource(context.Background(), "overview", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, `"unavailable":["tasks"]`) || strings.Contains(raw, "private task index failure") || strings.Contains(raw, `"bots"`) || !strings.Contains(raw, `"name":"chat"`) {
		t.Fatalf("degraded overview = %s", raw)
	}
}

// consoleBlockedTasks never answers; only the caller's deadline ends the call.
type consoleBlockedTasks struct{ pb.AmuxTaskClient }

func (consoleBlockedTasks) ListSubtasks(ctx context.Context, _ *pb.ListSubtasksRequest, _ ...grpc.CallOption) (*pb.ListSubtasksResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConsoleOverviewDoesNotWaitForeverOnASlowTaskIndex(t *testing.T) {
	consoleTestOperator(t)
	previous := consoleOverviewTaskTimeout
	consoleOverviewTaskTimeout = 20 * time.Millisecond
	t.Cleanup(func() { consoleOverviewTaskTimeout = previous })
	backend := &consoleBackend{hub: consoleTestHub(), tasks: consoleBlockedTasks{}}
	started := time.Now()
	value, err := backend.snapshotResource(context.Background(), "overview", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("overview waited %s for the task index", elapsed)
	}
	raw := consoleTestJSON(t, value)
	if !strings.Contains(raw, `"unavailable":["tasks"]`) || !strings.Contains(raw, `"name":"chat"`) || strings.Contains(raw, "recent_tasks") {
		t.Fatalf("overview with a stalled task index = %s", raw)
	}
}

// consoleCountingHub counts the canonical replays a detail read performs.
type consoleCountingHub struct {
	*consoleResourceHub
	attaches atomic.Int32
}

func (h *consoleCountingHub) Attach(ctx context.Context, req *pb.AmuxdAttachRequest, opts ...grpc.CallOption) (pb.Amuxd_AttachClient, error) {
	h.attaches.Add(1)
	return h.consoleResourceHub.Attach(ctx, req, opts...)
}

func TestConsoleSessionDetailLightReadSkipsTheTurnReplay(t *testing.T) {
	consoleTestOperator(t)
	hub := &consoleCountingHub{consoleResourceHub: consoleTestHub()}
	backend := &consoleBackend{hub: hub}
	query := url.Values{"name": {"chat"}, "target": {"runtime-a"}, "activity": {"0"}}
	value, err := backend.snapshotResource(context.Background(), "session", query)
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if hub.attaches.Load() != 0 || !strings.Contains(raw, `"activity_status":"unknown"`) || strings.Contains(raw, `"activity":{`) || !strings.Contains(raw, `"state":"running"`) {
		t.Fatalf("light read replayed=%d body=%s", hub.attaches.Load(), raw)
	}
	query.Del("activity")
	value, err = backend.snapshotResource(context.Background(), "session", query)
	if err != nil {
		t.Fatal(err)
	}
	if raw = consoleTestJSON(t, value); hub.attaches.Load() != 1 || !strings.Contains(raw, `"activity_status":"known"`) {
		t.Fatalf("full read replayed=%d body=%s", hub.attaches.Load(), raw)
	}
}

func TestConsoleSessionDetailReportsCanonicalActivity(t *testing.T) {
	consoleTestOperator(t)
	backend := &consoleBackend{hub: consoleTestHub()}
	value, err := backend.snapshotResource(context.Background(), "session", url.Values{"name": {"chat"}, "target": {"runtime-a"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	for _, expected := range []string{`"activity_status":"known"`, `"activity":{`, `"turn_state":"idle"`, `"active_turn":false`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing %s in %s", expected, raw)
		}
	}
}
