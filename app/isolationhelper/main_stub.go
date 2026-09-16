//go:build !linux

package isolationhelper

import (
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
