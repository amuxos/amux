package setup

import (
	"testing"

	"github.com/amuxos/amux/internal/amuxhome/amuxhometest"
)

// TestMain keeps every test in this package away from the caller's real amux
// home, even when the test binary inherits AMUX_HOME.
func TestMain(m *testing.M) { amuxhometest.Main(m) }
