package runtime

import (
	"testing"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

func TestPersonalFeishuSessionRequiresOwnIsolation(t *testing.T) {
	req := feishumsg.PersonalRequest{SessionID: "session", PrincipalID: "owner"}
	base := session.Snapshot{ID: "session", State: session.StateRunning, ExecutionIdentity: principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceFeishu, IsolationBackend: principal.BackendLinuxDAC,
		OriginActorPrincipalID: "owner", EffectivePrincipalID: "owner",
	}}
	if !personalFeishuSessionMatches(base, req) {
		t.Fatal("own isolated Feishu Session rejected")
	}
	for name, change := range map[string]func(*session.Snapshot){
		"different session":   func(s *session.Snapshot) { s.ID = "other" },
		"archived":            func(s *session.Snapshot) { s.State = session.StateArchived },
		"delegated origin":    func(s *session.Snapshot) { s.ExecutionIdentity.OriginActorPrincipalID = "other" },
		"different effective": func(s *session.Snapshot) { s.ExecutionIdentity.EffectivePrincipalID = "other" },
		"host session":        func(s *session.Snapshot) { s.ExecutionIdentity.IsolationBackend = principal.BackendPassthrough },
		"CLI session":         func(s *session.Snapshot) { s.ExecutionIdentity.Source = principal.SourceCLI },
	} {
		t.Run(name, func(t *testing.T) {
			snap := base
			change(&snap)
			if personalFeishuSessionMatches(snap, req) {
				t.Fatal("accepted a Session outside the personal delivery boundary")
			}
		})
	}
}
