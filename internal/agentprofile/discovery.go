package agentprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// IsDevelopment identifies profiles intended for explicit development or
// evaluation, not default discovery or formal skill overrides. The suffix is
// retained for registries created before development metadata was recorded.
func (p Pack) IsDevelopment() bool {
	return strings.TrimSpace(p.DevelopmentOf) != "" || p.Temporary || p.Evaluation ||
		strings.HasSuffix(strings.TrimSpace(p.Namespace), "-dev")
}

// DiscoverProfiles returns usable profiles and independent Pack diagnostics.
// A non-nil error does not invalidate the returned profiles. Explicit show/run
// resolution is separate, so hidden development profiles remain addressable.
func DiscoverProfiles(registry PackRegistry, includeDevelopment bool) ([]Profile, error) {
	profiles := Builtins()
	for i := range profiles {
		profiles[i] = AttachExternalSkillOverrides(profiles[i], registry)
	}
	visible := PackRegistry{Packs: make(map[string]Pack, len(registry.Packs))}
	for namespace, pack := range registry.Packs {
		if includeDevelopment || !pack.IsDevelopment() {
			visible.Packs[namespace] = pack
		}
	}
	var diagnostics []error
	for _, result := range ListPackProfilesByPack(visible) {
		if result.Err != nil {
			diagnostics = append(diagnostics, fmt.Errorf("%s: %w", result.Namespace, result.Err))
			continue
		}
		profiles = append(profiles, result.Profiles...)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Ref() < profiles[j].Ref() })
	return profiles, errors.Join(diagnostics...)
}
