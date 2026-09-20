package extensions

import (
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/task"
)

type Profile = agentprofile.Profile
type ProfileProvider = agentprofile.Provider
type ProfileCustomization = agentprofile.Customization
type ProfileProjectionFile = agentprofile.ProjectionFile
type ProfilePiProviderConfig = agentprofile.PiProviderConfig
type BuiltinProfileMetadata = agentprofile.BuiltinMetadata
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
