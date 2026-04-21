package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// streamEvent represents a parsed line from --output-format stream-json.
type streamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
	} `json:"message,omitempty"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	NumTurns int     `json:"num_turns,omitempty"`
}

// RunClaude invokes Claude Code CLI in the given workspace directory.
// heartbeatFn is called on each Claude event for Temporal activity heartbeating.
func RunClaude(ctx context.Context, workDir string, opts ClaudeOptions, prompt string, logFn func(msg string, kv ...any), heartbeatFn func(detail string)) (*ClaudeAnalysisResult, error) {
	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--model", opts.Model,
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	args = append(args, "--", prompt)

	logFn("Spawning claude CLI", "dir", workDir, "model", opts.Model)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	if opts.OAuthToken != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+opts.OAuthToken)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude start: %w", err)
	}
	logFn("Claude CLI started", "pid", cmd.Process.Pid)

	// Drain stderr
	var stderrBuf strings.Builder
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrBuf.WriteString(scanner.Text() + "\n")
		}
	}()

	// Parse stream-json
	var sessionID string
	var totalTokens int
	var analysisLines []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var event streamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		switch event.Type {
		case "assistant":
			for _, block := range event.Message.Content {
				switch block.Type {
				case "text":
					if block.Text != "" {
						analysisLines = append(analysisLines, block.Text)
						heartbeatFn("claude: " + truncate(block.Text, 200))
					}
				case "tool_use":
					heartbeatFn("claude tool: " + block.Name)
				}
			}
		case "result":
			sessionID = event.SessionID
			totalTokens = event.Usage.InputTokens + event.Usage.OutputTokens
			heartbeatFn(fmt.Sprintf("claude complete: %d tokens", totalTokens))
		}
	}

	if err := cmd.Wait(); err != nil {
		errMsg := stderrBuf.String()
		if errMsg != "" {
			return nil, fmt.Errorf("claude failed: %w: %s", err, errMsg)
		}
		return nil, fmt.Errorf("claude failed: %w", err)
	}

	return &ClaudeAnalysisResult{
		Analysis:   strings.Join(analysisLines, "\n"),
		SessionID:  sessionID,
		TokensUsed: totalTokens,
	}, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
