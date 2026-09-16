package runtime

import (
	"context"
	"path/filepath"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) PutSessionFile(ctx context.Context, req *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	return s.sessionFile(ctx, req, "put")
}

func (s *server) ReadSessionArtifact(ctx context.Context, req *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	return s.sessionFile(ctx, req, "read")
}

func (s *server) sessionFile(ctx context.Context, req *pb.SessionFileRequest, action string) (*pb.SessionFileResponse, error) {
	if !tunnel.IsTrustedSubmit(ctx) {
		return nil, status.Error(codes.PermissionDenied, "file exchange requires trusted tunnel provenance")
	}
	sess, err := s.mgr.Get(req.GetName())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	snap := sess.Snapshot()
	if req.GetSessionId() == "" || req.GetSessionId() != snap.ID || req.GetLaunchId() != snap.LaunchID {
		return nil, status.Error(codes.FailedPrecondition, "session execution changed")
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	if action == "read" && len(req.Data) != 0 {
		return nil, status.Error(codes.InvalidArgument, "read cannot include data")
	}
	data, err := s.isolationRuntime.SessionFile(ctx, snap.ExecutionIdentity, snap.Cwd, action, req.FileName, req.Data)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if action == "put" {
		return &pb.SessionFileResponse{Path: filepath.Join(snap.Cwd, string(data))}, nil
	}
	return &pb.SessionFileResponse{Data: data}, nil
}
