package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"go.temporal.io/sdk/activity"
)

func CreatePRActivity(ctx context.Context, input CreatePRInput) (*PRResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Creating PR", "owner", input.Owner, "repo", input.Repo, "branch", input.BranchName)

	ghToken := input.GHToken
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}

	args := []string{
		"pr", "create",
		"--repo", fmt.Sprintf("%s/%s", input.Owner, input.Repo),
		"--head", input.BranchName,
		"--title", input.Title,
		"--body", input.Body,
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+ghToken)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh pr create: %w\n%s", err, string(out))
	}

	// gh pr create outputs the PR URL
	prURL := strings.TrimSpace(string(out))

	// Try to get PR number from gh pr view
	viewArgs := []string{
		"pr", "view", input.BranchName,
		"--repo", fmt.Sprintf("%s/%s", input.Owner, input.Repo),
		"--json", "number",
	}
	viewCmd := exec.CommandContext(ctx, "gh", viewArgs...)
	viewCmd.Env = append(os.Environ(), "GH_TOKEN="+ghToken)
	viewOut, _ := viewCmd.Output()

	var prInfo struct {
		Number int `json:"number"`
	}
	json.Unmarshal(viewOut, &prInfo) //nolint:errcheck

	logger.Info("PR created", "url", prURL, "number", prInfo.Number)
	return &PRResult{
		PRURL:    prURL,
		PRNumber: prInfo.Number,
	}, nil
}
