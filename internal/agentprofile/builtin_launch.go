package agentprofile

import "strings"

// BuiltinLaunchPreset is caller-owned execution policy for a builtin shortcut.
// It is not part of Profile discovery, pack content, or the pack contract.
type BuiltinLaunchPreset struct {
	Runtime        string
	Model          string
	PermissionMode string
}

var builtinLaunchPresets = map[string]BuiltinLaunchPreset{
	"claude": {Runtime: "claude", PermissionMode: "bypass"},
	"codex":  {Runtime: "codex", PermissionMode: "bypass"},
	"pi":     {Runtime: "pi"},
}

// BuiltinLaunchDefaults returns the amux shortcut configuration, never defaults
// for a portable external profile or an already resolved execution snapshot.
func BuiltinLaunchDefaults(profile Profile) (BuiltinLaunchPreset, bool) {
	if profile.Source != SourceBuiltin || profile.Namespace != BuiltinNamespace || profile.Type != "" {
		return BuiltinLaunchPreset{}, false
	}
	preset, ok := builtinLaunchPresets[profile.Name]
	return preset, ok
}

// ProjectProfileLaunchWithDefaults is the amux caller boundary. The strict pack
// projector still requires a runtime for v2; amux supplies its own builtin
// shortcut configuration before resolving and freezing the launch snapshot.
func ProjectProfileLaunchWithDefaults(profile Profile, args, env []string, opts LaunchOptions) (LaunchProjection, error) {
	preset, ok := BuiltinLaunchDefaults(profile)
	if !ok {
		return ProjectProfileLaunchChecked(profile, args, env, opts)
	}
	inferredRuntime := strings.TrimSpace(opts.Runtime) == ""
	if inferredRuntime {
		opts.Runtime = preset.Runtime
	}
	runtime, err := ResolveRuntimeOption(opts.Runtime)
	if err != nil {
		return LaunchProjection{}, err
	}
	// Preserve native --model precedence for the historical shortcut. Explicit
	// Runtime Catalog selections remain authoritative over legacy native args.
	if inferredRuntime {
		if model, found := AgentArgsModel(runtime.AgentType, args); found {
			opts.Model = model
		}
	}
	if strings.TrimSpace(opts.Model) == "" && inferredRuntime {
		opts.Model = preset.Model
	}
	// Only the owned launch copy receives execution policy; source content stays
	// portable. The projector freezes these defaults for retries/model switches.
	selected := cloneProfile(profile)
	selected.Type = runtime.AgentType
	selected.DefaultArgs, err = permissionModeArgs(runtime.AgentType, packProfilePermission{PermissionMode: preset.PermissionMode})
	if err != nil {
		return LaunchProjection{}, err
	}
	if inferredRuntime {
		// A shortcut still accepts native execution flags. Freeze the catalog
		// selection, then retain the legacy native-argument projection rules.
		selected, _, err = resolveProfileRuntimeSelection(selected, opts)
		if err != nil {
			return LaunchProjection{}, err
		}
		opts.Runtime = ""
		launch, err := ProjectProfileLaunchChecked(selected, args, env, opts)
		launch.Runtime = runtime.Ref
		return launch, err
	}
	return ProjectProfileLaunchChecked(selected, args, env, opts)
}
