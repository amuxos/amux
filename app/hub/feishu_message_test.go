package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeFeishuMessageSender struct {
	bound                bool
	lookupErr, errorSend error
	sends                int
	message              feishumsg.Message
}

func (f *fakeFeishuMessageSender) ProfileName() string { return "bound-bot" }
func (f *fakeFeishuMessageSender) HasAgentMessageBinding(string, string) (bool, error) {
	return f.bound, f.lookupErr
}
func (f *fakeFeishuMessageSender) SendAgentMessage(_ context.Context, target, name string, msg feishumsg.Message) (feishumsg.Result, error) {
	f.sends++
	f.message = msg
	if target != "target" || name != "conversation" {
		return feishumsg.Result{}, errors.New("wrong session")
	}
	return feishumsg.Result{MessageID: "om-sent", ChatID: "oc-a"}, f.errorSend
}

func TestSendFeishuMessageAuthorizationAndRouting(t *testing.T) {
	store := agentTokenTestStore(t)
	p, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "caller"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.IssueAgentCredential(p.ID, "relaunch:target:conversation", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", credential.Token))
	sender := &fakeFeishuMessageSender{bound: true}
	s := &hubServer{principals: store, feishuMessageSenders: []feishuMessageSender{&fakeFeishuMessageSender{}, sender}, listTargets: func() []*pb.Target {
		return []*pb.Target{{Id: "target", Sessions: []*pb.Session{{Id: "session", Name: "conversation", LaunchId: "new-launch"}, {Id: "foreign", Name: "other"}}}}
	}}
	req := &pb.SendFeishuMessageRequest{SessionId: "session", ReceiveId: "oc-a", ReceiveIdType: "chat_id", Text: "**hello**", Format: "markdown", Uuid: "key"}
	resp, err := s.SendFeishuMessage(ctx, req)
	if err != nil || resp.GetMessageId() != "om-sent" || resp.GetBotProfile() != "bound-bot" || sender.sends != 1 || sender.message.UUID != "key" || sender.message.Format != "markdown" || sender.message.Text != "**hello**" {
		t.Fatalf("send: %+v %v, calls=%d", resp, err, sender.sends)
	}
	req.Format = "html"
	if _, err := s.SendFeishuMessage(ctx, req); status.Code(err) != codes.InvalidArgument || sender.sends != 1 {
		t.Fatalf("invalid format reached sender: %v", err)
	}
	req.Format = "markdown"
	for _, tc := range []struct {
		name, session string
		ctx           context.Context
		code          codes.Code
	}{
		{"no credential", "session", context.Background(), codes.PermissionDenied},
		{"bad credential", "session", metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", "invalid")), codes.Unauthenticated},
		{"foreign session same principal", "foreign", ctx, codes.PermissionDenied},
		{"missing session", "missing", ctx, codes.PermissionDenied},
		{"empty session", "", ctx, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req.SessionId = tc.session
			if _, err := s.SendFeishuMessage(tc.ctx, req); status.Code(err) != tc.code {
				t.Fatalf("error = %v", err)
			}
			if sender.sends != 1 {
				t.Fatal("unauthorized send")
			}
		})
	}
	req.SessionId = "session"
	for _, tc := range []struct {
		name    string
		senders []feishuMessageSender
		code    codes.Code
	}{
		{"unbound", []feishuMessageSender{&fakeFeishuMessageSender{}}, codes.FailedPrecondition},
		{"ambiguous", []feishuMessageSender{sender, &fakeFeishuMessageSender{bound: true}}, codes.FailedPrecondition},
		{"lookup failure", []feishuMessageSender{sender, &fakeFeishuMessageSender{lookupErr: errors.New("db failure")}}, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.feishuMessageSenders = tc.senders
			if _, err := s.SendFeishuMessage(ctx, req); status.Code(err) != tc.code {
				t.Fatalf("error = %v", err)
			}
			if sender.sends != 1 {
				t.Fatal("sent before unique binding resolved")
			}
		})
	}
	s.feishuMessageSenders = []feishuMessageSender{sender}
	sender.errorSend = errors.New("permission denied by platform")
	if _, err := s.SendFeishuMessage(ctx, req); status.Code(err) != codes.Unavailable {
		t.Fatalf("delivery failure = %v", err)
	}
	if err := store.Disable(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendFeishuMessage(ctx, req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled credential = %v", err)
	}
	if sender.sends != 2 {
		t.Fatal("disabled principal sent")
	}
}
