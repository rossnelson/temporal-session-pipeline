package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"go.temporal.io/sdk/activity"
)

func CommitAndPushActivity(ctx context.Context, input CommitAndPushInput) (*CommitAndPushResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Committing and pushing changes", "workspace", input.WorkspacePath, "branch", input.BranchName)

	// Check for dirty changes
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = input.WorkspacePath
	statusOut, err := statusCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	hasDirtyChanges := strings.TrimSpace(string(statusOut)) != ""

	// Check for local commits not yet pushed (branch ahead of upstream,
	// or branch has no upstream yet because push never succeeded).
	needsPush := false
	aheadCmd := exec.CommandContext(ctx, "git", "rev-list", "--count", "@{u}..HEAD")
	aheadCmd.Dir = input.WorkspacePath
	aheadOut, aheadErr := aheadCmd.Output()
	if aheadErr != nil {
		// No upstream — means push has never happened for this branch.
		needsPush = true
	} else if strings.TrimSpace(string(aheadOut)) != "0" {
		needsPush = true
	}

	if !hasDirtyChanges && !needsPush {
		logger.Info("No changes to commit and nothing to push")
		return &CommitAndPushResult{CommitSHA: ""}, nil
	}

	// Stage and commit only if there are dirty changes
	if hasDirtyChanges {
		addCmd := exec.CommandContext(ctx, "git", "add", "-A")
		addCmd.Dir = input.WorkspacePath
		if out, err := addCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git add: %w\n%s", err, string(out))
		}

		commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", input.CommitMessage)
		commitCmd.Dir = input.WorkspacePath
		if out, err := commitCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git commit: %w\n%s", err, string(out))
		}
	}

	// Get commit SHA (works whether we just committed or already had one)
	shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	shaCmd.Dir = input.WorkspacePath
	shaOut, _ := shaCmd.Output()
	sha := strings.TrimSpace(string(shaOut))

	// Push — rewrite origin with token so HTTPS auth works
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token != "" {
		getURL := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
		getURL.Dir = input.WorkspacePath
		if urlBytes, err := getURL.Output(); err == nil {
			url := strings.TrimSpace(string(urlBytes))
			if strings.HasPrefix(url, "https://github.com/") {
				newURL := "https://x-access-token:" + token + "@github.com/" + strings.TrimPrefix(url, "https://github.com/")
				setURL := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", newURL)
				setURL.Dir = input.WorkspacePath
				_, _ = setURL.CombinedOutput()
			}
		}
	}

	pushCmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", input.BranchName)
	pushCmd.Dir = input.WorkspacePath
	pushCmd.Env = os.Environ()
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git push: %w\n%s", err, string(out))
	}

	logger.Info("Changes committed and pushed", "sha", sha, "branch", input.BranchName)
	return &CommitAndPushResult{CommitSHA: sha}, nil
}
