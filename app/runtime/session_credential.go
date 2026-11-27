package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessioncredential"
	"github.com/google/uuid"
)

const sessionCredentialOutputLimit = 64 << 10

// IsolatedSessionCredential is dispatched by the trusted Hub tunnel. It is
// not a local gRPC method. The tunnel request never supplies an executable.
func (s *server) IsolatedSessionCredential(ctx context.Context, request sessioncredential.Request) sessioncredential.Result {
	policy := distributionHooks.IsolatedSessionCredential
	identity := request.Identity
	if policy == nil || s.isolationRuntime == nil || s.credentialLifetime == nil ||
		identity.Source != principal.SourceFeishu || identity.IsolationBackend != principal.BackendLinuxDAC ||
		!identity.Identified() || identity.OriginActorPrincipalID != identity.EffectivePrincipalID ||
		!request.Actor.Identified() {
		return sessioncredential.Result{State: sessioncredential.Unavailable}
	}
	switch request.Action {
	case sessioncredential.Probe, sessioncredential.Begin, sessioncredential.Poll, sessioncredential.Reauth:
	default:
		return sessioncredential.Result{State: sessioncredential.Unavailable}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return policy(ctx, s.credentialLifetime, request, func(ctx context.Context, name string, args, env []string, observeLine func(string)) (sessioncredential.CommandOutput, error) {
		return runIsolatedCommand(ctx, s.isolationRuntime, identity, name, args, env, "", observeLine)
	})
}

func (s *maintenanceSessionServer) IsolatedSessionCredential(ctx context.Context, request sessioncredential.Request) sessioncredential.Result {
	runtime, ok := s.AmuxletServer.(*server)
	if !ok {
		return sessioncredential.Result{State: sessioncredential.Unavailable}
	}
	done, err := s.maintenance.enter(true)
	if err != nil {
		return sessioncredential.Result{State: sessioncredential.Unavailable}
	}
	defer done()
	return runtime.IsolatedSessionCredential(ctx, request)
}

func runIsolatedCommand(ctx context.Context, rt *isolation.Runtime, identity principal.ExecutionIdentity, name string, args, env []string, input string, observeLine func(string)) (sessioncredential.CommandOutput, error) {
	var output sessioncredential.CommandOutput
	err := withIsolatedCommand(ctx, rt, identity, name, args, env, input, func(process isolation.ProcessHandle, kill func()) error {
		_ = process.Stdin().Close()
		var stdout, stderr []byte
		errorsRead := make(chan error, 2)
		read := func(reader io.Reader, dest *[]byte) {
			var err error
			*dest, err = readCredentialOutput(reader, observeLine)
			errorsRead <- err
		}
		go read(process.Stdout(), &stdout)
		go read(process.Stderr(), &stderr)
		for range 2 {
			if err := <-errorsRead; err != nil {
				kill()
				return err
			}
		}
		exit, err := process.Wait(ctx)
		output = sessioncredential.CommandOutput{Stdout: stdout, Stderr: stderr, ExitCode: exit.Code}
		return err
	})
	return output, err
}

func withIsolatedCommand(ctx context.Context, rt *isolation.Runtime, identity principal.ExecutionIdentity, name string, args, env []string, input string, use func(isolation.ProcessHandle, func()) error) error {
	commandEnv := ensureLaunchEnvPATH(env)
	launchID := uuid.NewString()
	identity.ExecutionStateRef = "session-credential:" + launchID
	paths, _, err := rt.PrepareExecution(ctx, identity, launchID, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: prepare execution: %v\n", err)
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		_ = rt.RemoveRun(cleanupCtx, identity, launchID)
		_ = rt.RemoveWorkspace(cleanupCtx, identity, paths.Workspace)
	}()
	if input != "" {
		// The helper retains a copy of the stdin writer for process handoff.
		// Commands that consume all of their input need a private regular file.
		stage, err := os.MkdirTemp("", "amux-isolated-command-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := os.WriteFile(filepath.Join(stage, "input.json"), []byte(input), 0o600); err != nil {
			return err
		}
		if err := rt.MaterializeExecution(ctx, identity, launchID, paths.Workspace, []isolation.BundleSource{{Source: stage, Prefix: "workspace"}}); err != nil {
			return err
		}
	}
	launch := isolation.LaunchContext{LaunchID: launchID, Identity: &identity, Backend: principal.BackendLinuxDAC}
	backend, err := rt.BackendForContext(launch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: select backend: %v\n", err)
		return err
	}
	commandEnv = append(commandEnv, "TMPDIR="+paths.Run)
	process, err := backend.Start(ctx, isolation.LaunchSpec{Path: name, Args: args, Dir: paths.Workspace, Env: commandEnv, Context: launch})
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: start command: %v\n", err)
		return err
	}
	defer process.Close()

	kill := func() {
		killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = process.Kill(killCtx)
		_ = process.Stdout().Close()
		_ = process.Stderr().Close()
	}
	stop := context.AfterFunc(ctx, kill)
	defer stop()
	return use(process, kill)
}

// The observer runs from both output drains and must be concurrency safe.
func readCredentialOutput(reader io.Reader, observeLine func(string)) ([]byte, error) {
	limited := io.LimitReader(reader, sessionCredentialOutputLimit+1)
	var buffer bytes.Buffer
	if observeLine == nil {
		if _, err := buffer.ReadFrom(limited); err != nil {
			return nil, err
		}
	} else {
		scanner := bufio.NewScanner(io.TeeReader(limited, &buffer))
		scanner.Buffer(make([]byte, 4096), sessionCredentialOutputLimit+1)
		for scanner.Scan() {
			observeLine(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	if buffer.Len() > sessionCredentialOutputLimit {
		return nil, errors.New("isolated credential response exceeds limit")
	}
	return buffer.Bytes(), nil
}
