package hub

import (
	"fmt"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/principal"
)

// mintAgentActorToken mints the credential an identified launch carries into
// its agent process.
//
// The agent already runs as this principal; the token only lets it say so. That
// closes the gap that made an isolated agent's own `amux run` arrive
// unauthenticated — and, before isolation.ErrUnidentifiedExecution, launch a
// sibling as the service account instead of as the user who asked for it.
//
// Best-effort by design: a launch is not worth failing over a credential the
// agent may never use. When minting fails the agent simply cannot create
// further executions, which is the safe direction.
//
// Takes the store rather than the hub server because Feishu launches are minted
// from the bridge's identity resolver, which is wired before the hub server
// exists and never routes through it.
func mintAgentActorToken(
	principals *principal.Store, identity principal.ExecutionIdentity, launchID string,
) (string, int64) {
	if principals == nil || !identity.Identified() || launchID == "" {
		return "", 0
	}
	return mintAgentActorTokenForPrincipal(principals, identity.EffectivePrincipalID, launchID)
}

func mintAgentActorTokenForPrincipal(
	principals *principal.Store, principalID, launchID string,
) (string, int64) {
	if principals == nil || principalID == "" || launchID == "" {
		return "", 0
	}
	cred, err := principals.IssueAgentCredential(
		principalID, launchID, principal.AgentCredentialTTL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "amuxd: issue agent actor credential for %s: %v\n", launchID, err)
		return "", 0
	}
	return cred.Token, cred.ExpiresAt.Unix()
}

func (s *hubServer) issueAgentActorToken(identity principal.ExecutionIdentity, launchID string) (string, int64) {
	if s == nil {
		return "", 0
	}
	return mintAgentActorToken(s.principals, identity, launchID)
}

// sessionAgentCredentialSubject is stable for the lifetime of one named
// session. The process LaunchID still rotates on every relaunch; keeping that
// volatile identity out of the credential subject lets a locally recovered
// session replay its still-valid token without losing its control-plane scope.
func sessionAgentCredentialSubject(targetID, name string) string {
	targetID = strings.TrimSpace(targetID)
	name = strings.TrimSpace(name)
	if targetID == "" || name == "" {
		return ""
	}
	return "relaunch:" + targetID + ":" + name
}

// relaunchAgentActorToken mints the credential a relaunch carries. A relaunch
// otherwise replays whatever was stored at creation, so an agent whose original
// credential lapsed would stay mute about it forever; restarting is the
// documented recovery and this is what makes it one.
//
// The mint is keyed by session rather than by launch id: amuxlet rotates the
// launch id locally and the hub never learns the new one, and one live
// credential per session is the semantics we want anyway.
func (s *hubServer) relaunchAgentActorToken(targetID, name string) (string, int64) {
	if s == nil || s.principals == nil {
		return "", 0
	}
	sess := sessionByRef(s.currentTargets(), targetID, name)
	if sess == nil {
		return "", 0
	}
	relaunchID := sessionAgentCredentialSubject(targetID, sess.Name)
	identity := principal.FromProtoIdentity(sess.ExecutionIdentity)
	if identity.Identified() {
		return s.issueAgentActorToken(identity, relaunchID)
	}
	for _, launchID := range []string{sess.GetLaunchId(), relaunchID} {
		principalID, err := s.principals.AgentCredentialPrincipalForLaunch(launchID)
		if err == nil {
			return mintAgentActorTokenForPrincipal(s.principals, principalID, relaunchID)
		}
	}
	return "", 0
}
