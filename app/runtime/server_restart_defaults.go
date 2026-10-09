package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// recordedRestartEngine never supplies a default: its caller uses this to decide
// whether the native conversation handle still has a known engine identity.
func recordedRestartEngine(snap session.Snapshot) string {
	if engine := strings.TrimSpace(snap.AgentType); engine != "" {
		return engine
	}
	var saved struct {
		AgentType   string               `json:"agent_type"`
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	if decodeLaunchSpec(snap.LaunchSpec, &saved) {
		if engine := strings.TrimSpace(saved.AgentType); engine != "" {
			return engine
		}
		if engine := strings.TrimSpace(saved.ProfileSpec.GetType()); engine != "" {
			return engine
		}
		if ref := strings.TrimSpace(saved.ProfileSpec.GetRuntimeRef()); ref != "" {
			if runtime, err := agentprofile.ResolveRuntimeOption(ref); err == nil {
				return runtime.AgentType
			}
		}
	}
	return ""
}

// recoverRestartDefaults fills absent identity only. Existing snapshots remain
// authoritative even if their source Pack is no longer installed. A non-nil
// request means new fallback content must be materialized before launching.
func (s *server) recoverRestartDefaults(snap session.Snapshot) (session.Snapshot, *pb.RunRequest, error) {
	if strings.TrimSpace(snap.AgentType) != "" && strings.TrimSpace(snap.AgentProfile) != "" {
		return snap, nil, nil
	}
	var saved struct {
		AgentProfile string               `json:"agent_profile"`
		ProfileSpec  *pb.AgentProfileSpec `json:"profile_spec"`
	}
	if strings.TrimSpace(snap.LaunchSpec) != "" {
		if err := json.Unmarshal([]byte(snap.LaunchSpec), &saved); err != nil {
			return snap, nil, err
		}
	}
	snap.AgentType = recordedRestartEngine(snap)
	if snap.AgentType == "" && strings.TrimSpace(saved.ProfileSpec.GetRuntimeRef()) != "" {
		_, err := agentprofile.ResolveRuntimeOption(saved.ProfileSpec.GetRuntimeRef())
		if err != nil {
			return snap, nil, err
		}
	}
	if strings.TrimSpace(snap.AgentType) == "" {
		if builtin, ok := agentprofile.ResolveBuiltin(agentprofile.DefaultProfileRef); ok {
			if preset, ok := agentprofile.BuiltinLaunchDefaults(builtin); ok {
				runtime, err := agentprofile.ResolveRuntimeOption(preset.Runtime)
				if err != nil {
					return snap, nil, err
				}
				snap.AgentType = runtime.AgentType
			}
		}
	}
	if strings.TrimSpace(snap.AgentProfile) == "" {
		snap.AgentProfile = saved.AgentProfile
	}
	if saved.ProfileSpec != nil {
		missingContentIdentity := strings.TrimSpace(saved.ProfileSpec.Ref) == "" &&
			(strings.TrimSpace(saved.ProfileSpec.Namespace) == "" || strings.TrimSpace(saved.ProfileSpec.Name) == "")
		useFallbackContent := false
		if missingContentIdentity {
			if strings.TrimSpace(snap.AgentProfile) == "" {
				snap.AgentProfile = saved.ProfileSpec.PinnedRef
			}
			if strings.TrimSpace(snap.AgentProfile) == "" {
				snap.AgentProfile = restartDefaultProfileRef()
				useFallbackContent = true
			}
			saved.ProfileSpec.Ref = snap.AgentProfile
		}
		if strings.TrimSpace(saved.ProfileSpec.Type) == "" {
			saved.ProfileSpec.Type = snap.AgentType
		}
		profile, err := agentprofile.FromPBProfileSpec(saved.ProfileSpec)
		if err != nil {
			return snap, nil, err
		}
		if strings.TrimSpace(snap.AgentProfile) == "" {
			snap.AgentProfile = profile.PinnedRef()
		}
		if snap.EffectiveModel == "" {
			snap.EffectiveModel = profile.ConfiguredModel
			if snap.EffectiveModel == "" {
				snap.EffectiveModel = profile.Model
			}
		}
		// Patch the original JSON instead of rebuilding a reduced snapshot, so
		// provider bindings and unknown persisted fields survive recovery.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(snap.LaunchSpec), &fields); err != nil {
			return snap, nil, err
		}
		fields["agent_type"], _ = json.Marshal(snap.AgentType)
		fields["agent_profile"], _ = json.Marshal(snap.AgentProfile)
		var profileFields map[string]json.RawMessage
		if err := json.Unmarshal(fields["profile_spec"], &profileFields); err != nil {
			return snap, nil, err
		}
		profileFields["type"], _ = json.Marshal(saved.ProfileSpec.Type)
		if missingContentIdentity {
			profileFields["ref"], _ = json.Marshal(saved.ProfileSpec.Ref)
		}
		fields["profile_spec"], _ = json.Marshal(profileFields)
		encoded, err := json.Marshal(fields)
		if err != nil {
			return snap, nil, err
		}
		snap.LaunchSpec = string(encoded)
		if useFallbackContent {
			content, err := agentprofile.ResolveProfile(filepath.Join(s.amuxDir, "agent-packs", "registry.json"), snap.AgentProfile)
			if err != nil {
				return snap, nil, err
			}
			launch, err := projectRefreshedProfileLaunch(snap, content)
			if err != nil {
				return snap, nil, err
			}
			req := refreshedRunRequest(snap, launch)
			req.ExtraArgs = append([]string(nil), snap.ExtraArgs...)
			snap.LaunchSpec = launchSpecSnapshot(req)
			return snap, req, nil
		}
		return snap, nil, nil
	}
	if strings.TrimSpace(snap.AgentProfile) == "" {
		snap.AgentProfile = restartDefaultProfileRef()
	}
	profile, err := agentprofile.ResolveProfile(filepath.Join(s.amuxDir, "agent-packs", "registry.json"), snap.AgentProfile)
	if err != nil {
		return snap, nil, err
	}
	// Recover content only: running a fresh execution projection would append
	// today's catalog default effort after the session's saved native arguments.
	profile.Type = snap.AgentType
	profile.ExecutionRuntimeRef = snap.AgentType
	profile.Model = snap.EffectiveModel
	if profile.Model == "" {
		profile.Model, _ = agentprofile.AgentArgsModel(snap.AgentType, snap.ExtraArgs)
	}
	req := runRequestFromLaunchSnapshot(snap)
	req.AgentProfile, req.ProfileSpec = profile.PinnedRef(), agentprofile.ToPBProfileSpec(profile)
	snap.AgentProfile = req.AgentProfile
	if snap.EffectiveModel == "" {
		snap.EffectiveModel = profile.Model
	}
	snap.LaunchSpec = launchSpecSnapshot(req)
	return snap, req, nil
}

func restartDefaultProfileRef() string {
	if fallback, ok := agentprofile.ResolveBuiltin("builtin/default"); ok {
		return fallback.Ref()
	}
	return agentprofile.DefaultProfileRef
}
