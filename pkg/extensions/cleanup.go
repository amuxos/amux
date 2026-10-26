package extensions

import "github.com/amuxos/amux/internal/cleanup"

type DailyCleanupHomePolicy = cleanup.HomePolicy

func RegisterDailyCleanupHomePolicy(policy DailyCleanupHomePolicy) error {
	return cleanup.RegisterHomePolicy(policy)
}
