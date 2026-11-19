package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var updateMaintenanceNeeded = func() (bool, error) {
	if runtime.GOOS != "linux" {
		return false, nil
	}
	if _, err := os.Stat(isolationHelperUnitPath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	cfg, err := config.LoadIsolationFrom(filepath.Join(defaultAmuxDir(), "config.toml"))
	if err != nil {
		return false, err
	}
	socket := cfg.HelperSocket
	if socket == "" {
		socket = filepath.Join(defaultAmuxDir(), "isolation-helper.sock")
	}
	uid, _, _ := helperServiceDefaults()
	return helperUpgradeTarget(socket, uid)
}
var updateMaintenanceDial = newLocalRuntimeClientWithTimeout

type updateMaintenance struct {
	cmd   *cobra.Command
	token string
	stop  context.CancelFunc
	done  chan struct{}
	once  sync.Once
}

func maintenanceText(cmd *cobra.Command, key string, args ...any) string {
	return commandCatalog.Text("update.maintenance."+key, args...).For(commandLocale(cmd))
}
func (m *updateMaintenance) call(ctx context.Context, action string) (*pb.MaintenanceResponse, error) {
	client, err := updateMaintenanceDial(time.Second)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return pb.NewRuntimeMaintenanceClient(client.conn).Maintain(ctx, &pb.MaintenanceRequest{Action: action, Token: m.token})
}
func beginUpdateMaintenance(cmd *cobra.Command, opts *installOptions) (*updateMaintenance, error) {
	needed, err := updateMaintenanceNeeded()
	if err != nil {
		return nil, err
	}
	if !needed {
		return nil, nil
	}
	token := opts.maintenanceToken
	if token == "" {
		token = uuid.NewString()
	}
	m := &updateMaintenance{cmd: cmd, token: token, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	_, err = m.call(ctx, "begin")
	cancel()
	if status.Code(err) == codes.Unimplemented {
		// Bootstrapping an old Runtime must never remove the helper's final guard.
		fmt.Fprintln(cmd.OutOrStdout(), maintenanceText(cmd, "legacy"))
		return nil, nil
	}
	if err != nil {
		// A stopped Runtime has nothing to coordinate. The root helper still checks
		// all actual launches, including orphan processes, before replacement.
		if _, statErr := os.Stat(filepath.Join(defaultAmuxDir(), "runtime.sock")); os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", maintenanceText(cmd, "failed"), err)
	}
	opts.maintenanceToken = token
	heartbeatCtx, stop := context.WithCancel(context.Background())
	m.stop = stop
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
			}
			ctx, cancel := context.WithTimeout(heartbeatCtx, 5*time.Second)
			_, _ = m.call(ctx, "renew")
			cancel()
		}
	}()
	fmt.Fprintln(cmd.OutOrStdout(), maintenanceText(cmd, "draining"))
	timeout := opts.drainTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel = context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	var last *pb.MaintenanceResponse
	for {
		pollCtx, pollCancel := context.WithTimeout(ctx, 30*time.Second)
		response, pollErr := m.call(pollCtx, "poll")
		err = pollErr
		if response != nil {
			last = response
		}
		pollCancel()
		if err != nil {
			break
		}
		if last.Phase == "ready" {
			fmt.Fprintln(cmd.OutOrStdout(), maintenanceText(cmd, "ready", last.SleepingSessions))
			return m, nil
		}
		if last.Phase != "draining" {
			err = errors.New(maintenanceText(cmd, "expired"))
			break
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			err = ctx.Err()
		case <-timer.C:
		}
		if err != nil {
			break
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		busy, tasks := 0, int32(0)
		if last != nil {
			busy = len(last.BusySessions)
			tasks = last.ActiveTasks
		}
		err = fmt.Errorf("%s", maintenanceText(cmd, "timeout", timeout.String(), busy, tasks))
	} else {
		err = fmt.Errorf("%s: %w", maintenanceText(cmd, "failed"), err)
	}
	return nil, errors.Join(err, m.finish())
}
func (m *updateMaintenance) finish() error {
	if m == nil {
		return nil
	}
	var result error
	m.once.Do(func() {
		defer func() { m.stop(); <-m.done }()
		if err := waitForMaintenanceRecovery(m.call, 2*time.Minute, time.Second); err != nil {
			result = fmt.Errorf("%s: %w", maintenanceText(m.cmd, "recovery_failed"), err)
		}
	})
	return result
}

// Bound the time without progress. A large recovery can span several RPCs.
func waitForMaintenanceRecovery(call func(context.Context, string) (*pb.MaintenanceResponse, error), stallTimeout, retryInterval time.Duration) error {
	deadline := time.Now().Add(stallTimeout)
	var lastErr error
	previous := int32(-1)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		endCtx, endCancel := context.WithTimeout(ctx, 30*time.Second)
		response, err := call(endCtx, "end")
		endCancel()
		if err != nil {
			lastErr = err
			// gRPC drops the response on errors, including partial recovery
			// and batch deadlines. Older Runtimes lack this read-only action.
			statusCtx, statusCancel := context.WithTimeout(ctx, 2*time.Second)
			response, err = call(statusCtx, "status")
			statusCancel()
		}
		cancel()
		if err == nil && response != nil {
			if response.Phase == "idle" {
				return nil
			}
			if response.Phase == "recovering" {
				if previous >= 0 && response.SleepingSessions < previous {
					deadline = time.Now().Add(stallTimeout)
				}
				previous = response.SleepingSessions
			}
		}
		if wait := min(retryInterval, time.Until(deadline)); wait > 0 {
			time.Sleep(wait)
		}
	}
	return errors.Join(context.DeadlineExceeded, lastErr)
}
