//go:build !linux

package isolation

import (
	"context"
	"fmt"
)

type HelperConfig struct {
	SocketPath           string
	StateDir             string
	PrincipalRoot        string
	ServiceUID           int
	ServiceGID           int
	UIDRangeStart        int
	UIDRangeEnd          int
	AllowUnprivileged    bool
	SkipAccountProvision bool
}

func ServeHelper(context.Context, HelperConfig) error {
	return fmt.Errorf("linux-dac isolation helper is only supported on Linux")
}
