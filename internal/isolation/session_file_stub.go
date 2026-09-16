//go:build !linux

package isolation

import "context"

func (c *HelperClient) SessionFile(context.Context, AccountMapping, string, string, string, []byte) ([]byte, error) {
	return nil, helperUnsupported()
}
