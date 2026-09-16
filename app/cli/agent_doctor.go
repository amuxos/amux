package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const agentSmokePrompt = "This is an amux readiness probe. Reply with AMUX_READY without using tools or modifying files."

// runAgentProfileSmoke verifies the complete profile -> hub -> scheduler ->
// runtime -> provider turn path. Static doctor checks intentionally stay free;
// callers opt in to this real model turn with --smoke.
func runAgentProfileSmoke(ctx context.Context, out io.Writer, profile agentprofile.Profile, timeoutSeconds int) error {
	return runAgentProfileSmokeWithLaunch(ctx, out, profile, "", "", timeoutSeconds)
}

func runAgentProfileSmokeWithLaunch(ctx context.Context, out io.Writer, profile agentprofile.Profile, runtimeRef, model string, timeoutSeconds int) error {
	if timeoutSeconds <= 0 {
		return fmt.Errorf("--smoke-timeout must be greater than zero")
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return fmt.Errorf("smoke task control plane: %w", err)
	}
	defer closeFn()

	spec := task.Spec{
		Agent:            profile.Ref(),
		Runtime:          strings.TrimSpace(runtimeRef),
		Model:            strings.TrimSpace(model),
		Task:             agentSmokePrompt,
		Isolation:        task.IsolationProcess,
		PermissionPolicy: task.PolicyDenyAll,
		Workspace: task.WorkspaceSpec{
			Policy: task.WorkspaceTempDir,
		},
		TimeoutSeconds: timeoutSeconds,
		Blocking:       true,
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode smoke task: %w", err)
	}
	smokeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds+15)*time.Second)
	defer cancel()
	resp, err := client.SubmitSubtask(smokeCtx, &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		return fmt.Errorf("submit smoke task: %w", err)
	}
	run, err := decodeTask(resp.TaskJson)
	if err != nil {
		return err
	}
	if err := streamTask(smokeCtx, client, run.ID, io.Discard, false, false); err != nil {
		cancelTaskBestEffort(client, run.ID)
		return fmt.Errorf("smoke task %s: %w", run.ID, err)
	}
	final, err := getTask(smokeCtx, client, run.ID)
	if err != nil {
		cancelTaskBestEffort(client, run.ID)
		return fmt.Errorf("get smoke task %s: %w", run.ID, err)
	}
	if !final.Status.IsTerminal() {
		cancelTaskBestEffort(client, run.ID)
		return fmt.Errorf("smoke task %s stream ended before a terminal status (last status: %s)", run.ID, final.Status)
	}
	if final.Status != task.StatusSucceeded {
		detail := final.Error
		if detail == "" {
			detail = string(final.Status)
		}
		return fmt.Errorf("smoke task %s did not succeed: %s", run.ID, detail)
	}
	fmt.Fprintf(out, "[OK] smoke: task %s completed one real turn on %s\n", run.ID, profile.Ref())
	fmt.Fprintf(out, "[READY] %s\n", profile.Ref())
	return nil
}
