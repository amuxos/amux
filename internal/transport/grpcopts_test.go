package transport

import (
	"testing"

	"google.golang.org/grpc"
)

func TestGRPCClientDialOptionsRaiseMessageSizeAboveTunnelPayload(t *testing.T) {
	const observedTunnelPayload = 5_908_644
	if GRPCMaxMessageSize <= observedTunnelPayload {
		t.Fatalf("GRPCMaxMessageSize = %d, want > observed tunnel payload %d", GRPCMaxMessageSize, observedTunnelPayload)
	}

	opts := GRPCCallOptions()
	var recv, send int
	for _, opt := range opts {
		switch typed := opt.(type) {
		case grpc.MaxRecvMsgSizeCallOption:
			recv = typed.MaxRecvMsgSize
		case grpc.MaxSendMsgSizeCallOption:
			send = typed.MaxSendMsgSize
		}
	}
	if recv != GRPCMaxMessageSize {
		t.Fatalf("client recv max = %d, want %d", recv, GRPCMaxMessageSize)
	}
	if send != GRPCMaxMessageSize {
		t.Fatalf("client send max = %d, want %d", send, GRPCMaxMessageSize)
	}

	if len(GRPCClientDialOptions()) == 0 {
		t.Fatalf("GRPCClientDialOptions returned no options")
	}
}

func TestGRPCServerOptionsRaiseMessageSize(t *testing.T) {
	opts := GRPCServerOptions()
	if len(opts) == 0 {
		t.Fatalf("GRPCServerOptions returned no options")
	}
}
