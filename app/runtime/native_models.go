package runtime

import (
	"context"
	"os"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider/codex"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) ListNativeModels(parent context.Context, req *pb.ListNativeModelsRequest) (*pb.ListNativeModelsResponse, error) {
	if !config.SupportsAgentExecutable(req.GetAgentType()) {
		return nil, status.Error(codes.InvalidArgument, config.ExecutableError("unsupported", req.GetAgentType()).Error())
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	identity := principal.FromProtoIdentity(req.ExecutionIdentity)
	launchID := "models-" + uuid.NewString()
	// Discovery has its own temporary workspace, never an existing session's.
	identity.ExecutionStateRef = launchID
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cwd, _, identity, paths, err := s.prepareExecution(ctx, identity, launchID, &pb.RunRequest{Cwd: home})
	if err != nil {
		return nil, err
	}
	if identity.IsolationBackend == principal.BackendLinuxDAC {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.isolationRuntime.RemoveRun(cleanup, identity, launchID)
			_ = s.isolationRuntime.RemoveWorkspace(cleanup, identity, paths.Workspace)
		}()
	}
	launchContext := isolation.LaunchContext{LaunchID: launchID, Identity: &identity, Backend: identity.IsolationBackend}
	backend, err := s.isolationRuntime.BackendForContext(launchContext)
	if err != nil {
		return nil, err
	}
	env := append(os.Environ(), s.launchEnvForIdentity(identity, nil)...)
	executable := req.AgentExecutable
	if executable == "" {
		executable = config.GetAgentExecutable(req.AgentType)
	}
	resolutionEnv := env
	if identity.IsolationBackend == principal.BackendLinuxDAC {
		resolutionEnv = append(append([]string{}, env...), "HOME="+paths.Home)
	}
	executable, err = resolveAgentExecutable(req.AgentType, executable, resolutionEnv)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	models, signedIn, err := codex.DiscoverModels(ctx, codex.StartOptions{
		Backend: backend, LaunchContext: launchContext, Cwd: cwd, Executable: executable,
		Env: env, ProviderName: req.AgentType,
	})
	if err != nil {
		return nil, err
	}
	result := &pb.ListNativeModelsResponse{Status: "signed_out"}
	if !signedIn {
		return result, nil
	}
	result.Status = "ready"
	for _, model := range models {
		entry := &pb.NativeModel{Model: model.Model, DisplayName: model.DisplayName,
			DefaultReasoningEffort: model.DefaultReasoningEffort, IsDefault: model.IsDefault}
		for _, effort := range model.SupportedReasoningEfforts {
			entry.ReasoningEfforts = append(entry.ReasoningEfforts, effort.ReasoningEffort)
		}
		for _, variant := range model.Variants {
			entry.Variants = append(entry.Variants, &pb.NativeModelVariant{DisplayName: variant.DisplayName, BackendVariant: variant.BackendVariant})
		}
		result.Models = append(result.Models, entry)
	}
	return result, nil
}
