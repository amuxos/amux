package runtime

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestRunDefaultsToBypassAndPreservesExplicitPermissionsOnRestart(t *testing.T) {
	for _, tc := range []struct {
		name, engine string
		args, want   []string
	}{
		{"codex default", "codex", nil, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"claude default", "claude", nil, []string{"--permission-mode", "bypassPermissions"}},
		{"codex explicit", "codex", []string{"-a", "on-request", "-s", "read-only"}, []string{"-a", "on-request", "-s", "read-only"}},
		{"claude explicit", "claude", []string{"--permission-mode", "plan"}, []string{"--permission-mode", "plan"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			var launched *session.Session
			var launches [][]string
			srv := &server{mgr: mgr, amuxDir: t.TempDir(), disableMCPAutoInject: true,
				startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
					launched = sess
					launches = append(launches, slices.Clone(args))
					sess.SetProc(&serverTestProc{})
					return mgr.SetRunning(sess)
				},
			}
			_, err = srv.Run(context.Background(), &pb.RunRequest{Name: "permission-default", AgentType: tc.engine, ExtraArgs: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if got := launched.Snapshot().ExtraArgs; !slices.Equal(got, tc.want) {
				t.Fatalf("recorded args = %q, want %q", got, tc.want)
			}
			if len(launches[0]) < len(tc.want) || !slices.Equal(launches[0][:len(tc.want)], tc.want) {
				t.Fatalf("native launch did not receive permissions: %q", launches[0])
			}
			if err := mgr.SetSleeping(launched, "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.Restart(context.Background(), &pb.RestartRequest{Name: launched.Name}); err != nil {
				t.Fatal(err)
			}
			if len(launches) != 2 || !slices.Equal(launches[0], launches[1]) {
				t.Fatalf("restart changed native settings: %q", launches)
			}
		})
	}
}
