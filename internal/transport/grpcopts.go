package transport

import "google.golang.org/grpc"

// GRPCMaxMessageSize is the bounded per-message cap used by amux control-plane
// transports. Tunnel payloads can include replay/session/task data that exceeds
// gRPC's 4 MiB default, so all amux gRPC endpoints opt into this shared limit.
const GRPCMaxMessageSize = 64 * 1024 * 1024

func GRPCCallOptions() []grpc.CallOption {
	return []grpc.CallOption{
		grpc.MaxCallRecvMsgSize(GRPCMaxMessageSize),
		grpc.MaxCallSendMsgSize(GRPCMaxMessageSize),
	}
}

func GRPCClientDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDefaultCallOptions(GRPCCallOptions()...),
	}
}

func GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(GRPCMaxMessageSize),
		grpc.MaxSendMsgSize(GRPCMaxMessageSize),
	}
}
