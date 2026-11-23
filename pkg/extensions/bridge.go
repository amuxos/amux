package extensions

import (
	"io"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/bridge"
)

type BridgeExtension = bridge.Extension
type BridgeTextCommandRequest = bridge.TextCommandRequest
type BridgeExperimentToggle = bridge.ExperimentToggle
type BridgeOnboardingOptions = bridge.OnboardingOptions
type BridgeActivityView = agentui.ActivityView
type BridgeConsoleOptions = bridge.ConsoleOptions
type BridgeConsoleResult = bridge.ConsoleResult
type BridgeScopeManifest = bridge.ScopeManifest

// RegisterBridge installs an immutable, statically linked Bridge extension.
func RegisterBridge(extension BridgeExtension) error {
	return bridge.RegisterExtension(extension)
}

func BridgeSecretsDir() string { return bridge.DefaultSecretsDir() }

func BridgeRequiredScopeManifest() BridgeScopeManifest { return bridge.RequiredScopeManifest() }

// BridgeRenderQRCode uses the same terminal rendering as official provisioning.
func BridgeRenderQRCode(writer io.Writer, payload string) { bridge.RenderQRCode(writer, payload) }

type ConversationBinding = bridge.ConversationBinding
type ConversationSubmission = bridge.ConversationSubmission
type ConversationCompletion = bridge.ConversationCompletion
type ConversationCard = bridge.ConversationCard
type ConversationServices = bridge.ConversationServices
type ConversationObserver = bridge.ConversationObserver
