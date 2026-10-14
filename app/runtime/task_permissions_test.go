package runtime

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
)

func TestPersistentChildCannotChangeFrozenTaskPermissionPolicy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var launches [][]string
	srv := &server{mgr: mgr, amuxDir: t.TempDir(), startSessionHook: func(_ adapter.Adapter, s *session.Session, args, _ []string) error {
		launches = append(launches, slices.Clone(args))
		s.SetProc(&serverTestProc{})
		return mgr.SetRunning(s)
	}}
	ctrl := newTaskController(srv, "t-1")
	spec := task.ChildSpec{TaskID: "first", OwnerTaskId: "first", InstanceID: "frozen", AgentType: "claude", PermissionPolicy: task.PolicyDenyAll, ExtraArgs: []string{"--model", "frozen-model", "--dangerously-skip-permissions"}}
	child, err := ctrl.CreateChild(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	sess := child.(*childSession).sess
	if summaries := ctrl.ListInstanceSummaries(); len(summaries) != 1 || instanceSummaryToProto(summaries[0], ctrl).PermissionPolicy != "deny-all" {
		t.Fatalf("runtime policy readback = %+v", summaries)
	}
	want := []string{"--model", "frozen-model", "--permission-mode", "plan"}
	if !slices.Equal(sess.Snapshot().ExtraArgs, want) || !strings.Contains(sess.Snapshot().LaunchSpec, `"task_permission_policy":"deny-all"`) {
		t.Fatalf("launch snapshot = %+v", sess.Snapshot())
	}
	if got := launches[0]; len(got) != len(want)+2 || !slices.Equal(got[:len(want)], want) || got[len(want)] != "--append-system-prompt" || !strings.Contains(got[len(want)+1], "Task-owned child") {
		t.Fatalf("launch args = %q, want frozen permissions and native restricted guidance", got)
	}
	for _, sleeping := range []bool{false, true} {
		if sleeping {
			if err := mgr.SetSleeping(sess, "test"); err != nil {
				t.Fatal(err)
			}
		}
		spec.PermissionPolicy = task.PolicyAllow
		if _, err := ctrl.CreateChild(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "deny-all") || !strings.Contains(err.Error(), "policy-allow") || !strings.Contains(err.Error(), "新建") {
			t.Fatalf("policy change error = %v", err)
		}
	}
	spec.PermissionPolicy = task.PolicyDenyAll
	spec.ExtraArgs = []string{"--model", "changed-model"}
	if _, err := ctrl.CreateChild(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(launches) != 2 || !slices.Equal(launches[0], launches[1]) {
		t.Fatalf("resume changed native boundary/model: %q", launches)
	}
}

func TestLegacyPersistentChildDoesNotSilentlyReuseUnknownPermissions(t *testing.T) {
	for _, snap := range []session.Snapshot{{}, {LaunchSpec: `{}`}, {LaunchSpec: `{"profile_spec":{"ref":"builtin/claude","permission_policy":"policy-allow"}}`}} {
		err := validateTaskPermissionResume(snap, task.PolicyAllow)
		if err == nil || !strings.Contains(err.Error(), "create a new instance") || !strings.Contains(err.Error(), "新建实例") {
			t.Fatalf("legacy permission error = %v", err)
		}
	}
}
