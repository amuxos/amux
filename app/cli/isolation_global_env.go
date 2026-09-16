package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/cobra"
)

var isolationGlobalEnvConfigPath = []string{"isolation", "global_env"}

func newIsolationGlobalEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "global-env",
		Short: "Manage environment secrets shared by identified isolated executions",
	}
	cmd.AddCommand(
		newIsolationGlobalEnvSetCmd(),
		newIsolationGlobalEnvUnsetCmd(),
		newIsolationGlobalEnvListCmd(),
	)
	return cmd
}

func newIsolationGlobalEnvSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <env-name>",
		Short: "Save one installation-wide isolated environment secret",
		Example: strings.TrimSpace(`
amux isolation global-env set GLM_API_KEY
printf '%s\n' "$GLM_API_KEY" | amux isolation global-env set GLM_API_KEY
`),
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName := args[0]
			if err := validateCredentialEnvName(envName); err != nil {
				return err
			}
			value, err := readCredentialValue(cmd.InOrStdin(), cmd.ErrOrStderr(), envName)
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("%s value must not be empty", envName)
			}
			if err := updateIsolationGlobalEnv(envName, &value); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved isolation global env %s\n", envName)
			fmt.Fprintln(cmd.OutOrStdout(), "restart the amux system to apply it to new isolated executions")
			return nil
		},
	}
}

func newIsolationGlobalEnvUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "unset <env-name>",
		Short:             "Remove one installation-wide isolated environment secret",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName := args[0]
			if err := validateCredentialEnvName(envName); err != nil {
				return err
			}
			if err := updateIsolationGlobalEnv(envName, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unset isolation global env %s\n", envName)
			fmt.Fprintln(cmd.OutOrStdout(), "restart the amux system to apply it to new isolated executions")
			return nil
		},
	}
}

func newIsolationGlobalEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ls",
		Aliases:           []string{"list"},
		Short:             "List shared isolated environment names without values",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := readCLIConfigFile()
			if err != nil {
				return err
			}
			entries, err := isolationGlobalEnvEntries(map[string]any(cfg))
			if err != nil {
				return err
			}
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				name, _, _ := strings.Cut(entry, "=")
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintln(cmd.OutOrStdout(), "ENV")
			for _, name := range names {
				fmt.Fprintln(cmd.OutOrStdout(), name)
			}
			return nil
		},
	}
}

func updateIsolationGlobalEnv(envName string, value *string) error {
	return config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		entries, err := isolationGlobalEnvEntries(raw)
		if err != nil {
			return err
		}
		updated := make([]string, 0, len(entries)+1)
		found := false
		for _, entry := range entries {
			name, _, _ := strings.Cut(entry, "=")
			if name != envName {
				updated = append(updated, entry)
				continue
			}
			found = true
			if value != nil {
				updated = append(updated, envName+"="+*value)
			}
		}
		if value != nil && !found {
			updated = append(updated, envName+"="+*value)
		}
		if err := config.ValidateIsolationGlobalEnv(updated); err != nil {
			return commandValidationError(err)
		}
		isolationSection, _ := raw["isolation"].(map[string]any)
		if isolationSection == nil {
			isolationSection = map[string]any{}
			raw["isolation"] = isolationSection
		}
		isolationSection["global_env"] = updated
		return nil
	})
}

func isolationGlobalEnvEntries(raw map[string]any) ([]string, error) {
	value, ok := nestedConfigValue(raw, isolationGlobalEnvConfigPath)
	if !ok {
		return nil, nil
	}
	var entries []string
	switch values := value.(type) {
	case []string:
		entries = append(entries, values...)
	case []any:
		entries = make([]string, 0, len(values))
		for _, item := range values {
			entry, ok := item.(string)
			if !ok {
				return nil, commandValidationError(fmt.Errorf("isolation.global_env must be a string array"))
			}
			entries = append(entries, entry)
		}
	default:
		return nil, commandValidationError(fmt.Errorf("isolation.global_env must be a string array"))
	}
	if err := config.ValidateIsolationGlobalEnv(entries); err != nil {
		return nil, commandValidationError(err)
	}
	return entries, nil
}

func redactIsolationGlobalEnvForDisplay(cfg cliConfigFile) bool {
	value, ok := nestedConfigValue(map[string]any(cfg), isolationGlobalEnvConfigPath)
	if !ok {
		return false
	}
	var values []any
	switch entries := value.(type) {
	case []string:
		values = make([]any, 0, len(entries))
		for _, entry := range entries {
			values = append(values, entry)
		}
	case []any:
		values = entries
	default:
		values = []any{value}
	}
	redacted := make([]string, 0, len(values))
	for _, value := range values {
		entry, ok := value.(string)
		name, _, hasValue := strings.Cut(entry, "=")
		if !ok || !hasValue || !credentialEnvNamePattern.MatchString(name) {
			redacted = append(redacted, "<invalid>=[REDACTED]")
			continue
		}
		redacted = append(redacted, name+"=[REDACTED]")
	}
	isolationSection, _ := map[string]any(cfg)["isolation"].(map[string]any)
	if isolationSection != nil {
		isolationSection["global_env"] = redacted
	}
	return true
}
