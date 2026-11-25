package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// A managed Console is an immediate child of the installed, recorded Hub, not
// an Agent process that happens to share a host HOME containing enrollment.
// The private flag is insufficient: both process identity and ancestry must
// match. Explicit Agent/session credentials always fail closed.
var agentContextEnvKeys = []string{
	"AMUX_ACTOR_TOKEN",
	"AMUX_ACTOR_TOKEN_FILE",
	"AMUX_SESSION_ID",
	"AMUX_TASK_ID",
	"AMUX_PRINCIPAL_ID",
}

func consoleHasAgentContext() bool {
	for _, key := range agentContextEnvKeys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// stripAgentContextEnv removes per-session Agent identity markers so system
// daemons spawned from inside an Agent session never inherit session identity.
func stripAgentContextEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		strip := false
		for _, blocked := range agentContextEnvKeys {
			if strings.EqualFold(key, blocked) {
				strip = true
				break
			}
		}
		if !strip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
func consoleManagedParentVerified() bool {
	if consoleHasAgentContext() {
		return false
	}
	pid, ok := readPIDFile(filepath.Join(defaultAmuxDir(), "hub.pid"))
	if !ok || pid <= 1 || pid != os.Getppid() {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	expected := filepath.Join(filepath.Dir(self), exeName("amuxd"))
	info, err := os.Stat(expected)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "linux" {
		parent, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
		return err == nil && os.SameFile(parent, info)
	}
	actual, err := processExecutablePath(pid)
	if err != nil {
		return false
	}
	parent, err := os.Stat(actual)
	return err == nil && os.SameFile(parent, info)
}
func (b *consoleBackend) consoleOperatorCredentials(ctx context.Context) (map[string]any, error) {
	// The operator console describes host credentials, not an enrolled
	// Principal's store. Never attach a Principal token to this metadata read.
	metadata, err := listLocalCredentialMetadata()
	if err != nil {
		return nil, err
	}
	profiles := make([]map[string]any, 0, len(metadata))
	for _, p := range metadata {
		profiles = append(profiles, map[string]any{"profile": p.Profile, "kind": p.Kind, "env_names": nonNilStrings(p.EnvNames), "file_names": nonNilStrings(p.FileNames)})
	}
	return map[string]any{"profiles": profiles}, nil
}
func newConsoleOperatorHub(timeout time.Duration) (*hubClient, error) {
	host, _, err := net.SplitHostPort(hubAddr())
	if err != nil {
		return nil, consoleInvalid("operator_required", "")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, consoleInvalid("operator_required", "")
	}
	// Do not consult actorToken(): an on-disk enrollment belongs to ordinary
	// CLI callers, not the verified Hub's own operator management child.
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock()}
	opts = append(opts, transport.GRPCClientDialOptions()...)
	if token := hubToken(); token != "" {
		opts = append(opts, grpc.WithUnaryInterceptor(hubUnaryTokenInterceptor(token, "")), grpc.WithStreamInterceptor(hubStreamTokenInterceptor(token, "")))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	connection, err := grpc.DialContext(ctx, hubAddr(), opts...)
	if err != nil {
		return nil, hubUnavailableError(hubAddr(), err)
	}
	return &hubClient{c: pb.NewAmuxdClient(connection), conn: connection}, nil
}
