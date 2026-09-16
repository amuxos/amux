package coordination

import "context"

// CatalogScope carries caller provenance that can restrict which collaborators
// are visible and resolvable. InitiatorRef is supplied by the trusted MCP
// process, not by the model.
type CatalogScope struct {
	InitiatorRef string
}

// Catalog discovers collaborators and resolves opaque public IDs into one
// adapter-private route. Implementations own their discovery truth.
type Catalog interface {
	List(context.Context, CatalogScope) ([]CollaboratorDescriptor, error)
	ResolveTarget(context.Context, string, CatalogScope) (ResolvedCollaborator, error)
}

// Backend owns the lifecycle truth for one BackendKind. Coordination only
// routes calls and projects the returned snapshot.
type Backend interface {
	Create(context.Context, *Route) (string, error)
	Get(context.Context, *Route) (*Snapshot, error)
	Wait(context.Context, *Route, uint64) (*Snapshot, error)
	RespondPermission(context.Context, *Route, PermissionResponse) error
	Cancel(context.Context, *Route) error
}

// PreboundBackend can derive a stable backend reference before it performs
// external side effects. The coordinator persists that reference first, then
// calls Create. Create must therefore be idempotent for a pre-bound route: a
// retry after process failure must observe or resume the same backend object,
// never emit a duplicate side effect.
type PreboundBackend interface {
	Backend
	PrebindRef(*Route) (string, error)
}

// Adapter is one real collaborator family: discovery plus lifecycle owner.
type Adapter interface {
	Catalog
	Backend
	BackendKind() string
}
