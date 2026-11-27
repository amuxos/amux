//go:build !linux

package isolationhelper

import (
	"context"
	"fmt"
	"os"

	"github.com/amuxos/amux/internal/extensionstate"
)

var version = "dev"

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	fmt.Fprintln(os.Stderr, "amux-isolation-helper is only supported on Linux")
	os.Exit(1)
}

func MainWithStartup(buildVersion string, _ func(context.Context, int, int)) {
	Main(buildVersion)
}
