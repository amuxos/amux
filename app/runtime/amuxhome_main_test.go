package runtime

import (
	"os"
	"testing"

	"github.com/amuxos/amux/internal/amuxhome/amuxhometest"
)

// TestMain intercepts the test binary when it is re-exec'd as a fake broker
// (argv[1] is a broker subcommand) and blocks forever, so the reaper sees a
// real live process in /proc. Normal test runs keep every test away from the
// caller's real amux home, even when the test binary inherits AMUX_HOME.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "_structured_broker" || os.Args[1] == "_acp_broker") {
		select {} // fake broker: idle forever
	}
	amuxhometest.Main(m)
}
