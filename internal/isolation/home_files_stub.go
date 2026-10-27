//go:build !linux

package isolation

import "context"

func (c *HelperClient) ReadHomeFiles(context.Context, AccountMapping, []string) (map[string][]byte, error) {
	return nil, helperUnsupported()
}
