package task

import (
	"context"
	"os/exec"
	"strings"
)

// GitChangedFiles derives the changed files in a git worktree by diffing against
// a base commit (and including uncommitted changes). This is the structured,
// non-text source the design mandates for changed_files. Returns an empty slice
// (not an error) when the directory is not a usable git worktree.
func GitChangedFiles(ctx context.Context, workspaceDir, baseCommit string) ([]ChangedFile, error) {
	if workspaceDir == "" {
		return nil, nil
	}
	args := []string{"-C", workspaceDir, "diff", "--name-status"}
	if baseCommit != "" {
		args = append(args, baseCommit)
	}
	out, err := runGit(ctx, args...)
	if err != nil {
		// Not a git repo / bad base: changed_files is simply unavailable. The
		// design says it may be empty; do not fail the task on this.
		return nil, nil
	}
	files := parseNameStatus(out)

	// Include untracked files so newly created outputs are reported.
	if untracked, err := runGit(ctx, "-C", workspaceDir, "ls-files", "--others", "--exclude-standard"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(untracked), "\n") {
			p := strings.TrimSpace(line)
			if p == "" {
				continue
			}
			files = append(files, ChangedFile{Path: p, ChangeType: ChangeAdded})
		}
	}
	return files, nil
}

func parseNameStatus(out string) []ChangedFile {
	var files []ChangedFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status := fields[0]
		path := fields[len(fields)-1]
		ct := ChangeModified
		switch status[0] {
		case 'A':
			ct = ChangeAdded
		case 'D':
			ct = ChangeDeleted
		case 'R':
			ct = ChangeRenamed
		case 'M':
			ct = ChangeModified
		}
		files = append(files, ChangedFile{Path: path, ChangeType: ct})
	}
	return files
}

func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
