package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func guardHubStop(force bool, impacts []string) error {
	if len(impacts) == 0 || force {
		return nil
	}
	return fmt.Errorf("hub stop will temporarily disable: %s\nUse `amux system stop --force` to continue.", strings.Join(impacts, "; "))
}

func collectHubStopImpacts(ctx context.Context) []string {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	_ = loadUserConfig()

	var impacts []string
	if addr := hubAddr(); addr != "" {
		if hc, err := newHubClientWithTimeout(addr, 500*time.Millisecond); err == nil {
			defer hc.Close()
			if targets, err := hc.ListTargets(ctx); err == nil && len(targets) > 0 {
				impacts = append(impacts, fmt.Sprintf("connected targets: %d", len(targets)))
			}
			if schedules, err := hc.c.ListSchedules(ctx, &pb.ListSchedulesRequest{}); err == nil {
				enabled := 0
				for _, item := range schedules.Schedules {
					if item.Enabled {
						enabled++
					}
				}
				if enabled > 0 {
					impacts = append(impacts, fmt.Sprintf("enabled schedules: %d", enabled))
				}
			}
		}
	}
	if bots, err := feishuBots(ctx); err == nil {
		if configured := countConfiguredFeishuBots(bots); configured > 0 {
			impacts = append(impacts, fmt.Sprintf("Feishu bridge profiles: %d", configured))
		}
	} else if status, err := feishuStatus(ctx, ""); err == nil && status.Configured {
		impacts = append(impacts, "Feishu bridge")
	}
	return impacts
}

type hubServiceOptions struct {
	WebAddr    string
	WebToken   string
	WebBaseURL string
}

func hubService(opts ...hubServiceOptions) managedService {
	_ = loadUserConfig()
	addr := hubAddr()
	args := []string{"start"}
	if addr != "" {
		args = append(args, "--addr", addr)
	}
	var opt hubServiceOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.WebAddr != "" {
		args = append(args, "--web-addr", opt.WebAddr)
	}
	if opt.WebToken != "" {
		args = append(args, "--web-token", opt.WebToken)
	}
	if opt.WebBaseURL != "" {
		args = append(args, "--web-base-url", opt.WebBaseURL)
	}
	return managedService{
		Name:       "hub",
		Binary:     "amuxd",
		Args:       args,
		PIDPath:    filepath.Join(defaultAmuxDir(), "hub.pid"),
		LogPath:    filepath.Join(defaultAmuxDir(), "hub.log"),
		ListenAddr: hubListenAddr(addr),
	}
}

// hubListenAddr maps the configured hub addr to a bindable probe address. An
// empty config means amuxd defaults to the local-only loopback listener; a bare
// ":port" or "0.0.0.0:port" binds all interfaces and is an explicit distributed
// mode choice. The probe uses the same form so addrInUse / reclaim act on the
// port the daemon actually listens on.
func hubListenAddr(addr string) string {
	if addr == "" {
		return defaultLocalHubAddr
	}
	return addr
}
