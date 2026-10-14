package extensions

import (
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/task"
)

type BuiltinLaunchPreset = agentprofile.BuiltinLaunchPreset

type Profile = agentprofile.Profile
type ProfileProvider = agentprofile.Provider
type ProfileCustomization = agentprofile.Customization
type ProfileProjectionFile = agentprofile.ProjectionFile
type ProfilePiProviderConfig = agentprofile.PiProviderConfig
type BuiltinProfileMetadata = agentprofile.BuiltinMetadata
type ProfileRuntimeModelOption = agentprofile.RuntimeModelOption

type ProfileRuntimeCatalogSource = agentprofile.RuntimeCatalogSource
type ProfileRuntimeModelCapability = agentprofile.RuntimeModelCapability
type ProfileRuntimeModelVariant = agentprofile.RuntimeModelVariant
type ProfileModelOption = agentprofile.ModelOption
type ProfileLaunchOptions = agentprofile.LaunchOptions
type ProfileModelKind = agentprofile.ModelKind
type ProfileSandbox = task.Sandbox

const (
	ProfileModelKindDefault     = agentprofile.ModelKindDefault
	ProfileModelKindNative      = agentprofile.ModelKindNative
	ProfileModelKindProvider    = agentprofile.ModelKindProvider
	ProfileIsolationPrivateHome = task.IsolationPrivateHome
)

var (
	RegisterProfiles                 = agentprofile.RegisterCustomization
	FreezeProfiles                   = agentprofile.FreezeCustomization
	ConfiguredProfileModel           = agentprofile.ConfiguredModel
	ProfileConfigValue               = agentprofile.CodexConfigValue
	ReplaceProfileStructuredModelArg = agentprofile.ReplaceStructuredModelArg
	ProfileProjectionRoot            = agentprofile.ProjectionRoot
	WriteProfileFile                 = agentprofile.WriteProfileFile
	ProfileMCPEnvironment            = agentprofile.MCPEnvironment
	ProfileMCPCommand                = agentprofile.MCPCommand
	ProfileFromExecutionSnapshot     = agentprofile.FromPBProfileSpec
	ProjectProfileLaunchWithDefaults = agentprofile.ProjectProfileLaunchWithDefaults
	BuiltinLaunchDefaults            = agentprofile.BuiltinLaunchDefaults
	ProjectProfileLaunch             = agentprofile.ProjectProfileLaunch
	ProjectLegacyProfileLaunch       = agentprofile.ProjectLegacyLaunch
	ProjectProfileModelSwitch        = agentprofile.ProjectModelSwitch
	ReplaceProfileArgsModel          = agentprofile.ReplaceArgsModel
	ProfileArgsModel                 = agentprofile.AgentArgsModel
	ResolveBuiltinProfile            = agentprofile.ResolveBuiltin
	ProfileRuntimeCatalog            = agentprofile.RuntimeCatalog
	ResolveProfileRuntime            = agentprofile.ResolveRuntimeOption
	ResolveProfileRuntimeModel       = agentprofile.ResolveRuntimeModel
	MaterializeProfileMCP            = agentprofile.MaterializeAmuxMCP
	PrepareProfileSandbox            = task.PrepareSandbox
	RenderProfileWorkspace           = agentprofile.RenderProfileWorkspace
	RequireResponsesChat             = distribution.RequireResponsesChat
)

func DefaultProfileRef() string { return agentprofile.DefaultProfileRef }

// ProjectTaskPermissionArgs applies a Task policy to an engine's launch arguments.
// outerConfined must describe an enforced OS boundary, not a requested mode.
func ProjectTaskPermissionArgs(agentType, policy string, args []string, outerConfined bool) ([]string, error) {
	return agentprofile.TaskPermissionArgs(agentType, task.PermissionPolicy(policy), args, outerConfined)
}
