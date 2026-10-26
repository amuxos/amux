package hub

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/cleanup"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type cleanupNotifier interface {
	CleanupRecipientKey() string
	NotifyDailyCleanup(context.Context, cleanup.Report) error
}
type cleanupDelivery struct {
	Report    cleanup.Report  `json:"report"`
	Delivered map[string]bool `json:"delivered"`
}
type dailyCleaner struct {
	mu           sync.Mutex
	path         string
	readSettings func() (config.DailyCleanupSettings, error)
	targets      func() []*pb.Target
	execute      func(context.Context, *pb.CleanupRequest) (*pb.CleanupResponse, error)
	notifiers    []cleanupNotifier
}

func newDailyCleaner(s *hubServer, bridges []*bridge.Bridge) *dailyCleaner {
	c := &dailyCleaner{path: filepath.Join(amuxhome.Dir(), "hub", "daily-cleanup.json"), readSettings: config.ReadDailyCleanupSettings, targets: s.hub.ListTargets}
	c.execute = func(ctx context.Context, req *pb.CleanupRequest) (*pb.CleanupResponse, error) {
		return routeUnary[pb.CleanupRequest, pb.CleanupResponse](s, ctx, req.TargetId, "Cleanup", req)
	}
	for _, b := range bridges {
		c.notifiers = append(c.notifiers, b)
	}
	return c
}
func (c *dailyCleaner) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := c.sweep(ctx, time.Now()); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "hub: daily cleanup: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func cleanupDue(now time.Time, at string) bool {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return false
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	return !now.Before(due)
}
func (c *dailyCleaner) sweep(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	settings, err := c.readSettings()
	if err != nil {
		return err
	}
	history := map[string]*cleanupDelivery{}
	if err := readCleanupDeliveries(c.path, &history); err != nil {
		return err
	}
	if settings.Enabled && cleanupDue(now, settings.At) {
		for _, target := range c.targets() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			id := "daily/" + now.Format("2006-01-02") + "/" + target.Id
			if _, ok := history[id]; ok {
				continue
			}
			// Verify journal writability before requesting destructive work. A crash
			// before recording the result reuses this deterministic Runtime request ID.
			if err := cleanup.WriteJSON(c.path, history); err != nil {
				return err
			}
			req := &pb.CleanupRequest{TargetId: target.Id, RunId: id, ProtectedPaths: settings.ProtectedPaths}
			report := c.executeReport(ctx, req, now)
			history[id] = &cleanupDelivery{Report: report, Delivered: map[string]bool{}}
			if err := cleanup.WriteJSON(c.path, history); err != nil {
				return err
			}
		}
	}
	return c.deliver(ctx, history, now)
}
func (c *dailyCleaner) executeReport(ctx context.Context, req *pb.CleanupRequest, now time.Time) cleanup.Report {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	response, err := c.execute(runCtx, req)
	if err == nil && len(response.GetReports()) == 1 {
		return cleanup.FromProto(response.Reports[0])
	}
	if err == nil {
		err = fmt.Errorf("invalid runtime cleanup response")
	}
	return cleanup.Report{ID: req.RunId, Target: req.TargetId, StartedAt: now, FinishedAt: time.Now(), DryRun: req.DryRun, Steps: []cleanup.Step{{Name: "isolation", Errors: []string{err.Error()}}}}
}
func (c *dailyCleaner) deliver(ctx context.Context, history map[string]*cleanupDelivery, now time.Time) error {
	var first error
	for id, entry := range history {
		if entry == nil {
			return fmt.Errorf("invalid cleanup journal entry")
		}
		all := len(c.notifiers) > 0
		for _, n := range c.notifiers {
			key := n.CleanupRecipientKey()
			if key == "" {
				all = false
				continue
			}
			if entry.Delivered[key] {
				continue
			}
			notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := n.NotifyDailyCleanup(notifyCtx, entry.Report)
			cancel()
			if err != nil {
				all = false
				if first == nil {
					first = err
				}
				continue
			}
			if entry.Delivered == nil {
				entry.Delivered = map[string]bool{}
			}
			entry.Delivered[key] = true
			if err := cleanup.WriteJSON(c.path, history); err != nil {
				return err
			}
		}
		if all && entry.Report.FinishedAt.Before(now.Add(-30*24*time.Hour)) {
			delete(history, id)
		}
	}
	if err := cleanup.WriteJSON(c.path, history); err != nil {
		return err
	}
	return first
}
func (s *hubServer) Cleanup(ctx context.Context, req *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	// System-wide destructive maintenance is deliberately narrower than Session
	// prune: enrolled/Agent identities cannot invoke it, even without isolation.
	md, _ := metadata.FromIncomingContext(ctx)
	for _, value := range md.Get("x-amux-actor-token") {
		if strings.TrimSpace(value) != "" {
			return nil, status.Error(codes.PermissionDenied, "system cleanup requires the local host operator")
		}
	}
	remote, ok := peer.FromContext(ctx)
	if !ok || remote.Addr == nil {
		return nil, status.Error(codes.PermissionDenied, "system cleanup requires a local connection")
	}
	if remote.Addr.Network() != "unix" {
		host, _, err := net.SplitHostPort(remote.Addr.String())
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return nil, status.Error(codes.PermissionDenied, "system cleanup requires a local connection")
		}
	}
	if s.dailyCleaner == nil {
		return nil, status.Error(codes.Unavailable, "daily cleanup is unavailable")
	}
	c := s.dailyCleaner
	c.mu.Lock()
	defer c.mu.Unlock()
	settings, err := c.readSettings()
	if err != nil {
		return nil, err
	}
	history := map[string]*cleanupDelivery{}
	if !req.DryRun {
		if err := readCleanupDeliveries(c.path, &history); err != nil {
			return nil, err
		}
		if err := cleanup.WriteJSON(c.path, history); err != nil {
			return nil, err
		}
	}
	out := &pb.CleanupResponse{}
	for _, target := range c.targets() {
		if req.TargetId != "" && target.Id != req.TargetId && target.Name != req.TargetId {
			continue
		}
		r := c.executeReport(ctx, &pb.CleanupRequest{TargetId: target.Id, DryRun: req.DryRun, RunId: uuid.NewString(), ProtectedPaths: settings.ProtectedPaths}, time.Now())
		out.Reports = append(out.Reports, r.Proto())
		if !req.DryRun {
			history[r.ID] = &cleanupDelivery{Report: r, Delivered: map[string]bool{}}
			if err := cleanup.WriteJSON(c.path, history); err != nil {
				return nil, err
			}
		}
	}
	if len(out.Reports) == 0 {
		return nil, status.Error(codes.NotFound, "no matching connected runtime")
	}
	if !req.DryRun {
		if err := c.deliver(ctx, history, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "hub: cleanup report delivery pending: %v\n", err)
		}
	}
	return out, nil
}

func readCleanupDeliveries(path string, history *map[string]*cleanupDelivery) error {
	if err := cleanup.ReadJSON(path, history); err != nil {
		return err
	}
	for _, entry := range *history {
		if entry == nil {
			return fmt.Errorf("invalid cleanup journal entry")
		}
	}
	return nil
}
