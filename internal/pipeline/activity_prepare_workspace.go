package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.temporal.io/sdk/activity"
)

// PrepareWorkspaceActivity clones the dnd repo into a temp directory and creates a branch.
func PrepareWorkspaceActivity(ctx context.Context, input PrepareWorkspaceInput) (*PrepareWorkspaceResult, error) {
	logger := activity.GetLogger(ctx)

	// Create workspace directory under persistent storage.
	// /tmp is pod-ephemeral — when the pod restarts mid-workflow, the cloned repo is gone
	// but Temporal's history still has the cached path, causing downstream activities to fail
	// with "git: not a git repository". Writing under /data/workspaces keeps it alive
	// across pod restarts, matching the pattern for audio/chunks/transcripts under the PV mount.
	baseDir := os.Getenv("PIPELINE_DATA_DIR")
	if baseDir == "" {
		baseDir = "/data"
	}
	baseDir = filepath.Join(baseDir, "workspaces")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create workspace base dir: %w", err)
	}
	workDir, err := os.MkdirTemp(baseDir, "session-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}

	// Clone via gh CLI — uses GH_TOKEN/GITHUB_TOKEN from env for auth
	repoSlug := fmt.Sprintf("%s/%s", input.Options.Owner, input.Options.Repo)
	cloneCmd := exec.CommandContext(ctx, "gh", "repo", "clone", repoSlug, ".", "--", "--depth=1")
	cloneCmd.Dir = workDir
	cloneCmd.Env = os.Environ()
	if input.Options.GHToken != "" {
		cloneCmd.Env = append(cloneCmd.Env, "GH_TOKEN="+input.Options.GHToken)
	}
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("git clone: %w\n%s", err, string(out))
	}

	// Configure git identity
	for _, args := range [][]string{
		{"config", "user.email", "pipeline-bot@example.com"},
		{"config", "user.name", "Session Pipeline Bot"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		_, _ = cmd.CombinedOutput()
	}

	// Create branch
	branchName := fmt.Sprintf("pipeline/session-%s-%s", input.SessionID, time.Now().Format("2006-01-02-1504"))
	branchCmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	branchCmd.Dir = workDir
	if out, err := branchCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("git checkout -b: %w\n%s", err, string(out))
	}

	logger.Info("Workspace prepared", "path", workDir, "branch", branchName)
	return &PrepareWorkspaceResult{
		WorkspacePath: workDir,
		BranchName:    branchName,
	}, nil
}
