//go:build !linux

package isolation

import (
	"context"
	"fmt"
	"os"
)

type HelperClient struct {
	SocketPath string
}

func NewHelperClient(socketPath string) *HelperClient {
	return &HelperClient{SocketPath: socketPath}
}

func helperUnsupported() error { return fmt.Errorf("linux-dac isolation is only supported on Linux") }

func (c *HelperClient) Handshake(context.Context) (HelperResponse, error) {
	return HelperResponse{}, helperUnsupported()
}
func (c *HelperClient) EnsurePrincipal(context.Context, AccountMapping) (PrincipalPaths, error) {
	return PrincipalPaths{}, helperUnsupported()
}
func (c *HelperClient) PrepareRun(context.Context, AccountMapping, string, string) (PrincipalPaths, error) {
	return PrincipalPaths{}, helperUnsupported()
}

func (c *HelperClient) MaterializeRun(context.Context, AccountMapping, string, string, []BundleSource) error {
	return helperUnsupported()
}
func (c *HelperClient) Start(context.Context, AccountMapping, LaunchSpec) (ProcessHandle, error) {
	return nil, helperUnsupported()
}
func (c *HelperClient) Describe(context.Context, string) (LaunchDescription, error) {
	return LaunchDescription{}, helperUnsupported()
}
func (c *HelperClient) Wait(context.Context, string) (ExitStatus, error) {
	return ExitStatus{}, helperUnsupported()
}
func (c *HelperClient) Signal(context.Context, string, string) error { return helperUnsupported() }
func (c *HelperClient) Kill(context.Context, string) error           { return helperUnsupported() }
func (c *HelperClient) CloseHandle(context.Context, string) error    { return helperUnsupported() }
func (c *HelperClient) RemoveRun(context.Context, AccountMapping, string) error {
	return helperUnsupported()
}
func (c *HelperClient) RemoveWorkspace(context.Context, AccountMapping, string) error {
	return helperUnsupported()
}
func (c *HelperClient) ReclaimHome(context.Context, AccountMapping, []HomeCacheSpec) error {
	return helperUnsupported()
}
func (c *HelperClient) KillPrincipal(context.Context, AccountMapping) error {
	return helperUnsupported()
}
func (c *HelperClient) Drain(context.Context, bool) error { return helperUnsupported() }
func (c *HelperClient) DrainStatus(context.Context, bool) (int, error) {
	return 0, helperUnsupported()
}
func (c *HelperClient) AcquireUpgradeGuard(context.Context, *os.File) (int, error) {
	return 0, helperUnsupported()
}
func (c *HelperClient) ListAccounts(context.Context) ([]AccountMapping, error) {
	return nil, helperUnsupported()
}
func (c *HelperClient) RemoveAccount(context.Context, AccountMapping) error {
	return helperUnsupported()
}
