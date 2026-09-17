package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/spf13/cobra"
)

func newFeishuGroupSettingsCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use: "group-settings", Short: "Manage group defaults in the local Hub installation",
		Long:              "Manage complete per-group settings on the Hub host. Requires the default-off bridge.feishu.group_settings.enabled experiment; enable it in the private /amux experiments page and restart amux. These commands use the local Bridge database, regardless of the configured remote Hub address. All results are JSON.",
		ValidArgsFunction: completeNoFiles,
	}
	c.PersistentFlags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Local Feishu bot profile name")
	scope := func(chatID string) (string, string, error) {
		// Unlike Hub-backed commands, this local operator surface must load the
		// installation's experiment and Bot defaults before reading the DB.
		if err := loadUserConfig(); err != nil && !os.IsNotExist(err) {
			return "", "", err
		}
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			return "", "", fmt.Errorf("chat_id is required")
		}
		secrets, err := bridge.LoadProfileSecrets(feishuProfileOrDefault(profile))
		if err != nil {
			return "", "", err
		}
		bridgeID := bridge.BridgeIdFromAppId(secrets.AppId)
		if bridgeID == "" {
			return "", "", fmt.Errorf("Feishu profile %s missing app_id", profile)
		}
		return bridgeID, chatID, nil
	}
	write := func(cmd *cobra.Command, value any) error {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	show := &cobra.Command{
		Use: "show <chat-id>", Short: "Export a complete versioned group settings JSON snapshot",
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			bridgeID, chatID, err := scope(args[0])
			if err != nil {
				return err
			}
			settings, _, err := bridge.GroupSettingsInDB(bridge.DefaultDBPath(), profile, bridgeID, chatID)
			if err != nil {
				return err
			}
			return write(cmd, settings)
		},
	}
	var file string
	apply := &cobra.Command{
		Use: "apply <chat-id>", Short: "Validate and atomically save a complete settings JSON snapshot",
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			bridgeID, chatID, err := scope(args[0])
			if err != nil {
				return err
			}
			var reader io.Reader = cmd.InOrStdin()
			if file != "-" {
				input, err := os.Open(file)
				if err != nil {
					return err
				}
				defer input.Close()
				reader = input
			}
			settings, err := decodeFeishuGroupSettings(reader)
			if err != nil {
				return err
			}
			if settings.BridgeID != bridgeID || settings.ChatID != chatID {
				return fmt.Errorf("JSON bridge_id/chat_id must match --profile and chat-id")
			}
			saved, err := bridge.SaveGroupSettingsInDB(bridge.DefaultDBPath(), profile, settings, settings.Version)
			if err != nil {
				return err
			}
			return write(cmd, saved)
		},
	}
	apply.Flags().StringVar(&file, "file", "", "Complete settings JSON file; use - for stdin (version is required)")
	_ = apply.MarkFlagRequired("file")
	var version int64
	newConversation := &cobra.Command{
		Use: "new-conversation <chat-id>", Short: "Retire a group singleton so the next task starts a new conversation",
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			bridgeID, chatID, err := scope(args[0])
			if err != nil {
				return err
			}
			if err := bridge.NewGroupConversationInDB(bridge.DefaultDBPath(), profile, bridgeID, chatID, version); err != nil {
				return err
			}
			settings, _, err := bridge.GroupSettingsInDB(bridge.DefaultDBPath(), profile, bridgeID, chatID)
			if err != nil {
				return err
			}
			return write(cmd, settings)
		},
	}
	newConversation.Flags().Int64Var(&version, "expected-version", 0, "Settings version from show")
	_ = newConversation.MarkFlagRequired("expected-version")
	c.AddCommand(show, apply, newConversation)
	return c
}

func decodeFeishuGroupSettings(reader io.Reader) (*bridge.GroupSettings, error) {
	// A full snapshot avoids treating an omitted field as a request to clear it.
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one settings JSON object")
	}
	for _, key := range []string{"bridge_id", "chat_id", "interaction_mode", "isolation_required", "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "thinking_process", "mode", "default_target", "default_cwd", "default_args", "idle_timeout_seconds", "thread_context_limit", "max_sessions_total", "max_sessions_per_user", "version"} {
		if value, ok := raw[key]; !ok || string(value) == "null" && key != "default_args" {
			return nil, fmt.Errorf("complete settings snapshot requires %s", key)
		}
	}
	var args []*string
	if err := json.Unmarshal(raw["default_args"], &args); err != nil {
		return nil, fmt.Errorf("default_args must be an array of strings: %w", err)
	}
	for _, arg := range args {
		if arg == nil {
			return nil, fmt.Errorf("default_args cannot contain null entries")
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var settings bridge.GroupSettings
	decoder = json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, err
	}
	return &settings, nil
}
