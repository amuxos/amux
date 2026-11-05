package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/spf13/cobra"
)

func newFeishuOnboardingCmd() *cobra.Command {
	var profile string
	var capabilities, enableDaily bool
	text := func(key string) string {
		return commandCatalog.Format(commandLocale(&cobra.Command{}), "onboarding."+key)
	}
	c := &cobra.Command{Use: "onboarding", Short: text("short"), Args: cobra.NoArgs, ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if enableDaily && !capabilities {
				return errors.New(commandCatalog.Format(commandLocale(cmd), "onboarding.daily_requires_capabilities"))
			}
			var sendErr error
			if capabilities {
				sendErr = bridge.SendCapabilityPlaza(ctx, profile, enableDaily)
			} else {
				sendErr = bridge.SendOnboardingGuide(ctx, profile)
			}
			if err := sendErr; err != nil {
				if errors.Is(err, bridge.ErrOnboardingOwnerRequired) {
					return errors.New(commandCatalog.Format(commandLocale(cmd), "onboarding.owner_required"))
				}
				return fmt.Errorf("%s: %w", commandCatalog.Format(commandLocale(cmd), "onboarding.failed"), err)
			}
			key := "onboarding.sent"
			if capabilities {
				key = "onboarding.capabilities_sent"
			}
			fmt.Fprintln(cmd.OutOrStdout(), commandCatalog.Format(commandLocale(cmd), key))
			return nil
		}}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, text("profile"))
	c.Flags().BoolVar(&capabilities, "capabilities", false, text("capabilities"))
	c.Flags().BoolVar(&enableDaily, "enable-daily", false, text("enable_daily"))
	return c
}
