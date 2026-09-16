package isolation

import (
	"bytes"
	"context"
	"fmt"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessionfiles"
)

func (r *Runtime) SessionFile(ctx context.Context, identity principal.ExecutionIdentity, workspace, action, name string, data []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sessionfiles.Validate(action, name); err != nil {
		return nil, err
	}
	if len(data) > sessionfiles.MaxBytes {
		return nil, fmt.Errorf("file too large")
	}
	if identity.IsolationBackend == principal.BackendLinuxDAC {
		if r == nil || r.store == nil || r.client == nil {
			return nil, fmt.Errorf("linux-dac file runtime unavailable")
		}
		mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
		if err != nil {
			return nil, err
		}
		return r.client.SessionFile(ctx, mapping, ResolveExecutionWorkspaceID(workspace, identity), action, name, data)
	}
	var out bytes.Buffer
	err := sessionfiles.Exchange(workspace, action, name, bytes.NewReader(data), &out)
	return out.Bytes(), err
}
