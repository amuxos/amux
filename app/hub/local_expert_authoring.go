package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/executionauth"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const localExpertPreviewReceiptTTL = 30 * time.Minute

type localExpertAuthoringControl struct {
	library  *agentprofile.ManagedLibrary
	receipts *agentprofile.ManagedAuthoringReceiptSigner
}

func (c *localExpertAuthoringControl) runEvaluationReconciler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_ = c.library.ReconcileAuthoringEvaluations(now)
		}
	}
}

func newLocalExpertAuthoringControl(library *agentprofile.ManagedLibrary) (*localExpertAuthoringControl, error) {
	if err := library.ReconcileAuthoringEvaluations(time.Now()); err != nil {
		return nil, fmt.Errorf("reconcile local expert evaluations: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate local expert preview receipt key: %w", err)
	}
	receipts, err := agentprofile.NewManagedAuthoringReceiptSigner(key, localExpertPreviewReceiptTTL, time.Now)
	if err != nil {
		return nil, err
	}
	return &localExpertAuthoringControl{library: library, receipts: receipts}, nil
}

func (s *hubServer) ReadLocalExpert(ctx context.Context, req *pb.ReadLocalExpertRequest) (*pb.ReadLocalExpertResponse, error) {
	turnID, err := s.authorizeLocalExpertTurn(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	_ = turnID
	control, err := s.requireLocalExpertAuthoringControl()
	if err != nil {
		return nil, err
	}
	base, err := control.library.ReadAuthoringBase(ctx, managedAuthoringRequestFromRead(req))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out := &pb.ReadLocalExpertResponse{
		Operation: string(base.Operation), TargetRef: base.TargetRef, SourceRef: base.SourceRef, BaseRevision: base.BaseRevision,
		Files: make([]*pb.LocalExpertAuthoringFile, 0, len(base.Files)),
	}
	for _, file := range base.Files {
		out.Files = append(out.Files, &pb.LocalExpertAuthoringFile{Path: file.Path, Content: file.Content, Binary: file.Binary})
	}
	return out, nil
}

func (s *hubServer) ChangeLocalExpert(ctx context.Context, req *pb.ChangeLocalExpertRequest) (*pb.ChangeLocalExpertResponse, error) {
	turnID, err := s.authorizeLocalExpertTurn(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	control, err := s.requireLocalExpertAuthoringControl()
	if err != nil {
		return nil, err
	}
	request := managedAuthoringRequestFromChange(req)
	switch strings.TrimSpace(req.GetMode()) {
	case "preview":
		var result agentprofile.ManagedAuthoringResult
		var evaluation agentprofile.ManagedAuthoringEvaluation
		if req.GetPrepareEvaluation() {
			result, evaluation, err = control.library.PrepareAuthoringEvaluation(ctx, request)
		} else {
			result, err = control.library.PreviewAuthoring(ctx, request)
		}
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		receipt, err := control.receipts.Issue(agentprofile.ManagedAuthoringReceiptClaims{
			SessionID: req.GetSessionId(), PreviewTurnID: turnID, Operation: request.Operation,
			TargetRef: request.TargetRef, SourceRef: request.SourceRef, BaseRevision: request.BaseRevision,
			PatchDigest: result.PatchDigest, Tree: result.Tree, EvaluationProfileRef: evaluation.ProfileRef,
		})
		if err != nil {
			if evaluation.ProfileRef != "" {
				_ = control.library.CleanupAuthoringEvaluation(evaluation.ProfileRef)
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		response := &pb.ChangeLocalExpertResponse{
			Mode: "preview", BaseRevision: result.BaseRevision, Tree: result.Tree,
			PatchDigest: result.PatchDigest, Diff: result.Diff, PreviewReceipt: receipt,
			EvaluationProfileRef: evaluation.ProfileRef,
		}
		if !evaluation.ExpiresAt.IsZero() {
			response.EvaluationExpiresAt = evaluation.ExpiresAt.Unix()
		}
		return response, nil
	case "cleanup":
		claims, err := control.receipts.Verify(req.GetPreviewReceipt())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		if claims.SessionID != strings.TrimSpace(req.GetSessionId()) || claims.EvaluationProfileRef == "" {
			return nil, status.Error(codes.FailedPrecondition, "cleanup request does not match an evaluation preview")
		}
		if err := control.library.CleanupAuthoringEvaluation(claims.EvaluationProfileRef); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return &pb.ChangeLocalExpertResponse{Mode: "cleanup", EvaluationProfileRef: claims.EvaluationProfileRef}, nil
	case "publish":
		claims, err := control.receipts.Verify(req.GetPreviewReceipt())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		if err := validateLocalExpertPublishClaims(claims, req, turnID); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		result, err := control.library.PublishAuthoring(ctx, request, claims.Tree)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		if claims.EvaluationProfileRef != "" {
			_ = control.library.CleanupAuthoringEvaluation(claims.EvaluationProfileRef)
		}
		return &pb.ChangeLocalExpertResponse{
			Mode: "publish", BaseRevision: result.BaseRevision, Tree: result.Tree,
			PatchDigest: result.PatchDigest, Diff: result.Diff, Published: true,
			ProfileRef: result.Mutation.ProfileRef, Revision: result.Mutation.Revision,
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "mode must be preview, publish, or cleanup")
	}
}

func (s *hubServer) requireLocalExpertAuthoringControl() (*localExpertAuthoringControl, error) {
	if s == nil || s.localExpertAuthoring == nil || s.localExpertAuthoring.library == nil || s.localExpertAuthoring.receipts == nil {
		return nil, status.Error(codes.FailedPrecondition, "local expert authoring is unavailable")
	}
	return s.localExpertAuthoring, nil
}

func (s *hubServer) authorizeLocalExpertTurn(ctx context.Context, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", status.Error(codes.Unauthenticated, "local expert authoring requires an active Feishu owner session")
	}
	targets := s.currentTargets()
	targetID, session := sessionByID(targets, sessionID)
	if session == nil {
		return "", status.Error(codes.PermissionDenied, "local expert authoring session is not available")
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return "", err
	}
	if actor == nil {
		return "", status.Error(codes.PermissionDenied, "local expert authoring requires the current session agent credential")
	}
	if !actor.Delegated {
		return "", status.Error(codes.PermissionDenied, "local expert authoring is only available inside a Feishu Bot owner turn")
	}
	decision := executionauth.New(nil).CallerSessionAccess(
		actor,
		executionauth.SessionFacts{
			ID: session.Id, TargetID: targetID, Name: session.Name, LaunchID: session.LaunchId,
		},
		sessionFactsFromTargets(targets),
	)
	if !decision.Allowed {
		return "", status.Error(codes.PermissionDenied, "local expert authoring credential does not belong to this session")
	}
	var turn *pb.LocalExpertAuthoringTurnResponse
	if s.getLocalExpertAuthoringTurn != nil {
		turn, err = s.getLocalExpertAuthoringTurn(ctx, targetID, session.Name)
	} else {
		turn, err = routeUnary[pb.LocalExpertAuthoringTurnRequest, pb.LocalExpertAuthoringTurnResponse](s, ctx, targetID, "GetLocalExpertAuthoringTurn", &pb.LocalExpertAuthoringTurnRequest{Name: session.Name})
	}
	if err != nil {
		return "", err
	}
	if !turn.GetActive() || !turn.GetAllowed() || strings.TrimSpace(turn.GetTurnId()) == "" {
		return "", status.Error(codes.PermissionDenied, "local expert authoring is only available during the current Feishu Bot owner turn")
	}
	return strings.TrimSpace(turn.GetTurnId()), nil
}

func sessionByID(targets []*pb.Target, sessionID string) (string, *pb.Session) {
	for _, target := range targets {
		if target == nil {
			continue
		}
		for _, session := range target.Sessions {
			if session != nil && session.Id == sessionID {
				return target.Id, session
			}
		}
	}
	return "", nil
}

func managedAuthoringRequestFromRead(req *pb.ReadLocalExpertRequest) agentprofile.ManagedAuthoringRequest {
	return agentprofile.ManagedAuthoringRequest{
		Operation: agentprofile.ManagedAuthoringOperation(req.GetOperation()), TargetRef: req.GetTargetRef(), SourceRef: req.GetSourceRef(),
	}
}

func managedAuthoringRequestFromChange(req *pb.ChangeLocalExpertRequest) agentprofile.ManagedAuthoringRequest {
	return agentprofile.ManagedAuthoringRequest{
		Operation: agentprofile.ManagedAuthoringOperation(req.GetOperation()), TargetRef: req.GetTargetRef(),
		SourceRef: req.GetSourceRef(), BaseRevision: req.GetBaseRevision(), Patch: req.GetPatch(),
	}
}

func validateLocalExpertPublishClaims(claims agentprofile.ManagedAuthoringReceiptClaims, req *pb.ChangeLocalExpertRequest, currentTurnID string) error {
	requestSessionID := strings.TrimSpace(req.GetSessionId())
	if claims.SessionID != requestSessionID {
		return fmt.Errorf("publish session_id does not match the confirmed preview")
	}
	if claims.PreviewTurnID == strings.TrimSpace(currentTurnID) {
		return fmt.Errorf("publish requires a later owner turn than the confirmed preview")
	}
	requestOperation := agentprofile.ManagedAuthoringOperation(strings.TrimSpace(req.GetOperation()))
	if claims.Operation != requestOperation {
		return fmt.Errorf("publish operation does not match the confirmed preview: expected %q, got %q", claims.Operation, requestOperation)
	}
	requestTargetRef := strings.TrimSpace(req.GetTargetRef())
	if claims.TargetRef != requestTargetRef {
		return fmt.Errorf("publish target_ref does not match the confirmed preview: expected %q, got %q", claims.TargetRef, requestTargetRef)
	}
	requestSourceRef := strings.TrimSpace(req.GetSourceRef())
	if claims.SourceRef != requestSourceRef {
		return fmt.Errorf("publish source_ref does not match the confirmed preview: expected %q, got %q", claims.SourceRef, requestSourceRef)
	}
	requestBaseRevision := strings.TrimSpace(req.GetBaseRevision())
	if claims.BaseRevision != requestBaseRevision {
		return fmt.Errorf("publish base_revision does not match the confirmed preview: expected %q, got %q", claims.BaseRevision, requestBaseRevision)
	}
	digest := sha256.Sum256([]byte(req.GetPatch()))
	if requestPatchDigest := hex.EncodeToString(digest[:]); claims.PatchDigest != requestPatchDigest {
		return fmt.Errorf("publish patch does not match the confirmed preview")
	}
	return nil
}
