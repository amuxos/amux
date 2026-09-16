package runtime

import "context"

type providerEndpointSelector struct{ extension EndpointSelector }

func newProviderEndpointSelector() *providerEndpointSelector {
	selector := &providerEndpointSelector{}
	if create := distributionHooks.NewEndpointSelector; create != nil {
		selector.extension = create()
	}
	return selector
}

func (s *server) resolveProviderEndpoint(ctx context.Context, spec, providerID string, launchArgs []string) ([]string, string, error) {
	selector := s.providerEndpointSelector
	if selector == nil {
		selector = newProviderEndpointSelector()
	}
	if selector.extension != nil {
		return selector.extension.Resolve(ctx, spec, providerID, launchArgs)
	}
	return launchArgs, "", nil
}

func logSelectedProviderEndpoint(sessionID, providerID, endpoint string) {
	if log := distributionHooks.LogSelectedEndpoint; log != nil {
		log(sessionID, providerID, endpoint)
	}
}
