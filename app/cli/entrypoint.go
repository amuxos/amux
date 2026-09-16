//go:build !cli_doc && !mcp_doc

package cli

import (
	"os"

	"github.com/amuxos/amux/internal/extensionstate"
)

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	os.Exit(run(os.Args[1:], os.Stderr))
}
