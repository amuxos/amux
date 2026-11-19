package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		return runSessionCredentialCommand(ctx, s.isolationRuntime, identity, name, args, env, observeLine)
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

func runSessionCredentialCommand(ctx context.Context, rt *isolation.Runtime, identity principal.ExecutionIdentity, name string, args, env []string, observeLine func(string)) (sessioncredential.CommandOutput, error) {
	var output sessioncredential.CommandOutput
	executable, err := exec.LookPath(name)
	if err != nil {
		return output, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return output, err
	}
	commandPATH := "PATH=" + strings.Join(uniquePathEntries(appendStandardUserBinPaths(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)))), string(os.PathListSeparator))
	launchID := uuid.NewString()
	identity.ExecutionStateRef = "session-credential:" + launchID
	paths, _, err := rt.PrepareExecution(ctx, identity, launchID, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: prepare execution: %v\n", err)
		return output, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		_ = rt.RemoveRun(cleanupCtx, identity, launchID)
		_ = rt.RemoveWorkspace(cleanupCtx, identity, paths.Workspace)
	}()
	launch := isolation.LaunchContext{LaunchID: launchID, Identity: &identity, Backend: principal.BackendLinuxDAC}
	backend, err := rt.BackendForContext(launch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: select backend: %v\n", err)
		return output, err
	}
	commandEnv := append([]string{commandPATH}, env...)
	commandEnv = append(commandEnv, "TMPDIR="+paths.Run)
	process, err := backend.Start(ctx, isolation.LaunchSpec{Path: executable, Args: args, Dir: paths.Workspace, Env: commandEnv, Context: launch})
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolated session credential: start command: %v\n", err)
		return output, err
	}
	defer process.Close()
	_ = process.Stdin().Close()

	kill := func() {
		killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = process.Kill(killCtx)
		_ = process.Stdout().Close()
		_ = process.Stderr().Close()
	}
	stop := context.AfterFunc(ctx, kill)
	defer stop()
	errorsRead := make(chan error, 2)
	read := func(reader io.Reader, dest *[]byte) {
		var err error
		*dest, err = readCredentialOutput(reader, observeLine)
		errorsRead <- err
	}
	go read(process.Stdout(), &output.Stdout)
	go read(process.Stderr(), &output.Stderr)
	for range 2 {
		if err := <-errorsRead; err != nil {
			kill()
			return sessioncredential.CommandOutput{}, err
		}
	}
	exit, err := process.Wait(ctx)
	output.ExitCode = exit.Code
	return output, err
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
