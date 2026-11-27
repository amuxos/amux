package runtime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/sessioncredential"
)

func (s *server) ObservePersonalSchedule(ctx context.Context, binding feishumsg.PersonalScheduleIdentity, emit func(feishumsg.PersonalScheduleEvent) error) error {
	hook := distributionHooks.ObservePersonalSchedule
	if hook == nil || s.isolationRuntime == nil || !binding.Valid() {
		return errors.New(feishumsg.Copy("personal_unavailable"))
	}
	return hook(ctx, binding, s.personalScheduleCommand(binding), func(ctx context.Context, name string, args, env []string, receive func([]byte) error) error {
		return withIsolatedCommand(ctx, s.isolationRuntime, binding.Execution, name, args, env, "", func(process isolation.ProcessHandle, kill func()) error {
			// Keep stdin open. An unbounded CLI event consumer stops on stdin EOF.
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				_, _ = io.Copy(io.Discard, process.Stderr())
			}()
			scanner := bufio.NewScanner(process.Stdout())
			scanner.Buffer(make([]byte, 4096), sessionCredentialOutputLimit)
			for scanner.Scan() {
				if err := receive(scanner.Bytes()); err != nil {
					kill()
					<-drained
					return err
				}
			}
			if err := scanner.Err(); err != nil {
				kill()
				<-drained
				return err
			}
			<-drained
			exit, err := process.Wait(ctx)
			if err == nil && exit.Code != 0 {
				err = feishumsg.ErrPersonalBot
			}
			return err
		})
	}, emit)
}

func (s *server) WritePersonalScheduleCard(ctx context.Context, card feishumsg.PersonalScheduleCard) (feishumsg.PersonalResult, error) {
	hook := distributionHooks.WritePersonalScheduleCard
	if hook == nil || s.isolationRuntime == nil || !card.Binding.Valid() {
		return feishumsg.PersonalResult{}, errors.New(feishumsg.Copy("personal_unavailable"))
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return hook(ctx, card, s.personalScheduleCommand(card.Binding))
}

func (s *server) personalScheduleCommand(binding feishumsg.PersonalScheduleIdentity) feishumsg.RunPersonalCommand {
	return func(ctx context.Context, name string, args, env []string, input string) (sessioncredential.CommandOutput, error) {
		return runIsolatedCommand(ctx, s.isolationRuntime, binding.Execution, name, args, env, input, nil)
	}
}

func (s *maintenanceSessionServer) ObservePersonalSchedule(ctx context.Context, binding feishumsg.PersonalScheduleIdentity, emit func(feishumsg.PersonalScheduleEvent) error) error {
	runtime, ok := s.AmuxletServer.(*server)
	if !ok {
		return errors.New(feishumsg.Copy("personal_unavailable"))
	}
	ctx, done, err := s.maintenance.enterObserver(ctx)
	if err != nil {
		return err
	}
	defer done()
	return runtime.ObservePersonalSchedule(ctx, binding, emit)
}

func (s *maintenanceSessionServer) WritePersonalScheduleCard(ctx context.Context, card feishumsg.PersonalScheduleCard) (feishumsg.PersonalResult, error) {
	runtime, ok := s.AmuxletServer.(*server)
	if !ok {
		return feishumsg.PersonalResult{}, errors.New(feishumsg.Copy("personal_unavailable"))
	}
	done, err := s.maintenance.enter(true)
	if err != nil {
		return feishumsg.PersonalResult{}, err
	}
	defer done()
	return runtime.WritePersonalScheduleCard(ctx, card)
}
