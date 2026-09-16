package extensions

import "github.com/amuxos/amux/internal/distribution"

type DistributionOptions = distribution.Options

// RegisterDistribution installs defaults before any application starts.
func RegisterDistribution(opts DistributionOptions) error { return distribution.Register(opts) }
