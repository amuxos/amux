package hub

import (
	"context"
	"errors"
	"github.com/amuxos/amux/internal/cleanup"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"net"
	"path/filepath"
	"testing"
	"time"
)

type cleanupTestNotifier struct {
	fail    bool
	calls   int
	reports []cleanup.Report
}

func (n *cleanupTestNotifier) CleanupRecipientKey() string { return "owner" }
func (n *cleanupTestNotifier) NotifyDailyCleanup(_ context.Context, r cleanup.Report) error {
	n.calls++
	if n.fail {
		return errors.New("delivery offline")
	}
	n.reports = append(n.reports, r)
	return nil
}
func TestDailyCleanupSchedulingRestartAndDeliveryRetry(t *testing.T) {
	now := time.Date(2026, 10, 26, 6, 17, 0, 0, time.FixedZone("CST", 8*3600))
	calls := 0
	enabled := true
	n := &cleanupTestNotifier{fail: true}
	c := &dailyCleaner{path: filepath.Join(t.TempDir(), "state.json"), readSettings: func() (config.DailyCleanupSettings, error) {
		return config.DailyCleanupSettings{Enabled: enabled, At: "06:17"}, nil
	}, targets: func() []*pb.Target { return []*pb.Target{{Id: "local"}} }, notifiers: []cleanupNotifier{n}}
	c.execute = func(_ context.Context, req *pb.CleanupRequest) (*pb.CleanupResponse, error) {
		calls++
		return &pb.CleanupResponse{Reports: []*pb.CleanupReport{(cleanup.Report{ID: req.RunId, Target: req.TargetId, StartedAt: now, FinishedAt: now}).Proto()}}, nil
	}
	if err := c.sweep(context.Background(), now.Add(-time.Second)); err != nil || calls != 0 {
		t.Fatalf("before due: %v calls=%d", err, calls)
	}
	if err := c.sweep(context.Background(), now); err == nil || calls != 1 || n.calls != 1 {
		t.Fatalf("initial: %v calls=%d notify=%d", err, calls, n.calls)
	}
	n.fail = false
	restarted := &dailyCleaner{path: c.path, readSettings: c.readSettings, targets: c.targets, execute: c.execute, notifiers: c.notifiers}
	if err := restarted.sweep(context.Background(), now.Add(time.Hour)); err != nil || calls != 1 || len(n.reports) != 1 {
		t.Fatalf("restart: %v calls=%d reports=%d", err, calls, len(n.reports))
	}
	if err := restarted.sweep(context.Background(), now.Add(2*time.Hour)); err != nil || n.calls != 2 {
		t.Fatalf("duplicate notification: %v %d", err, n.calls)
	}
	enabled = false
	if err := restarted.sweep(context.Background(), now.Add(24*time.Hour)); err != nil || calls != 1 {
		t.Fatal("disabled cleanup ran")
	}
	enabled = true
	if err := restarted.sweep(context.Background(), now.Add(24*time.Hour)); err != nil || calls != 2 {
		t.Fatalf("next day: %v %d", err, calls)
	}
}
func TestDailyCleanupCorruptJournalFailsClosed(t *testing.T) {
	c := &dailyCleaner{path: filepath.Join(t.TempDir(), "bad.json"), readSettings: func() (config.DailyCleanupSettings, error) {
		return config.DailyCleanupSettings{Enabled: true, At: "00:00"}, nil
	}, targets: func() []*pb.Target { t.Fatal("read targets after corrupt journal"); return nil }}
	if err := cleanup.WriteJSON(c.path, []string{"invalid shape"}); err != nil {
		t.Fatal(err)
	}
	if err := c.sweep(context.Background(), time.Now()); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}
func TestDailyCleanupRejectsActorsAndRemoteCallers(t *testing.T) {
	srv := &hubServer{}
	local := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}})
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(local, metadata.Pairs("x-amux-actor-token", "agent-token")), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1}})} {
		_, err := srv.Cleanup(ctx, &pb.CleanupRequest{DryRun: true})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got %v", err)
		}
	}
	_, err := srv.Cleanup(local, &pb.CleanupRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("host operator rejected: %v", err)
	}
}

func TestDailyCleanupNullJournalFailsClosed(t *testing.T) {
	for _, contents := range []any{nil, map[string]any{"old": nil}} {
		c := &dailyCleaner{path: filepath.Join(t.TempDir(), "state.json"), readSettings: func() (config.DailyCleanupSettings, error) {
			return config.DailyCleanupSettings{Enabled: true, At: "00:00"}, nil
		}, targets: func() []*pb.Target { t.Fatal("corrupt journal reached execution"); return nil }}
		if err := cleanup.WriteJSON(c.path, contents); err != nil {
			t.Fatal(err)
		}
		if err := c.sweep(context.Background(), time.Now()); err == nil {
			t.Fatal("null journal accepted")
		}
	}
}
