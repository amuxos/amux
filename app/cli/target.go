package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func newSystemTargetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "target",
		Short: "Manage runtime targets",
	}
	c.AddCommand(newHubListCmd("list"))
	return c
}

func newHubListCmd(use string) *cobra.Command {
	c := supportsJSONOutput(&cobra.Command{
		Use:               use,
		Short:             "List runtimes connected to the hub",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = loadUserConfig()
			addr := hubAddr()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, err := grpc.DialContext(ctx, addr, hubDialOptions()...)
			if err != nil {
				return hubUnavailableError(addr, err)
			}
			defer conn.Close()

			client := pb.NewAmuxdClient(conn)
			resp, err := client.ListTargets(ctx, &pb.ListTargetsRequest{})
			if err != nil {
				return fmt.Errorf("list targets: %w", err)
			}

			return formatTargetList(cmd.OutOrStdout(), resp.Targets, jsonOutputEnabled(cmd))
		},
	})
	return c
}

func formatTargetList(out io.Writer, targets []*pb.Target, jsonOutput bool) error {
	if jsonOutput {
		views := make([]targetJSONView, 0, len(targets))
		for _, target := range targets {
			if target == nil {
				continue
			}
			summary := summarizePBSessions(target.Sessions)
			views = append(views, targetJSONView{
				ID:               target.Id,
				Name:             target.Name,
				Version:          target.Version,
				Sessions:         summary.Total,
				RunningSessions:  summary.Running,
				ArchivedSessions: summary.Archived,
				Capacity:         formatTargetCapacity(target),
				Agents:           summary.Agents,
				Capabilities:     target.Capabilities,
				Load:             target.Load,
				Status:           target.Status,
			})
		}
		return printJSON(out, map[string]any{"schema": "amux.system.target.list.v1", "targets": views})
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TARGET\tID\tSESSIONS\tRUNNING\tARCHIVED\tCAPACITY\tAGENTS\tCAPABILITIES")
	for _, target := range targets {
		sum := summarizePBSessions(target.Sessions)
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n",
			target.Name,
			target.Id,
			sum.Total,
			sum.Running,
			sum.Archived,
			formatTargetCapacity(target),
			formatAgentCounts(sum.Agents),
			targetCapabilitySummary(targetCapabilitiesToRegistry(target.Capabilities)),
		)
	}
	return w.Flush()
}

type targetJSONView struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Version          string                 `json:"version,omitempty"`
	Sessions         int                    `json:"sessions"`
	RunningSessions  int                    `json:"running_sessions"`
	ArchivedSessions int                    `json:"archived_sessions"`
	Capacity         string                 `json:"capacity"`
	Agents           map[string]int         `json:"agents"`
	Capabilities     *pb.TargetCapabilities `json:"capabilities,omitempty"`
	Load             *pb.RuntimeLoad        `json:"load,omitempty"`
	Status           string                 `json:"status,omitempty"`
}

func targetCapabilitySummary(caps *registry.Capabilities) string {
	if caps == nil {
		return "-"
	}
	seen := map[string]struct{}{}
	var parts []string
	add := func(values []string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			parts = append(parts, value)
		}
	}
	add(caps.AgentTypes)
	add(caps.RuntimeTools)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}
