package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type targetRef struct {
	ID           string
	Name         string
	Version      string
	Capabilities *registry.Capabilities
	Backend      sessionClient
}

type resolvedSession struct {
	Target  targetRef
	Session *sessionEntry
}

type targetSessions struct {
	target   targetRef
	sessions []*sessionEntry
}

type sessionResolver struct {
	listTargets func(context.Context) ([]targetSessions, error)
	clients     []sessionClient
}

func newSessionResolver() (*sessionResolver, error) {
	_ = loadUserConfig()

	hub, err := newHubClient(hubAddr())
	if err != nil {
		return nil, err
	}
	return &sessionResolver{
		listTargets: hub.ListTargets,
		clients:     []sessionClient{hub},
	}, nil
}

func (r *sessionResolver) Close() error {
	var firstErr error
	seen := map[sessionClient]struct{}{}
	for _, c := range r.clients {
		if c == nil {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *sessionResolver) ResolveSession(ctx context.Context, ref string) (*resolvedSession, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("session ref is required")
	}
	targets, err := r.listTargets(ctx)
	if err != nil {
		return nil, err
	}

	if targetName, sessionName, ok := splitTargetSession(ref); ok {
		return resolveTargetSession(targets, targetName, sessionName)
	}

	if matches := matchingSessions(targets, func(s *sessionEntry) bool {
		return s.Id == ref
	}); len(matches) > 0 {
		if len(matches) == 1 {
			return matches[0], nil
		}
		return nil, ambiguousSessionError(ref, matches)
	}

	matches := matchingSessions(targets, func(s *sessionEntry) bool {
		return s.Name == ref
	})
	if len(matches) == 0 {
		return nil, fmt.Errorf("session %q not found; run `amux session list` to see available sessions", ref)
	}
	if len(matches) > 1 {
		return nil, ambiguousSessionError(ref, matches)
	}
	return matches[0], nil
}

func (r *sessionResolver) ResolveTarget(ctx context.Context, ref string) (targetRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "local"
	}
	targets, err := r.listTargets(ctx)
	if err != nil {
		return targetRef{}, err
	}
	var matches []targetRef
	for _, ts := range targets {
		if targetMatchesRef(ts.target, ref) {
			matches = append(matches, ts.target)
		}
	}
	if len(matches) == 0 {
		return targetRef{}, fmt.Errorf("target %q not found; run `amux system target list` to see available targets", ref)
	}
	if len(matches) > 1 {
		return targetRef{}, fmt.Errorf("target %q is ambiguous", ref)
	}
	return matches[0], nil
}

func splitTargetSession(ref string) (target, session string, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	target = strings.TrimSpace(parts[0])
	session = strings.TrimSpace(parts[1])
	return target, session, target != "" && session != ""
}

func resolveTargetSession(targets []targetSessions, targetName, sessionName string) (*resolvedSession, error) {
	var targetMatches []targetSessions
	for _, ts := range targets {
		if targetMatchesRef(ts.target, targetName) {
			targetMatches = append(targetMatches, ts)
		}
	}
	if len(targetMatches) == 0 {
		return nil, fmt.Errorf("target %q not found; run `amux system target list` to see available targets", targetName)
	}
	matches := matchingSessions(targetMatches, func(s *sessionEntry) bool {
		return s.Name == sessionName || s.Id == sessionName
	})
	if len(matches) == 0 {
		return nil, fmt.Errorf("session %q not found on target %q", sessionName, targetName)
	}
	if len(matches) > 1 {
		return nil, ambiguousSessionError(targetName+"/"+sessionName, matches)
	}
	return matches[0], nil
}

func matchingSessions(targets []targetSessions, match func(*sessionEntry) bool) []*resolvedSession {
	var out []*resolvedSession
	for _, ts := range targets {
		for _, sess := range ts.sessions {
			if match(sess) {
				out = append(out, &resolvedSession{Target: ts.target, Session: sess})
			}
		}
	}
	return out
}

