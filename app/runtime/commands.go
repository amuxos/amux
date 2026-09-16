package runtime

import "github.com/spf13/cobra"

func addDistributionCommands(root *cobra.Command) {
	if add := distributionHooks.AddCommands; add != nil {
		add(root)
	}
}
