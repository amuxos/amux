package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/memory"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *hubServer) memoryEnabled() bool {
	if s.memoryConfig != nil {
		return s.memoryConfig().Enabled
	}
	return config.GetMemory().Enabled
}

// memoryScope derives authorization exclusively from the Hub policy and actor.
// A binding is only an equality precondition: it cannot select a principal.
// Unlike session visibility, helper readiness/online targets never weaken it.
func (s *hubServer) memoryScope(ctx context.Context) (memory.Scope, string, *principal.ActorContext, error) {
	if !s.memoryEnabled() {
		return memory.Scope{}, "", nil, status.Error(codes.FailedPrecondition, "memory is disabled by memory.enabled=false")
	}
	if s.memory == nil {
		return memory.Scope{}, "", nil, status.Error(codes.FailedPrecondition, "Hub memory store is unavailable")
	}
	if s.principals == nil {
		md, _ := metadata.FromIncomingContext(ctx)
		for _, token := range md.Get("x-amux-actor-token") {
			if strings.TrimSpace(token) != "" {
				return memory.Scope{}, "", nil, status.Error(codes.Unauthenticated, "actor registry is unavailable")
			}
		}
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return memory.Scope{}, "", nil, err
	}
	getIsolation := s.isolationConfig
	if getIsolation == nil {
		getIsolation = config.GetIsolation
	}
	isolation, err := getIsolation()
	if err != nil {
		return memory.Scope{}, "", nil, status.Error(codes.FailedPrecondition, "cannot determine memory isolation policy")
	}
	if !isolation.Enabled {
		return memory.Scope{Global: true}, "global", actor, nil
	}
	if actor == nil || actor.ActorPrincipalID == "" {
		return memory.Scope{}, "", nil, status.Error(codes.Unauthenticated, "isolated memory requires CLI enrollment or an agent actor credential")
	}
	return memory.Scope{PrincipalID: actor.ActorPrincipalID}, "principal:" + actor.ActorPrincipalID, actor, nil
}

func (s *hubServer) authorizeMemory(ctx context.Context, binding string) (memory.Scope, error) {
	scope, current, actor, err := s.memoryScope(ctx)
	if err != nil {
		return memory.Scope{}, err
	}
	if actor != nil && actor.Delegated && binding == "" {
		return memory.Scope{}, status.Error(codes.FailedPrecondition, "agent memory requires a managed conversation binding; start a new conversation")
	}
	if binding != "" && binding != current {
		return memory.Scope{}, status.Error(codes.FailedPrecondition, "memory scope changed; start a new conversation")
	}
	return scope, nil
}

func (s *hubServer) GetMemoryBinding(ctx context.Context, _ *pb.MemoryBindingRequest) (*pb.MemoryBindingResponse, error) {
	if !s.memoryEnabled() {
		return &pb.MemoryBindingResponse{}, nil
	}
	_, binding, _, err := s.memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.MemoryBindingResponse{Enabled: true, Binding: binding}, nil
}

func (s *hubServer) MemoryGet(ctx context.Context, req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
	scope, err := s.authorizeMemory(ctx, req.GetBinding())
	if err != nil {
		return nil, err
	}
	if req.GetOffset() < 0 || req.GetOffset() > memory.MaxDocumentBytes {
		return nil, status.Error(codes.InvalidArgument, "memory offset is outside the document limit")
	}
	doc, err := s.memory.Get(ctx, scope, req.GetPath(), memory.GetOptions{
		StartLine: int(req.GetStartLine()), EndLine: int(req.GetEndLine()), Offset: int(req.GetOffset()),
	})
	if err != nil {
		return nil, memoryStatus(err)
	}
	return &pb.MemoryDocument{
		Path: doc.Path, Content: doc.Content, Revision: doc.Revision, Exists: doc.Exists,
		StartLine: int32(doc.StartLine), EndLine: int32(doc.EndLine), Truncated: doc.Truncated,
		NextOffset: int64(doc.NextOffset),
	}, nil
}

func (s *hubServer) MemorySearch(ctx context.Context, req *pb.MemorySearchRequest) (*pb.MemorySearchResponse, error) {
	scope, err := s.authorizeMemory(ctx, req.GetBinding())
	if err != nil {
		return nil, err
	}
	result, err := s.memory.Search(ctx, scope, req.GetQuery(), memory.SearchOptions{PathPrefix: req.GetPathPrefix(), Limit: int(req.GetLimit())})
	if err != nil {
		return nil, memoryStatus(err)
	}
	response := &pb.MemorySearchResponse{Incomplete: result.Incomplete, Truncated: result.Truncated, ScannedFiles: int32(result.ScannedFiles)}
	for _, match := range result.Matches {
		response.Matches = append(response.Matches, &pb.MemoryMatch{
			Path: match.Path, Snippet: match.Snippet, Revision: match.Revision, Line: int32(match.Line), Truncated: match.Truncated,
		})
	}
	return response, nil
}

func (s *hubServer) MemoryUpsert(ctx context.Context, req *pb.MemoryUpsertRequest) (*pb.MemoryWriteResponse, error) {
	scope, err := s.authorizeMemory(ctx, req.GetBinding())
	if err != nil {
		return nil, err
	}
	result, err := s.memory.Upsert(ctx, scope, req.GetPath(), req.GetContent(), req.GetExpectedRevision())
	if err != nil {
		return nil, memoryStatus(err)
	}
	return &pb.MemoryWriteResponse{Path: result.Path, Revision: result.Revision}, nil
}

func (s *hubServer) MemoryForget(ctx context.Context, req *pb.MemoryForgetRequest) (*pb.MemoryForgetResponse, error) {
	scope, err := s.authorizeMemory(ctx, req.GetBinding())
	if err != nil {
		return nil, err
	}
	result, err := s.memory.Forget(ctx, scope, req.GetPath(), req.GetExpectedRevision())
	if err != nil {
		return nil, memoryStatus(err)
	}
	return &pb.MemoryForgetResponse{Path: result.Path, AlreadyAbsent: result.AlreadyAbsent}, nil
}

func memoryStatus(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "memory call canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "memory call timed out")
	case errors.Is(err, memory.ErrConflict):
		st := status.New(codes.Aborted, err.Error())
		var conflict *memory.ConflictError
		if errors.As(err, &conflict) {
			if detailed, detailErr := st.WithDetails(&errdetails.ErrorInfo{Reason: "REVISION_CONFLICT", Domain: "amux.memory", Metadata: map[string]string{"current_revision": conflict.CurrentRevision}}); detailErr == nil {
				st = detailed
			}
		}
		return st.Err()
	case errors.Is(err, memory.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, memory.ErrUnsafe):
		return status.Error(codes.PermissionDenied, "unsafe memory path")
	case errors.Is(err, memory.ErrLimit):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		fmt.Fprintf(os.Stderr, "amuxd: memory operation: %v\n", err)
		return status.Error(codes.Internal, "memory storage operation failed")
	}
}
