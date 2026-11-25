package runtime

import (
	"context"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type maintenanceSessionServer struct {
	pb.AmuxletServer
	maintenance *runtimeMaintenance
}
type maintenanceTaskServer struct {
	pb.AmuxTaskServer
	maintenance *runtimeMaintenance
}

func (s *maintenanceSessionServer) Run(ctx context.Context, req *pb.RunRequest) (*pb.RunResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Run(ctx, req)
}
func (s *maintenanceSessionServer) Restart(ctx context.Context, req *pb.RestartRequest) (*pb.RunResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Restart(ctx, req)
}
func (s *maintenanceSessionServer) Resume(ctx context.Context, req *pb.ResumeRequest) (*pb.RunResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Resume(ctx, req)
}
func (s *maintenanceSessionServer) Submit(ctx context.Context, req *pb.SubmitRequest) (*pb.SubmitResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Submit(ctx, req)
}
func (s *maintenanceSessionServer) Goal(ctx context.Context, req *pb.GoalRequest) (*pb.GoalResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Goal(ctx, req)
}
func (s *maintenanceSessionServer) ChangeCwd(ctx context.Context, req *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.ChangeCwd(ctx, req)
}
func (s *maintenanceSessionServer) Sleep(ctx context.Context, req *pb.SleepRequest) (*pb.SleepResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Sleep(ctx, req)
}
func (s *maintenanceSessionServer) Kill(ctx context.Context, req *pb.KillRequest) (*pb.KillResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Kill(ctx, req)
}
func (s *maintenanceSessionServer) Prune(ctx context.Context, req *pb.PruneRequest) (*pb.PruneResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Prune(ctx, req)
}
func (s *maintenanceSessionServer) ListNativeModels(ctx context.Context, req *pb.ListNativeModelsRequest) (*pb.ListNativeModelsResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.ListNativeModels(ctx, req)
}
func (s *maintenanceSessionServer) PutSessionFile(ctx context.Context, req *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	done, err := s.maintenance.enter(false)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.PutSessionFile(ctx, req)
}
func (s *maintenanceSessionServer) Interrupt(ctx context.Context, req *pb.InterruptRequest) (*pb.InterruptResponse, error) {
	done, err := s.maintenance.enter(false)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.Interrupt(ctx, req)
}
func (s *maintenanceSessionServer) RespondPermission(ctx context.Context, req *pb.RespondPermissionRequest) (*pb.RespondPermissionResponse, error) {
	done, err := s.maintenance.enter(false)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.RespondPermission(ctx, req)
}
func (s *maintenanceSessionServer) ReadSessionArtifact(ctx context.Context, req *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	done, err := s.maintenance.enter(false)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxletServer.ReadSessionArtifact(ctx, req)
}
func (s *maintenanceTaskServer) SubmitSubtask(ctx context.Context, req *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxTaskServer.SubmitSubtask(ctx, req)
}
func (s *maintenanceTaskServer) SendAgentMessage(ctx context.Context, req *pb.SendAgentMessageRequest) (*pb.SendAgentMessageResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxTaskServer.SendAgentMessage(ctx, req)
}
func (s *maintenanceTaskServer) PruneSubtasks(ctx context.Context, req *pb.PruneSubtasksRequest) (*pb.PruneSubtasksResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxTaskServer.PruneSubtasks(ctx, req)
}
func (s *maintenanceTaskServer) CleanupAgentInstance(ctx context.Context, req *pb.CleanupAgentInstanceRequest) (*pb.CleanupAgentInstanceResponse, error) {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.AmuxTaskServer.CleanupAgentInstance(ctx, req)
}
