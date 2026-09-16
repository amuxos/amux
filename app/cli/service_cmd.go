package cli

import (
	"fmt"
	"io"
	"strings"
)

func printRestartedLocalServiceProcesses(out io.Writer, restarted []trackedProcess) {
	if len(restarted) > 0 {
		fmt.Fprintf(out, "Restarted local server(s): %s\n", formatTrackedProcesses(restarted))
		fmt.Fprintln(out, "Note: MCP hosts and GUI launchers own reconnect/relaunch; this asked their old `amux mcp` / `amux gui serve` processes to exit.")
		return
	}
	fmt.Fprintln(out, "Restarted local server(s): none")
	fmt.Fprintln(out, "No registered running `amux mcp` / `amux gui serve` processes found.")
}

func formatTrackedProcesses(procs []trackedProcess) string {
	parts := make([]string, 0, len(procs))
	for _, proc := range procs {
		parts = append(parts, fmt.Sprintf("%s(pid %d)", proc.Role, proc.PID))
	}
	return strings.Join(parts, ", ")
}