func ambiguousSessionError(ref string, matches []*resolvedSession) error {
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, sessionDisplayRef(m.Target, m.Session))
	}
	sort.Strings(refs)
	return fmt.Errorf("session %q is ambiguous:\n\n  %s\n\nUse:\n  amux session attach %s",
		ref, strings.Join(refs, "\n  "), strings.Join(refs, "\n  amux session attach "))
}

func sessionDisplayRef(target targetRef, sess *sessionEntry) string {
	name := target.Name
	if name == "" {
		name = target.ID
	}
	if name == "" {
		name = "local"
	}
	return name + "/" + sess.Name
}

func targetMatchesRuntime(target targetRef, runtimeCfg config.RuntimeConfig) bool {
	for _, targetValue := range []string{target.ID, target.Name} {
		if targetValue == "" {
			continue
		}
		if runtimeCfg.ID != "" && targetValue == runtimeCfg.ID {
			return true
		}
		if runtimeCfg.Name != "" && targetValue == runtimeCfg.Name {
			return true
		}
	}
	return false
}

func targetMatchesRef(target targetRef, ref string) bool {
	if target.Name == ref || target.ID == ref {
		return true
	}
	return ref == "local" && targetMatchesRuntime(target, config.GetRuntime())
}

func targetsToTargetSessions(backend sessionClient, targets []*pb.Target) []targetSessions {
	out := make([]targetSessions, 0, len(targets))
	for _, sb := range targets {
		id := sb.Id
		name := sb.Name
		if name == "" {
			name = id
		}
		ts := targetSessions{
			target: targetRef{ID: id, Name: name, Version: sb.Version, Capabilities: targetCapabilitiesToRegistry(sb.Capabilities), Backend: backend},
		}
		for _, s := range sb.Sessions {
			ts.sessions = append(ts.sessions, &sessionEntry{
				Session:    *s,
				TargetID:   sb.Id,
				TargetName: sb.Name,
			})
		}
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].target.Name == out[j].target.Name {
			return out[i].target.ID < out[j].target.ID
		}
		return out[i].target.Name < out[j].target.Name
	})
	return out
}

func loadUserConfig() error {
	return config.Load(filepath.Join(defaultAmuxDir(), "config.toml"))
}

func configuredHubAddr() string {
	return strings.TrimSpace(config.GetHub().Addr)
}

func hubAddr() string {
	if addr := configuredHubAddr(); addr != "" {
		return addr
	}
	if addr := strings.TrimSpace(os.Getenv("AMUX_HUB_ADDR")); addr != "" {
		return addr
	}
	return defaultLocalHubAddr
}

func hubToken() string {
	if token := strings.TrimSpace(config.GetHub().Token); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("AMUX_HUB_TOKEN"))
}

func actorToken() string {
	// The session-scoped credential file wins: amuxlet keeps it current, while
	// an AMUX_ACTOR_TOKEN value baked into this process's environment goes stale
	// the moment the hub re-mints the credential (restart revokes the old one).
	// It is re-read on every call, and every hub dial resolves its token here,
	// so an in-session caller picks up the adopted credential without a restart.
	if token := actorTokenFromFile(strings.TrimSpace(os.Getenv("AMUX_ACTOR_TOKEN_FILE"))); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("AMUX_ACTOR_TOKEN")); token != "" {
		return token
	}
	data, err := os.ReadFile(filepath.Join(defaultAmuxDir(), "secrets", "principal-enrollment.token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// actorTokenFromFile reads the credential amuxlet persists next to the session.
// Both the JSON form ({"token": ..., "expires_at": ...}) and a bare token line
// are accepted. A missing, unreadable or empty file yields "" so the caller
// falls back to AMUX_ACTOR_TOKEN and then the enrollment token.
func actorTokenFromFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var stored struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &stored); err == nil && strings.TrimSpace(stored.Token) != "" {
		return strings.TrimSpace(stored.Token)
	}
	return strings.TrimSpace(string(data))
}
