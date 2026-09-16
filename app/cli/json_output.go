package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const jsonOutputAnnotation = "amux.dev/json-output"

func supportsJSONOutput(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[jsonOutputAnnotation] = "true"
	return cmd
}

func validateJSONOutputSupport(cmd *cobra.Command, _ []string) error {
	if !jsonOutputEnabled(cmd) {
		return nil
	}
	if cmd.Annotations[jsonOutputAnnotation] == "true" {
		return nil
	}
	return commandValidationError(fmt.Errorf("command %q does not support --json", cmd.CommandPath()))
}

func jsonOutputEnabled(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	enabled, err := cmd.Flags().GetBool("json")
	return err == nil && enabled
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printJSONLine(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
