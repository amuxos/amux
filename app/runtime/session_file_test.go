package runtime

import (
	"context"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestSessionFileRejectsDirectRPCBeforeLookingUpSession(t *testing.T) {
	s := &server{}
	for _, action := range []string{"put", "read"} {
		if _, err := s.sessionFile(context.Background(), &pb.SessionFileRequest{Name: "other", FileName: "secret"}, action); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: %v", action, err)
		}
	}
}
